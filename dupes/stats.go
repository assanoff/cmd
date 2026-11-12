// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/google/subcommands"
)

type statsCmd struct {
	db string
}

func (*statsCmd) Name() string { return "stats" }

func (*statsCmd) Synopsis() string {
	return "say what the database holds and how much is duplicated"
}

func (*statsCmd) Usage() string {
	return `stats [-db file]

Prints the paths scanned and when, the files recorded, and how many of them
are duplicates: the totals the files and dirs commands list in detail.

`
}

func (c *statsCmd) SetFlags(f *flag.FlagSet) { dbFlag(f, &c.db) }

func (c *statsCmd) Execute(ctx context.Context, _ *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	return exit(c.run(ctx))
}

func (c *statsCmd) run(ctx context.Context) error {
	db, err := openDB(c.db, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	roots, err := listRoots(ctx, db)
	if err != nil {
		return err
	}
	s, err := summarize(ctx, db)
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "database\t%s\n", c.db)
	if fi, err := os.Stat(c.db); err == nil {
		_, _ = fmt.Fprintf(tw, "\t%s\n", formatSize(fi.Size()))
	}
	for i, r := range roots {
		label := ""
		if i == 0 {
			label = "scanned"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", label, r.path, r.scanned.Format("2006-01-02 15:04"))
	}
	_, _ = fmt.Fprintf(tw, "files\t%d, %s\n", s.files, formatSize(s.bytes))
	_, _ = fmt.Fprintf(tw, "hashed\t%d\n", s.hashed)
	_, _ = fmt.Fprintf(tw, "duplicate files\t%d in %d groups, %s to reclaim\n",
		s.dupFiles, s.groups, formatSize(s.waste))
	_, _ = fmt.Fprintf(tw, "duplicate dirs\t%d groups\n", s.dirGroups)
	return tw.Flush()
}

type root struct {
	path    string
	scanned time.Time
}

func listRoots(ctx context.Context, db *sql.DB) ([]root, error) {
	rows, err := db.QueryContext(ctx, `select path, scanned from roots order by path`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var roots []root
	for rows.Next() {
		var r root
		var t int64
		if err := rows.Scan(&r.path, &t); err != nil {
			return nil, err
		}
		r.scanned = time.Unix(t, 0)
		roots = append(roots, r)
	}
	return roots, rows.Err()
}

type summary struct {
	files, bytes, hashed    int64
	groups, dupFiles, waste int64
	dirGroups               int
}

func summarize(ctx context.Context, db *sql.DB) (summary, error) {
	var s summary
	err := db.QueryRowContext(ctx, `
		select count(*), coalesce(sum(size), 0), count(hash) from files`,
	).Scan(&s.files, &s.bytes, &s.hashed)
	if err != nil {
		return s, err
	}
	err = db.QueryRowContext(ctx, `
		select count(*), coalesce(sum(n), 0), coalesce(sum(w), 0) from (
			select count(*) as n, sum(size) - max(size) as w from files
			where hash is not null group by hash having n > 1)`,
	).Scan(&s.groups, &s.dupFiles, &s.waste)
	if err != nil {
		return s, err
	}
	groups, err := contentGroups(ctx, db)
	s.dirGroups = len(groups)
	return s, err
}
