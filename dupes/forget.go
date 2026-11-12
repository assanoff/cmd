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
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/subcommands"
)

type forgetCmd struct {
	db string
}

func (*forgetCmd) Name() string { return "forget" }

func (*forgetCmd) Synopsis() string {
	return "drop a disk or a directory from the database"
}

func (*forgetCmd) Usage() string {
	return `forget [-db file] path...

Drops from the database every file recorded under each path, and the paths
scanned there, then compares what is left again. The files themselves are not
touched. A path need not exist any more: a disk sold or formatted is the
usual reason to forget it.

A directory inside a scanned path is forgotten until that path is scanned
again, which records it anew.

`
}

func (c *forgetCmd) SetFlags(f *flag.FlagSet) { dbFlag(f, &c.db) }

func (c *forgetCmd) Execute(ctx context.Context, f *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	return exit(c.run(ctx, f.Args()))
}

func (c *forgetCmd) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("forget: no path; dupes help forget says more")
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

	var missing []string
	for _, arg := range args {
		path, err := recorded(arg, roots)
		if err != nil {
			return err
		}
		files, n, err := forget(ctx, db, path)
		if err != nil {
			return err
		}
		if files == 0 && n == 0 {
			missing = append(missing, path)
			continue
		}
		fmt.Fprintf(os.Stderr, "%s: forgot %s", path, plural(files, "file"))
		if n > 0 {
			fmt.Fprintf(os.Stderr, " and %s", plural(n, "scanned path"))
		}
		fmt.Fprintln(os.Stderr)
	}

	if err := buildDirs(ctx, db); err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("nothing recorded under %s; dupes stats lists what was scanned",
			strings.Join(missing, ", "))
	}
	return nil
}

// recorded turns a path from the command line into the form the database
// has it in. A path that is still there is made canonical as a scan does;
// one that is gone can only be made absolute, and on Windows, where names
// ignore case, takes the case of the scanned path it is in.
func recorded(arg string, roots []root) (string, error) {
	if p, err := canonical(arg); err == nil {
		return p, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if runtime.GOOS == "windows" && len(arg) == 2 && arg[1] == ':' {
		arg += `\`
	}
	p, err := filepath.Abs(arg)
	if err != nil {
		return "", err
	}
	if runtime.GOOS != "windows" {
		return p, nil
	}
	for _, r := range roots {
		if len(p) >= len(r.path) && strings.EqualFold(p[:len(r.path)], r.path) &&
			within(r.path+p[len(r.path):], r.path) {
			return r.path + p[len(r.path):], nil
		}
		if within(strings.ToLower(r.path), strings.ToLower(p)) {
			// A root below p: p's own case cannot be known, but the
			// roots below it say what it is for the part they share.
			return r.path[:len(p)], nil
		}
	}
	return p, nil
}

// forget deletes the files under path and the roots at or under it, and
// says how many of each.
func forget(ctx context.Context, db *sql.DB, path string) (files, roots int64, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	lo, hi := under(path)
	res, err := tx.ExecContext(ctx,
		`delete from files where path = ? or (path >= ? and path < ?)`, path, lo, hi)
	if err != nil {
		return 0, 0, err
	}
	files, _ = res.RowsAffected()
	res, err = tx.ExecContext(ctx,
		`delete from roots where path = ? or (path >= ? and path < ?)`, path, lo, hi)
	if err != nil {
		return 0, 0, err
	}
	roots, _ = res.RowsAffected()
	return files, roots, tx.Commit()
}

type resetCmd struct {
	db string
}

func (*resetCmd) Name() string { return "reset" }

func (*resetCmd) Synopsis() string {
	return "delete the database, to start over"
}

func (*resetCmd) Usage() string {
	return `reset [-db file]

Deletes the database file, and the files SQLite keeps beside it, so that the
next scan starts from nothing. The files on the disks are not touched.

`
}

func (c *resetCmd) SetFlags(f *flag.FlagSet) { dbFlag(f, &c.db) }

func (c *resetCmd) Execute(_ context.Context, f *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	if f.NArg() > 0 {
		return exit(usagef("reset: takes no arguments, only -db"))
	}
	return exit(c.run())
}

func (c *resetCmd) run() error {
	removed := false
	for _, suffix := range []string{"", "-wal", "-shm"} {
		err := os.Remove(c.db + suffix)
		switch {
		case err == nil:
			removed = true
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
	}
	if !removed {
		fmt.Fprintf(os.Stderr, "no database at %s\n", c.db)
		return nil
	}
	fmt.Fprintf(os.Stderr, "deleted %s\n", c.db)
	return nil
}
