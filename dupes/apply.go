// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/subcommands"
)

// trashDir is the directory at the top of each scanned path that apply moves
// the marked files of that path into, each under the path it had: moving a
// file within its disk is instant, takes no space and is undone by moving it
// back, on every system alike. dupes deletes nothing; emptying it is up to
// whoever has looked into it, and their file manager puts what they delete
// in the recycle bin.
const trashDir = ".dupes-trash"

// step is a marked file and the copy of it that stays, as the database has
// them. A file the database no longer has is zero in its fields.
type step struct {
	Path, Keep  string
	Size        int64
	Hash        []byte
	mtime       int64
	keepSize    int64
	keepMtime   int64
	keepHash    []byte
	keepMarked  bool
	known, kept bool // whether the database has the file and the copy
}

const stepQuery = `
select m.path, m.keep, f.path is not null, coalesce(f.size, 0), coalesce(f.mtime, 0), f.hash,
	k.path is not null, coalesce(k.size, 0), coalesce(k.mtime, 0), k.hash,
	exists (select 1 from marks x where x.path = m.keep)
from marks m
left join files f on f.path = m.path
left join files k on k.path = m.keep`

func scanStep(row interface{ Scan(...any) error }) (step, error) {
	var s step
	err := row.Scan(&s.Path, &s.Keep, &s.known, &s.Size, &s.mtime, &s.Hash,
		&s.kept, &s.keepSize, &s.keepMtime, &s.keepHash, &s.keepMarked)
	return s, err
}

