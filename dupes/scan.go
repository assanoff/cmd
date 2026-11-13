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
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/subcommands"
)

type scanCmd struct {
	db      string
	ext     string
	min     string
	exclude string
	jobs    int
	quiet   bool
	verbose bool
}

func (*scanCmd) Name() string { return "scan" }

func (*scanCmd) Synopsis() string {
	return "walk the paths, record their files and hash the candidates"
}

func (*scanCmd) Usage() string {
	return `scan [-ext list] [-min size] [-x names] [-j n] [-db file] path...

Walks each path and records every file that passes -ext and -min, then hashes
the files that could have a twin: those whose size another file shares. A path
scanned before is brought up to date, and a file unchanged since keeps its
hash; nothing on the disks is changed.

`
}

func (c *scanCmd) SetFlags(f *flag.FlagSet) {
	dbFlag(f, &c.db)
	f.StringVar(&c.ext, "ext", "", "record only these `extensions`, comma-separated: jpg,mp4")
	f.StringVar(&c.min, "min", "1", "record only files of at least this `size`: 0, 4K, 100M, 1.5G")
	f.StringVar(&c.exclude, "x", "$recycle.bin,system volume information",
		"skip directories with these `names`, comma-separated; * and ? match")
	f.IntVar(&c.jobs, "j", min(runtime.NumCPU(), 4), "hash this many `files` at once")
	f.BoolVar(&c.quiet, "q", false, "no progress line")
	f.BoolVar(&c.verbose, "v", false, "name each file or directory that could not be read")
}

func (c *scanCmd) Execute(ctx context.Context, f *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	return exit(c.run(ctx, f.Args()))
}

func (c *scanCmd) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("scan: no path to scan; dupes help scan says more")
	}
	if c.jobs < 1 {
		return usagef("scan: -j %d: at least one file at a time", c.jobs)
	}
	flt, err := parseFilter(c.ext, c.min)
	if err != nil {
		return err
	}
	roots, err := canonicalAll(args)
	if err != nil {
		return err
	}
	db, err := openDB(c.db, true)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	w := &walker{
		db:      db,
		filter:  flt,
		exclude: splitList(c.exclude),
		scan:    time.Now().UnixNano(),
		verbose: c.verbose,
		prog:    newProgress(c.quiet),
	}
	for _, root := range roots {
		st, err := w.walk(ctx, root)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s: %d files, %s", root, st.files, formatSize(st.bytes))
		if st.unread > 0 {
			fmt.Fprintf(os.Stderr, ", %d unreadable", st.unread)
		}
		if st.gone > 0 {
			fmt.Fprintf(os.Stderr, ", %d gone since the last scan", st.gone)
		}
		fmt.Fprintln(os.Stderr)
	}

	start := time.Now()
	hs, err := hashAll(ctx, db, c.jobs, c.verbose, w.prog)
	if err != nil {
		return err
	}
	for _, r := range hs.offline {
		fmt.Fprintf(os.Stderr, "%s: not there; its files are compared when it is\n", r)
	}
	if hs.files > 0 {
		fmt.Fprintf(os.Stderr, "hashed %d files, %s, in %s",
			hs.files, formatSize(hs.bytes), time.Since(start).Round(time.Second))
		if hs.unread > 0 {
			fmt.Fprintf(os.Stderr, ", %d unreadable", hs.unread)
		}
		fmt.Fprintln(os.Stderr)
	}

	if err := buildDirs(ctx, db); err != nil {
		return err
	}
	s, err := summarize(ctx, db)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%d duplicate files in %d groups, %s to reclaim; %d groups of duplicate directories\n",
		s.DupFiles, s.Groups, formatSize(s.Waste), s.DirGroups)
	return nil
}

// walker records the files of one scan.
type walker struct {
	db      *sql.DB
	filter  filter
	exclude []string
	scan    int64 // the mark of the files this scan has seen
	verbose bool
	prog    *progress
}

type walkStats struct {
	files  int
	bytes  int64
	unread int
	gone   int64
}

// A file whose size and time are those recorded keeps its hashes; any other
// change forgets them. SQLite reads the old row in every expression of the
// update, whatever the order of the assignments.
const upsertFile = `
insert into files (path, dir, name, lname, ext, size, mtime, seen)
values (?, ?, ?, ?, ?, ?, ?, ?)
on conflict (path) do update set
	partial = case when size = excluded.size and mtime = excluded.mtime then partial end,
	hash = case when size = excluded.size and mtime = excluded.mtime then hash end,
	dir = excluded.dir, name = excluded.name, lname = excluded.lname, ext = excluded.ext,
	size = excluded.size, mtime = excluded.mtime, seen = excluded.seen`

