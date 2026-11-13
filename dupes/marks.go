// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/subcommands"
)

// A mark says that a file is to be moved away and which copy of it stays.
// Marking changes nothing on the disks; apply does, later, and checks every
// mark again first.

// atOrUnder is the condition that a file is a path or under it, with the
// path and the bounds of under as its arguments.
const atOrUnder = "(path = ? or (path >= ? and path < ?))"

// keepOrder is the order in which dupes picks the copy of a file to keep
// when nobody said which: the oldest, since a copy is made after what it
// copies, then the one with the shortest path.
const keepOrder = "mtime, length(path), path"

type markStats struct {
	marked  int
	bytes   int64
	skipped int // files with no copy to keep
}

// markCopies marks the files at or under from that have a copy to keep:
// one at or under keep, or, with keep empty, one anywhere but in from that
// is not marked itself. A file with no such copy stays unmarked, so the last
// copy of anything is never marked. A copy in keep that was marked is not
// any more: the copies there are the ones to keep. dry counts and changes
// nothing.
func markCopies(ctx context.Context, db *sql.DB, from, keep string, dry bool) (markStats, error) {
	var st markStats
	if keep != "" && within(from, keep) {
		return st, usagef("%s is in %s, where the copies are kept", from, keep)
	}
	fromLo, fromHi := under(from)
	victims := `select path, hash, size from files
		where hash is not null and ` + atOrUnder
	vargs := []any{from, fromLo, fromHi}
	var keeper string
	var kargs []any
	if keep != "" {
		keepLo, keepHi := under(keep)
		victims += " and not " + atOrUnder
		vargs = append(vargs, keep, keepLo, keepHi)
		keeper = `select path from files where hash = ? and ` + atOrUnder +
			` order by ` + keepOrder + ` limit 1`
		kargs = []any{keep, keepLo, keepHi}
	} else {
		keeper = `select path from files f where hash = ? and not ` + atOrUnder + `
			and not exists (select 1 from marks m where m.path = f.path)
			order by ` + keepOrder + ` limit 1`
		kargs = []any{from, fromLo, fromHi}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return st, err
	}
	defer func() { _ = tx.Rollback() }()

	type victim struct {
		path string
		hash []byte
		size int64
	}
	var vs []victim
	rows, err := tx.QueryContext(ctx, victims+" order by path", vargs...)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var v victim
		if err := rows.Scan(&v.path, &v.hash, &v.size); err != nil {
			_ = rows.Close()
			return st, err
		}
		vs = append(vs, v)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}

	now := time.Now().Unix()
	for _, v := range vs {
		var k string
		err := tx.QueryRowContext(ctx, keeper, append([]any{v.hash}, kargs...)...).Scan(&k)
		if errors.Is(err, sql.ErrNoRows) {
			st.skipped++
			continue
		}
		if err != nil {
			return st, err
		}
		if err := mark(ctx, tx, v.path, k, now); err != nil {
			return st, err
		}
		st.marked++
		st.bytes += v.size
	}
	if dry {
		return st, nil
	}
	return st, tx.Commit()
}

// mark marks path to be moved with keep as the copy that stays, which is
// unmarked if it was marked, and makes keep the copy that stays for the
// files that had path as theirs.
func mark(ctx context.Context, tx *sql.Tx, path, keep string, now int64) error {
	for _, q := range []struct {
		query string
		args  []any
	}{
		{`delete from marks where path = ?`, []any{keep}},
		{
			`insert into marks (path, keep, marked) values (?, ?, ?)
			on conflict (path) do update set keep = excluded.keep, marked = excluded.marked`,
			[]any{path, keep, now},
		},
		{`update marks set keep = ? where keep = ?`, []any{keep, path}},
	} {
		if _, err := tx.ExecContext(ctx, q.query, q.args...); err != nil {
			return err
		}
	}
	return nil
}