// loadPlan returns every marked file, in the order of their paths.
func loadPlan(ctx context.Context, db *sql.DB) ([]step, error) {
	rows, err := db.QueryContext(ctx, stepQuery+` order by m.path`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var plan []step
	for rows.Next() {
		s, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		plan = append(plan, s)
	}
	return plan, rows.Err()
}

// loadStep reads the mark of path again, which may have changed or gone
// since the plan was read: web marks while apply moves.
func loadStep(ctx context.Context, db *sql.DB, path string) (step, bool, error) {
	s, err := scanStep(db.QueryRowContext(ctx, stepQuery+` where m.path = ?`, path))
	if errors.Is(err, sql.ErrNoRows) {
		return s, false, nil
	}
	return s, err == nil, err
}

// skipped is why a marked file stays where it is.
type skipped struct{ reason string }

func (e skipped) Error() string { return e.reason }

func skipf(format string, args ...any) error {
	return skipped{fmt.Sprintf(format, args...)}
}

// outcome is what became of a step: Err says why it was skipped, and Trash
// is where it went or, in a dry run, would go.
type outcome struct {
	step
	Trash string
	Err   error
}

// mover moves the marked files into the trash, one at a time, each after
// checking that it is still a copy of a file that stays. A dry run checks
// what can be checked without reading a file, and moves nothing.
type mover struct {
	db    *sql.DB
	roots []root
	stamp string // the directory of this run in each trash
	dry   bool
}

func newMover(ctx context.Context, db *sql.DB, dry bool) (*mover, error) {
	roots, err := listRoots(ctx, db)
	if err != nil {
		return nil, err
	}
	stamp := time.Now().Format("2006-01-02_150405")
	return &mover{db: db, roots: roots, stamp: stamp, dry: dry}, nil
}

// check returns why the step must not be taken, or nil. Every reason is
// one that would lose data or free nothing, and none of them is guessed
// past: the file stays.
func (m *mover) check(s step) error {
	switch {
	case !s.known:
		return skipf("no longer in the database: scan again, or unmark it")
	case s.Hash == nil:
		return skipf("changed since it was compared: scan again")
	case !s.kept:
		return skipf("the copy to keep, %s, is no longer in the database", s.Keep)
	case !bytes.Equal(s.Hash, s.keepHash):
		return skipf("the copy to keep, %s, is not a copy any more", s.Keep)
	case s.keepMarked:
		return skipf("the copy to keep, %s, is marked too", s.Keep)
	}
	fi, err := sameAsScanned(s.Path, s.Size, s.mtime)
	if err != nil {
		return err
	}
	ki, err := sameAsScanned(s.Keep, s.keepSize, s.keepMtime)
	if err != nil {
		return skipf("the copy to keep: %v", err)
	}
	if os.SameFile(fi, ki) {
		return skipf("one file with the copy to keep, by a link or a path scanned twice")
	}
	return nil
}

// sameAsScanned returns the file at path if it has the size and time that
// the scan recorded.
func sameAsScanned(path string, size, mtime int64) (fs.FileInfo, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, skipf("%v", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, skipf("%s is not a file any more", path)
	}
	if fi.Size() != size || fi.ModTime().UnixNano() != mtime {
		return nil, skipf("%s changed since the scan: scan again", path)
	}
	return fi, nil
}

// verify reads the file and the copy to keep whole and compares them with
// the hash, which only proves what it promises if both are read now.
func verify(s step) error {
	for _, p := range []string{s.Keep, s.Path} {
		h := readFull(job{path: p, size: s.Size})
		if h.err != nil {
			return skipf("%v", h.err)
		}
		if !bytes.Equal(h.hash, s.Hash) {
			return skipf("%s is not what was compared: scan again", p)
		}
	}
	return nil
}

// trashFor returns where in the trash of its scanned path a file goes.
func (m *mover) trashFor(path string) (string, error) {
	r := ""
	for _, x := range m.roots {
		if within(path, x.path) && len(x.path) > len(r) {
			r = x.path
		}
	}
	if r == "" {
		return "", skipf("not in a scanned path any more")
	}
	rel, err := filepath.Rel(r, path)
	if err != nil {
		return "", skipf("%v", err)
	}
	return filepath.Join(r, trashDir, m.stamp, rel), nil
}

// take checks the step and, unless this is a dry run, moves the file and
// records it. A reason not to is the outcome's; an error that stops the
// run is returned.
func (m *mover) take(ctx context.Context, path string) (outcome, error) {
	s, ok, err := loadStep(ctx, m.db, path)
	if err != nil {
		return outcome{}, err
	}
	o := outcome{step: s}
	if !ok {
		o.Path = path
		o.Err = skipf("not marked any more")
		return o, nil
	}
	if o.Err = m.check(s); o.Err != nil {
		return o, nil
	}
	if o.Trash, o.Err = m.trashFor(s.Path); o.Err != nil || m.dry {
		return o, nil
	}
	if o.Err = verify(s); o.Err != nil {
		return o, nil
	}
	o.Err, err = m.move(ctx, s, o.Trash)
	return o, err
}

// move moves the file to trash and records that in one transaction, which
// commits only once the file has moved. A move that fails is a reason to
// skip: a file on another disk than its trash is not copied.
func (m *mover) move(ctx context.Context, s step, trash string) (skip, err error) {
	if _, err := os.Lstat(trash); !errors.Is(err, fs.ErrNotExist) {
		return skipf("%s is there already", trash), nil
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []struct {
		query string
		args  []any
	}{
		{
			`insert into removed (path, keep, size, hash, trash, moved) values (?, ?, ?, ?, ?, ?)`,
			[]any{s.Path, s.Keep, s.Size, s.Hash, trash, time.Now().Unix()},
		},
		{`delete from files where path = ?`, []any{s.Path}},
		{`delete from marks where path = ?`, []any{s.Path}},
	} {
		if _, err := tx.ExecContext(ctx, q.query, q.args...); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(trash), 0o755); err != nil {
		return skipf("%v", err), nil
	}
	if err := os.Rename(s.Path, trash); err != nil {
		return skipf("not moved: %v", err), nil
	}
	if err := tx.Commit(); err != nil {
		if rerr := os.Rename(trash, s.Path); rerr != nil {
			return nil, fmt.Errorf("%w; and %s is left in %s: %v", err, s.Path, trash, rerr)
		}
		return nil, err
	}
	return nil, nil
}

// applyStats sums up a run.
type applyStats struct {
	moved, skipped int
	bytes          int64
}

// apply takes every step of the plan, calling each with what became of
// it, and then compares the directories again if any file moved.
func (m *mover) apply(ctx context.Context, plan []step, each func(outcome)) (applyStats, error) {
	var st applyStats
	var err error
	for _, s := range plan {
		if err = ctx.Err(); err != nil {
			break
		}
		var o outcome
		if o, err = m.take(ctx, s.Path); err != nil {
			break
		}
		if o.Err != nil {
			st.skipped++
		} else {
			st.moved++
			st.bytes += o.Size
		}
		each(o)
	}
	if st.moved > 0 && !m.dry {
		if berr := buildDirs(context.WithoutCancel(ctx), m.db); err == nil {
			err = berr
		}
	}
	return st, err
}

type applyCmd struct {
	db      string
	dry     bool
	verbose bool
}

func (*applyCmd) Name() string { return "apply" }

func (*applyCmd) Synopsis() string {
	return "move the marked files into the .dupes-trash of their disk"
}

func (*applyCmd) Usage() string {
	return `apply [-dry-run] [-v] [-db file]

Moves every marked file into .dupes-trash at the top of the scanned path it is
in, under the path it had: D:\a\b.jpg goes to D:\.dupes-trash\<time>\a\b.jpg.
Nothing is deleted, and dupes restore moves a file back; delete a trash
yourself once you have looked into it.

Before it moves a file, apply checks that the file and the copy of it that
stays are both where the scan saw them, unchanged, not one file under two
paths, and still the same content, read whole again. A file that fails any
check stays where it is, and apply says why. -dry-run makes every check but
the reading, and moves nothing.

`
}

func (c *applyCmd) SetFlags(f *flag.FlagSet) {
	dbFlag(f, &c.db)
	dryFlags(f, &c.dry, "list what would be moved, and move nothing")
	f.BoolVar(&c.verbose, "v", false, "name each file moved, not only those skipped")
}

func (c *applyCmd) Execute(ctx context.Context, f *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	if f.NArg() > 0 {
		return exit(usagef("apply: takes no arguments; mark says what to move"))
	}
	return exit(c.run(ctx))
}

func (c *applyCmd) run(ctx context.Context) error {
	db, err := openDB(c.db, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err := ensureRecords(db, true); err != nil {
		return err
	}
	plan, err := loadPlan(ctx, db)
	if err != nil {
		return err
	}
	if len(plan) == 0 {
		fmt.Fprintln(os.Stderr, "nothing is marked; dupes mark or web -rw marks files")
		return nil
	}
	m, err := newMover(ctx, db, c.dry)
	if err != nil {
		return err
	}
	prog := newProgress(false)
	n := 0
	st, err := m.apply(ctx, plan, func(o outcome) {
		n++
		prog.clear()
		switch {
		case o.Err != nil:
			_, _ = fmt.Fprintf(stdout, "skip  %s: %v\n", o.Path, o.Err)
		case c.dry:
			_, _ = fmt.Fprintf(stdout, "move  %s  (kept: %s)\n", o.Path, o.Keep)
		case c.verbose:
			_, _ = fmt.Fprintf(stdout, "moved %s -> %s\n", o.Path, o.Trash)
		}
		prog.show("%d of %d files", n, len(plan))
	})
	prog.clear()
	verb := "moved"
	if c.dry {
		verb = "would move"
	}
	fmt.Fprintf(os.Stderr, "%s %s, %s", verb, plural(st.moved, "file"), formatSize(st.bytes))
	if st.skipped > 0 {
		fmt.Fprintf(os.Stderr, "; %d skipped, each with the reason above", st.skipped)
	}
	fmt.Fprintln(os.Stderr)
	if errors.Is(err, context.Canceled) {
		return errors.New("interrupted; what was moved is recorded, apply again for the rest")
	}
	return err
}

// ---------------------------------------------------------------- history

// removal is a file apply moved.
type removal struct {
	ID       int64      `json:"id"`
	Path     string     `json:"path"`
	Keep     string     `json:"kept"`
	Size     int64      `json:"size"`
	Trash    string     `json:"trash"`
	Moved    time.Time  `json:"moved"`
	Restored *time.Time `json:"restored,omitempty"`
	hash     []byte
}

const removalQuery = `select id, path, keep, size, hash, trash, moved, restored from removed`

func loadRemovals(ctx context.Context, db *sql.DB, query string, args ...any) ([]removal, error) {
	rows, err := db.QueryContext(ctx, removalQuery+" "+query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []removal
	for rows.Next() {
		var r removal
		var moved int64
		var restored sql.NullInt64
		err := rows.Scan(&r.ID, &r.Path, &r.Keep, &r.Size, &r.hash, &r.Trash, &moved, &restored)
		if err != nil {
			return nil, err
		}
		r.Moved = time.Unix(moved, 0)
		if restored.Valid {
			t := time.Unix(restored.Int64, 0)
			r.Restored = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// restore moves a file apply moved back where it was, and records it in the
// database again with the hash it had.
func restore(ctx context.Context, db *sql.DB, r removal) error {
	if r.Restored != nil {
		return skipf("restored already")
	}
	fi, err := os.Lstat(r.Trash)
	if err != nil {
		return skipf("not in the trash any more: %v", err)
	}
	if _, err := os.Lstat(r.Path); !errors.Is(err, fs.ErrNotExist) {
		return skipf("something is at %s already", r.Path)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	name := filepath.Base(r.Path)
	if _, err := tx.ExecContext(ctx,
		`update removed set restored = ? where id = ?`, time.Now().Unix(), r.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into files (path, dir, name, lname, ext, size, mtime, hash, seen)
		values (?, ?, ?, ?, ?, ?, ?, ?, 0) on conflict (path) do nothing`,
		r.Path, filepath.Dir(r.Path), name, strings.ToLower(name), extOf(name),
		fi.Size(), fi.ModTime().UnixNano(), r.hash); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o755); err != nil {
		return skipf("%v", err)
	}
	if err := os.Rename(r.Trash, r.Path); err != nil {
		return skipf("not moved back: %v", err)
	}
	if err := tx.Commit(); err != nil {
		if rerr := os.Rename(r.Path, r.Trash); rerr != nil {
			return fmt.Errorf("%w; and %s is back, but not recorded: %v", err, r.Path, rerr)
		}
		return err
	}
	pruneTrash(r.Trash)
	return nil
}

// pruneTrash removes the directories of a trash that a restore left empty,
// up to the trash itself: dupes made them, and they hold nothing.
func pruneTrash(path string) {
	for dir := filepath.Dir(path); filepath.Base(dir) != trashDir; dir = filepath.Dir(dir) {
		if dir == filepath.Dir(dir) || os.Remove(dir) != nil {
			return
		}
	}
}

type historyCmd struct {
	db  string
	out output
}

func (*historyCmd) Name() string     { return "history" }
func (*historyCmd) Synopsis() string { return "list the files apply moved" }

func (*historyCmd) Usage() string {
	return `history [-n count] [-f text|csv|json] [-o file] [-db file]

Lists the files apply moved, the latest first: where each was, the copy that
stayed, and where in a trash it is now. dupes restore takes the numbers.

`
}

func (c *historyCmd) SetFlags(f *flag.FlagSet) {
	dbFlag(f, &c.db)
	c.out.flags(f)
}

func (c *historyCmd) Execute(ctx context.Context, f *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	if f.NArg() > 0 {
		return exit(usagef("history: takes no arguments"))
	}
	return exit(c.run(ctx))
}

func (c *historyCmd) run(ctx context.Context) error {
	if err := c.out.check(); err != nil {
		return err
	}
	db, err := openDB(c.db, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var total int
	if err := db.QueryRowContext(ctx, `select count(*) from removed`).Scan(&total); err != nil {
		return err
	}
	return c.out.write(total, func(w *writer, n int) error {
		rs, err := loadRemovals(ctx, db, `order by id desc limit ?`, n)
		if err != nil {
			return err
		}
		return writeRemovals(w, rs)
	})
}

func writeRemovals(w *writer, rs []removal) error {
	switch w.format {
	case "json":
		if rs == nil {
			rs = []removal{}
		}
		return w.json(rs)
	case "csv":
		rows := [][]string{{"id", "moved", "path", "size", "kept", "trash", "restored"}}
		for _, r := range rs {
			restored := ""
			if r.Restored != nil {
				restored = r.Restored.Format(time.RFC3339)
			}
			rows = append(rows, []string{
				itoa(r.ID), r.Moved.Format(time.RFC3339), r.Path, itoa(r.Size),
				r.Keep, r.Trash, restored,
			})
		}
		return w.csv(rows)
	}
	if w.total == 0 {
		_, _ = fmt.Fprintln(w, "nothing moved yet")
		return nil
	}
	for _, r := range rs {
		state := "in " + r.Trash
		if r.Restored != nil {
			state = "restored " + r.Restored.Format("2006-01-02 15:04")
		}
		_, _ = fmt.Fprintf(w, "%5d  %s  %9s  %s\n       kept: %s\n       %s\n",
			r.ID, r.Moved.Format("2006-01-02 15:04"), formatSize(r.Size), r.Path, r.Keep, state)
	}
	if len(rs) < w.total {
		_, _ = fmt.Fprintf(w, "%d of %d shown; -n 0 lists them all\n", len(rs), w.total)
	}
	return nil
}

type restoreCmd struct {
	db string
}

func (*restoreCmd) Name() string     { return "restore" }
func (*restoreCmd) Synopsis() string { return "move files apply moved back" }

func (*restoreCmd) Usage() string {
	return `restore [-db file] number|path...

Moves files apply moved back where they were: each by its number in dupes
history, or every one moved from a path or from under it. A file whose place
is taken again stays in the trash.

	dupes restore 12 13
	dupes restore E:\old\Photos

`
}

func (c *restoreCmd) SetFlags(f *flag.FlagSet) { dbFlag(f, &c.db) }

func (c *restoreCmd) Execute(ctx context.Context, f *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	return exit(c.run(ctx, f.Args()))
}

func (c *restoreCmd) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("restore: no number or path; dupes history lists them")
	}
	db, err := openDB(c.db, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err := ensureRecords(db, true); err != nil {
		return err
	}
	roots, err := listRoots(ctx, db)
	if err != nil {
		return err
	}
	var rs []removal
	for _, arg := range args {
		var found []removal
		if id, err := strconv.ParseInt(arg, 10, 64); err == nil {
			found, err = loadRemovals(ctx, db, `where id = ?`, id)
			if err != nil {
				return err
			}
		} else {
			path, err := recorded(arg, roots)
			if err != nil {
				return err
			}
			lo, hi := under(path)
			found, err = loadRemovals(ctx, db,
				`where restored is null and (path = ? or (path >= ? and path < ?)) order by id`,
				path, lo, hi)
			if err != nil {
				return err
			}
		}
		if len(found) == 0 {
			return fmt.Errorf("%s: nothing moved by that; dupes history lists what was", arg)
		}
		rs = append(rs, found...)
	}

	moved, failed := 0, 0
	for _, r := range rs {
		err := restore(ctx, db, r)
		if sk := (skipped{}); errors.As(err, &sk) {
			_, _ = fmt.Fprintf(stdout, "skip  %s: %v\n", r.Path, err)
			failed++
			continue
		}
		if err != nil {
			return err
		}
		moved++
	}
	if moved > 0 {
		if err := buildDirs(ctx, db); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "restored %s", plural(moved, "file"))
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "; %d left in the trash", failed)
	}
	fmt.Fprintln(os.Stderr)
	return nil
}
