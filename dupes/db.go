// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	_ "modernc.org/sqlite" // the database/sql driver, without cgo
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
// away, each with the copy of it that stays, and the files moved. They are
// small, so every command creates them if a database made before them has
// none; when they are there, which is always but once, that writes nothing.
//
// A file that keeps another is never marked itself: the marking keeps it so,
// and apply checks it again before it moves anything.
const records = `
create table if not exists marks (
	path   text primary key,
	keep   text not null,
	marked integer not null
);
create index if not exists marks_keep on marks(keep);

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
		// does without the index, only slower. The records are another
		// matter: creating an empty table takes no time.
		if create {
			_, err = db.Exec(schema)
		} else {
			_, err = db.Exec(records)
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