// keepOnly marks every copy of the file at path but itself.
func keepOnly(ctx context.Context, db *sql.DB, path string) error {
	var hash []byte
	err := db.QueryRowContext(ctx, `select hash from files where path = ?`, path).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) || err == nil && hash == nil {
		return usagef("%s has no copies that dupes knows of", path)
	}
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`delete from marks where path in (select path from files where hash = ?)`, hash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into marks (path, keep, marked)
		select path, ?, ? from files where hash = ? and path <> ?`,
		path, time.Now().Unix(), hash, path); err != nil {
		return err
	}
	return tx.Commit()
}

// unmark drops the marks of the files at or under path.
func unmark(ctx context.Context, db *sql.DB, path string) (int64, error) {
	lo, hi := under(path)
	res, err := db.ExecContext(ctx,
		`delete from marks where path = ? or (path >= ? and path < ?)`, path, lo, hi)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// markTotals returns how many files are marked and their bytes.
func markTotals(ctx context.Context, db *sql.DB) (n, bytes int64, err error) {
	err = db.QueryRowContext(ctx, `
		select count(*), coalesce(sum(f.size), 0)
		from marks m left join files f on f.path = m.path`).Scan(&n, &bytes)
	return n, bytes, err
}

// dryFlags adds -dry-run, and -dry for short.
func dryFlags(f *flag.FlagSet, p *bool, what string) {
	f.BoolVar(p, "dry-run", false, what)
	f.BoolVar(p, "dry", false, "the same as -dry-run")
}

type markCmd struct {
	db   string
	keep string
	dry  bool
}

func (*markCmd) Name() string { return "mark" }

func (*markCmd) Synopsis() string {
	return "mark the files that have a copy elsewhere, for apply to move"
}

func (*markCmd) Usage() string {
	return `mark [-keep dir] [-dry-run] [-db file] path...

Marks the files at or under each path that have a copy elsewhere, for apply to
move away; nothing on the disks changes until then. A file whose every copy
is in the path, or marked already, stays unmarked: the last copy of a file is
never marked.

With -keep, only the files with a copy under that directory are marked, and
those copies are kept even if they were marked before:

	dupes mark E:\old                       # whatever in E:\old is elsewhere too
	dupes mark -keep D:\Photos E:\backup    # what of E:\backup D:\Photos has

dupes apply -dry-run lists what is marked, unmark takes marks back, and web
-rw marks and unmarks file by file.

`
}

func (c *markCmd) SetFlags(f *flag.FlagSet) {
	dbFlag(f, &c.db)
	f.StringVar(&c.keep, "keep", "", "mark only files with a copy under this `dir`, which is kept")
	dryFlags(f, &c.dry, "count what would be marked, and mark nothing")
}

func (c *markCmd) Execute(ctx context.Context, f *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	return exit(c.run(ctx, f.Args()))
}

func (c *markCmd) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("mark: no path; dupes help mark says more")
	}
	db, err := openDB(c.db, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	roots, err := listRoots(ctx, db)
	if err != nil {
		return err
	}
	keep := ""
	if c.keep != "" {
		if keep, err = recorded(c.keep, roots); err != nil {
			return err
		}
	}
	verb := "marked"
	if c.dry {
		verb = "would mark"
	}
	for _, arg := range args {
		from, err := recorded(arg, roots)
		if err != nil {
			return err
		}
		st, err := markCopies(ctx, db, from, keep, c.dry)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s: %s %s, %s", from, verb, plural(st.marked, "file"), formatSize(st.bytes))
		if st.skipped > 0 {
			fmt.Fprintf(os.Stderr, "; %d with no copy to keep left alone", st.skipped)
		}
		fmt.Fprintln(os.Stderr)
	}
	n, bytes, err := markTotals(ctx, db)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s marked in all, %s; dupes apply -dry-run lists them\n",
		plural(n, "file"), formatSize(bytes))
	return nil
}

type unmarkCmd struct {
	db  string
	all bool
}

func (*unmarkCmd) Name() string     { return "unmark" }
func (*unmarkCmd) Synopsis() string { return "take marks back" }

func (*unmarkCmd) Usage() string {
	return `unmark [-all] [-db file] path...

Takes back the marks of the files at or under each path, or with -all every
mark, so that apply leaves them where they are.

`
}

func (c *unmarkCmd) SetFlags(f *flag.FlagSet) {
	dbFlag(f, &c.db)
	f.BoolVar(&c.all, "all", false, "take back every mark")
}

func (c *unmarkCmd) Execute(ctx context.Context, f *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	return exit(c.run(ctx, f.Args()))
}

func (c *unmarkCmd) run(ctx context.Context, args []string) error {
	if c.all == (len(args) > 0) {
		return usagef("unmark: paths or -all, not both and not neither")
	}
	db, err := openDB(c.db, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if c.all {
		res, err := db.ExecContext(ctx, `delete from marks`)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		fmt.Fprintf(os.Stderr, "unmarked %s\n", plural(n, "file"))
		return nil
	}
	roots, err := listRoots(ctx, db)
	if err != nil {
		return err
	}
	for _, arg := range args {
		path, err := recorded(arg, roots)
		if err != nil {
			return err
		}
		n, err := unmark(ctx, db, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s: unmarked %s\n", path, plural(n, "file"))
	}
	return nil
}
