// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What matters in cleaning up is what must never happen: the last copy of a
// file marked, a file moved that changed since it was compared or that is
// the copy it was to be moved for, and a file moved that cannot be put back.

func TestCleanUp(t *testing.T) {
	ctx := context.Background()
	root := tree(t, map[string][]byte{
		"a/x.bin":  []byte("one"),
		"b/x.bin":  []byte("one"),
		"c/y.bin":  []byte("two"),
		"d/y.bin":  []byte("two"),
		"e/z.bin":  []byte("three"),
		"f/z.bin":  []byte("three"),
		"only.txt": []byte("a size of its own"),
	})
	p := func(name string) string {
		return filepath.Join(root, filepath.FromSlash(name))
	}
	// A hard link is a copy by content and the same file on the disk.
	if err := os.Link(p("e/z.bin"), p("e/link.bin")); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "dupes.db")
	scan(t, dbPath, root)
	db, err := openDB(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// The last copy stays unmarked: a/x.bin is marked, so b/x.bin has no
	// copy left to keep.
	if st, err := markCopies(ctx, db, p("a"), "", false); err != nil || st.marked != 1 {
		t.Fatalf("mark a: %+v, %v; want 1 marked", st, err)
	}
	if st, err := markCopies(ctx, db, p("b"), "", false); err != nil || st.marked != 0 || st.skipped != 1 {
		t.Fatalf("mark b: %+v, %v; want 1 skipped", st, err)
	}
	// -keep keeps the copy there, even a marked one.
	if _, err := markCopies(ctx, db, p("c"), p("d"), false); err != nil {
		t.Fatal(err)
	}
	if err := keepOnly(ctx, db, p("e/z.bin")); err != nil {
		t.Fatal(err)
	}
	if got := marked(t, db, root); got != "a/x.bin<b/x.bin c/y.bin<d/y.bin e/link.bin<e/z.bin f/z.bin<e/z.bin" {
		t.Fatalf("marks: %s", got)
	}

	// a/x.bin changes after the scan, but keeps its size and time: only
	// reading it again shows it.
	fi, err := os.Stat(p("a/x.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p("a/x.bin"), []byte("ONE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p("a/x.bin"), time.Time{}, fi.ModTime()); err != nil {
		t.Fatal(err)
	}

	m, err := newMover(ctx, db, true)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := loadPlan(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	st, err := m.apply(ctx, plan, func(outcome) {})
	if err != nil || st.moved != 3 || st.skipped != 1 {
		t.Fatalf("dry run: %+v, %v; want 3 to move and the link skipped", st, err)
	}
	if _, err := os.Stat(p("c/y.bin")); err != nil {
		t.Fatalf("a dry run moved c/y.bin: %v", err)
	}

	m.dry = false
	skips := map[string]string{}
	st, err = m.apply(ctx, plan, func(o outcome) {
		if o.Err != nil {
			skips[strings.TrimPrefix(filepath.ToSlash(o.Path), filepath.ToSlash(root)+"/")] = o.Err.Error()
		}
	})
	if err != nil || st.moved != 2 {
		t.Fatalf("apply: %+v, %v; skipped %q", st, err, skips)
	}
	if !strings.Contains(skips["a/x.bin"], "not what was compared") ||
		!strings.Contains(skips["e/link.bin"], "one file") || len(skips) != 2 {
		t.Errorf("skipped %q", skips)
	}
	trash := filepath.Join(root, trashDir, m.stamp, "c", "y.bin")
	if _, err := os.Stat(trash); err != nil {
		t.Fatalf("c/y.bin is not in the trash: %v", err)
	}
	if _, err := os.Stat(p("c/y.bin")); err == nil {
		t.Fatal("c/y.bin is still there")
	}
	for _, f := range []string{"a/x.bin", "b/x.bin", "d/y.bin", "e/z.bin", "e/link.bin"} {
		if _, err := os.Stat(p(f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}

	// The trash is not scanned: what is in it is not a copy to compare.
	scan(t, dbPath, root)
	var n int
	if err := db.QueryRowContext(ctx, `select count(*) from files where path like ?`,
		"%"+trashDir+"%").Scan(&n); err != nil || n != 0 {
		t.Errorf("%d files of the trash recorded, %v", n, err)
	}

	rs, err := loadRemovals(ctx, db, `where path = ?`, p("c/y.bin"))
	if err != nil || len(rs) != 1 || rs[0].Keep != p("d/y.bin") {
		t.Fatalf("history of c/y.bin: %+v, %v", rs, err)
	}
	if err := restore(ctx, db, rs[0]); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(p("c/y.bin")); err != nil || string(b) != "two" {
		t.Fatalf("c/y.bin restored: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(root, trashDir, m.stamp, "c")); err == nil {
		t.Error("the trash directory a restore emptied is still there")
	}
	rs, _ = loadRemovals(ctx, db, `where path = ?`, p("c/y.bin"))
	if err := restore(ctx, db, rs[0]); err == nil {
		t.Error("restored twice")
	}
}

// marked lists the marks as path<keep, relative to root.
func marked(t *testing.T, db *sql.DB, root string) string {
	t.Helper()
	plan, err := loadPlan(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	r := func(p string) string {
		return filepath.ToSlash(strings.TrimPrefix(p, withSep(root)))
	}
	for _, s := range plan {
		out = append(out, r(s.Path)+"<"+r(s.Keep))
	}
	return strings.Join(out, " ")
}

// A database a dupes before marks made has no tables for them, and creating
// them waits for the write lock, which a scan of that dupes holds for
// minutes. The reports must not wait: they get empty tables of their own.
func TestRecordsWhileLocked(t *testing.T) {
	ctx := context.Background()
	root := tree(t, map[string][]byte{"a.txt": []byte("one"), "b.txt": []byte("one")})
	path := filepath.Join(t.TempDir(), "dupes.db")
	scan(t, path, root)

	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if _, err := other.Exec(`drop table marks; drop table removed`); err != nil {
		t.Fatal(err)
	}
	scanning, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanning.ExecContext(ctx, `begin immediate`); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	db, err := openDB(path, false)
	if err != nil {
		t.Fatalf("opening while a scan writes: %v", err)
	}
	defer func() { _ = db.Close() }()
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("opening while a scan writes took %v", d)
	}
	if n, _, err := markTotals(ctx, db); err != nil || n != 0 {
		t.Errorf("marks while a scan writes: %d, %v", n, err)
	}

	if _, err := scanning.ExecContext(ctx, `rollback`); err != nil {
		t.Fatal(err)
	}
	_ = scanning.Close()
	if err := ensureRecords(db, true); err != nil {
		t.Fatal(err)
	}
	var main, temp int
	if err := db.QueryRow(`select
		(select count(*) from main.sqlite_master where name in ('marks', 'removed')),
		(select count(*) from temp.sqlite_master where name in ('marks', 'removed'))`).
		Scan(&main, &temp); err != nil || main != 2 || temp != 0 {
		t.Errorf("tables: %d in the database, %d of its own, %v; want 2 and 0", main, temp, err)
	}
}
