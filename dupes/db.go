// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"modernc.org/sqlite" // the database/sql driver, without cgo
	sqlite3 "modernc.org/sqlite/lib"
)

// schemaVersion is stored in user_version. A database of another version is
// refused rather than migrated: everything in it can be scanned again.
const schemaVersion = 1

// The files are the whole of what a scan learns; dirs is derived from them at
// the end of every scan, so it is always rebuilt and never updated.
//
// A file has partial when its size is shared, and hash when its size and
// partial are: a file without a hash has no twin. lname is the name in lower
// case, folded by Go, since SQLite folds only ASCII.
const schema = `
create table if not exists roots (
	path    text primary key,
	scanned integer not null
);

create table if not exists files (
	path    text primary key,
	dir     text not null,
	name    text not null,
	lname   text not null,
	ext     text not null,
	size    integer not null,
	mtime   integer not null,
	partial blob,
	hash    blob,
	seen    integer not null
);
create index if not exists files_size on files(size, partial);
-- size beside hash makes the index cover a grouping by hash: without it,
-- grouping the files of a few disks reads the size of each from the table,
-- a random read per file, and takes twenty seconds instead of half of one.
drop index if exists files_hash;
create index if not exists files_hash_size on files(hash, size);
create index if not exists files_lname on files(lname, size);
create index if not exists files_dir on files(dir);

create table if not exists dirs (
	path  text primary key,
	lname text not null,
	files integer not null,
	bytes integer not null,
	sig   blob
);
create index if not exists dirs_sig on dirs(sig);
create index if not exists dirs_lname on dirs(lname);
` + records

// records are the tables of the cleaning up: the files marked to be moved
// away, each with the copy of it that stays, and the files moved. A database
// a dupes before them made has none; see ensureRecords.
//
// A file that keeps another is never marked itself: the marking keeps it so,
// and apply checks it again before it moves anything.
const records = recordTables + `
create index if not exists marks_keep on marks(keep);
`

const recordTables = `
create table if not exists marks (
	path   text primary key,
	keep   text not null,
	marked integer not null
);

create table if not exists removed (
	id       integer primary key,
	path     text not null,
	keep     text not null,
	size     integer not null,
	hash     blob not null,
	trash    text not null,
	moved    integer not null,
	restored integer
);
`

// openDB opens the database at path. Only a scan creates one; the reports
// say that there is nothing to report instead.
func openDB(path string, create bool) (*sql.DB, error) {
	if create {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	} else if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("no database at %s: run dupes scan first", path)
	}

	dsn := path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: no reader can then hold rows open while a writer waits
	// for them, so every query is read to the end before the next one runs.
	db.SetMaxOpenConns(1)

	var v int
	if err := db.QueryRow(`pragma user_version`).Scan(&v); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	switch v {
	case 0:
		_, err = db.Exec(schema + fmt.Sprintf("pragma user_version = %d;", schemaVersion))
	case schemaVersion:
		// Only a scan brings the schema up to date — an index a newer dupes
		// adds. Building one over a disk's files holds the write lock for
		// longer than a scan running meanwhile waits for it, and a report
		// does without the index, only slower.
		if create {
			_, err = db.Exec(schema)
		} else {
			err = ensureRecords(db, false)
		}
	default:
		err = fmt.Errorf("schema version %d, this dupes knows %d: delete the file and scan again", v, schemaVersion)
	}
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return db, nil
}

// under returns the bounds of the paths below dir, for a range query on an
// index: every such path is >= lo and < hi.
func under(dir string) (lo, hi string) {
	lo = withSep(dir)
	return lo, lo[:len(lo)-1] + string(rune(os.PathSeparator)+1)
}

// withSep returns dir with a trailing separator, which a drive root already
// has.
func withSep(dir string) string {
	if strings.HasSuffix(dir, string(os.PathSeparator)) {
		return dir
	}
	return dir + string(os.PathSeparator)
}

// within reports whether path is dir or below it.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, withSep(dir))
}

// canonical turns a path from the command line into the form the walk
// records: absolute, clean, with symbolic links resolved and, on Windows, in
// the case the file system has, so that "d:\photos" finds "D:\Photos".
func canonical(path string) (string, error) {
	// "D:" is the current directory of drive D to Windows, never what the
	// one typing it means.
	if runtime.GOOS == "windows" && len(path) == 2 && path[1] == ':' {
		path += `\`
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}

// canonicalAll is canonical over a list.
func canonicalAll(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		c, err := canonical(p)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// ensureRecords makes sure that the tables of the cleaning up are there in a
// database a dupes before them made. Creating them takes the write lock,
// which a scan running meanwhile may hold for as long as it reads a batch of
// big files. A command that only reads does not wait for it: until the
// tables are there, it gets empty ones of its own, which vanish when it
// ends. A command that marks or moves does wait, for as long as SQLite
// waits for a lock, and says so if that is not long enough.
func ensureRecords(db *sql.DB, writing bool) error {
	var n int
	err := db.QueryRow(`select count(*) from main.sqlite_master
		where type = 'table' and name in ('marks', 'removed')`).Scan(&n)
	if err != nil || n == 2 {
		if err == nil {
			_, err = db.Exec(`drop table if exists temp.marks; drop table if exists temp.removed`)
		}
		return err
	}
	if !writing {
		if _, err := db.Exec(`pragma busy_timeout = 0`); err != nil {
			return err
		}
	}
	_, err = db.Exec(records)
	if !writing {
		if _, perr := db.Exec(`pragma busy_timeout = 10000`); perr != nil {
			return perr
		}
	}
	switch {
	case err == nil:
		_, err = db.Exec(`drop table if exists temp.marks; drop table if exists temp.removed`)
		return err
	case !isBusy(err):
		return err
	case writing:
		return errors.New("a scan is writing to the database and holds it: try again when it is done")
	}
	_, err = db.Exec(strings.ReplaceAll(recordTables, "create table", "create temp table"))
	return err
}

// isBusy reports whether err is SQLite's "database is locked", which the
// low byte of an extended code says too.
func isBusy(err error) bool {
	var e *sqlite.Error
	return errors.As(err, &e) && e.Code()&0xff == sqlite3.SQLITE_BUSY
}