// walk records the files below root and then forgets those recorded there
// before that the walk did not see. An interrupted walk forgets nothing: it
// has not seen everything.
func (w *walker) walk(ctx context.Context, root string) (walkStats, error) {
	var st walkStats
	b := &batch{db: w.db, query: upsertFile}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if path == root {
				return err
			}
			// A directory that cannot be listed, or a file gone in the
			// meantime: the walk goes on without it.
			st.unread++
			if w.verbose {
				w.prog.clear()
				log.Print(err)
			}
			return nil
		}
		if d.IsDir() {
			// What apply moved away is not compared again: the copy it
			// was moved for is what is left of it.
			if path != root && (d.Name() == trashDir || match(w.exclude, d.Name())) {
				return filepath.SkipDir
			}
			return nil
		}
		// Symbolic links, junctions and devices are not files to compare;
		// a link to a directory is not followed, so no tree is seen twice.
		if !d.Type().IsRegular() {
			return nil
		}
		name := d.Name()
		ext := extOf(name)
		if !w.filter.extOK(ext) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			st.unread++
			return nil
		}
		if info.Size() < w.filter.min {
			return nil
		}
		st.files++
		st.bytes += info.Size()
		w.prog.show("walking: %d files, %s", st.files, formatSize(st.bytes))
		return b.exec(path, filepath.Dir(path), name, strings.ToLower(name), ext,
			info.Size(), info.ModTime().UnixNano(), w.scan)
	})
	w.prog.clear()
	if cerr := b.commit(); err == nil {
		err = cerr
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return st, fmt.Errorf("interrupted in %s; what was walked is kept, scan again to finish", root)
		}
		return st, err
	}

	tx, err := w.db.Begin()
	if err != nil {
		return st, err
	}
	defer func() { _ = tx.Rollback() }()
	lo, hi := under(root)
	res, err := tx.Exec(`delete from files where path >= ? and path < ? and seen <> ?`, lo, hi, w.scan)
	if err != nil {
		return st, err
	}
	st.gone, _ = res.RowsAffected()
	// A root replaces the roots below it, which its walk has just covered.
	if _, err := tx.Exec(`delete from roots where path >= ? and path < ?`, lo, hi); err != nil {
		return st, err
	}
	_, err = tx.Exec(`insert or replace into roots (path, scanned) values (?, ?)`, root, time.Now().Unix())
	if err != nil {
		return st, err
	}
	return st, tx.Commit()
}

// batch runs one statement many times, a few thousand to a transaction:
// SQLite commits a transaction at the speed of the disk, not of the rows.
// The rows wait in memory and go in together, so that the write lock is
// held while they are written and not while they are found: hashing a batch
// of big files takes minutes, which every other command opening the
// database would have to wait.
type batch struct {
	db    *sql.DB
	query string
	rows  [][]any
	since time.Time // when the oldest row waiting came
}

const (
	batchSize = 5000
	batchAge  = time.Second // a row waits no longer, if another one follows
)

func (b *batch) exec(args ...any) error {
	if len(b.rows) == 0 {
		b.since = time.Now()
	}
	b.rows = append(b.rows, args)
	if len(b.rows) >= batchSize || time.Since(b.since) >= batchAge {
		return b.commit()
	}
	return nil
}

// commit writes the rows waiting in one transaction.
func (b *batch) commit() error {
	if len(b.rows) == 0 {
		return nil
	}
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(b.query)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, r := range b.rows {
		if _, err := stmt.Exec(r...); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	b.rows = b.rows[:0]
	return nil
}

// match reports whether name matches one of the patterns, ignoring case.
func match(patterns []string, name string) bool {
	name = strings.ToLower(name)
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, name); ok {
			return true
		}
	}
	return false
}

// splitList splits a comma-separated flag into its items, in lower case.
func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// extOf returns the extension of a file name in lower case, without the dot.
func extOf(name string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
}

// filter is the -ext and -min of a command.
type filter struct {
	exts []string // empty for every extension
	min  int64
}

func parseFilter(exts, size string) (filter, error) {
	n, err := parseSize(size)
	if err != nil {
		return filter{}, usagef("-min %s: %v", size, err)
	}
	var f filter
	f.min = n
	for _, e := range splitList(exts) {
		f.exts = append(f.exts, strings.TrimPrefix(e, "."))
	}
	return f, nil
}

func (f filter) extOK(ext string) bool {
	if len(f.exts) == 0 {
		return true
	}
	for _, e := range f.exts {
		if e == ext {
			return true
		}
	}
	return false
}

// where returns the filter as a condition on the files table.
func (f filter) where() (string, []any) {
	cond := "size >= ?"
	args := []any{f.min}
	if len(f.exts) > 0 {
		cond += " and ext in (?" + strings.Repeat(", ?", len(f.exts)-1) + ")"
		for _, e := range f.exts {
			args = append(args, e)
		}
	}
	return cond, args
}

// progress is the line on standard error that a long scan rewrites in place.
// It is only drawn on a terminal: in a log it would be noise.
type progress struct {
	on    bool
	last  time.Time
	width int
}

func newProgress(quiet bool) *progress {
	fi, err := os.Stderr.Stat()
	return &progress{on: !quiet && err == nil && fi.Mode()&os.ModeCharDevice != 0}
}

// show draws the line, a few times a second at most.
func (p *progress) show(format string, args ...any) {
	if !p.on || time.Since(p.last) < 250*time.Millisecond {
		return
	}
	p.last = time.Now()
	line := fmt.Sprintf(format, args...)
	pad := max(p.width-len(line), 0)
	p.width = len(line)
	fmt.Fprintf(os.Stderr, "\r%s%s", line, strings.Repeat(" ", pad))
}

// clear blanks the line, before a message or the end.
func (p *progress) clear() {
	if !p.on || p.width == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\r%s\r", strings.Repeat(" ", p.width))
	p.width = 0
	p.last = time.Time{}
}
