// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// reviewSchemaSQL is the schema of the review database at version 5.
const reviewSchemaSQL = `
CREATE TABLE change (id INTEGER PRIMARY KEY, repo TEXT NOT NULL, change_key TEXT NOT NULL,
	UNIQUE(repo, change_key));
CREATE TABLE snapshot (id INTEGER PRIMARY KEY, change_id INTEGER NOT NULL REFERENCES change(id),
	n INTEGER NOT NULL);
CREATE TABLE thread (id INTEGER PRIMARY KEY,
	snapshot_id INTEGER NOT NULL REFERENCES snapshot(id), file TEXT NOT NULL);
CREATE TABLE comment (id INTEGER PRIMARY KEY, thread_id INTEGER NOT NULL REFERENCES thread(id),
	body TEXT NOT NULL);
CREATE TABLE reviewed (snapshot_id INTEGER NOT NULL REFERENCES snapshot(id), file TEXT NOT NULL,
	PRIMARY KEY(snapshot_id, file));
CREATE TABLE snapshot_mark (snapshot_id INTEGER NOT NULL REFERENCES snapshot(id),
	kind TEXT NOT NULL, PRIMARY KEY(snapshot_id, kind));
CREATE TABLE repo (path TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE);
CREATE TABLE pref (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

// seedRepo adds a repository with one change, snapshot, thread and comment,
// a reviewed file and a snapshot mark; n keeps the ids of repositories apart.
func seedRepo(path, name string, n int) string {
	r := strings.NewReplacer("$path", sqlString(path), "$name", sqlString(name), "$n", string(rune('0'+n)))
	return r.Replace(`
INSERT INTO repo VALUES ($path, $name);
INSERT INTO change VALUES ($n, $path, 'I$n');
INSERT INTO snapshot VALUES ($n, $n, 1);
INSERT INTO thread VALUES ($n, $n, 'a.go');
INSERT INTO comment VALUES ($n, $n, 'text');
INSERT INTO reviewed VALUES ($n, 'a.go');
INSERT INTO snapshot_mark VALUES ($n, 'reviewed');
`)
}

func sqlite(t *testing.T, db, script string) string {
	t.Helper()
	cmd := exec.Command("sqlite3", "-bail", db)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// rows counts the rows of every table that belong to the change n.
func rows(t *testing.T, db string, n int) string {
	t.Helper()
	return sqlite(t, db, strings.ReplaceAll(`SELECT
	(SELECT count(*) FROM repo WHERE path IN (SELECT repo FROM change WHERE id = $n))
		+ (SELECT count(*) FROM change WHERE id = $n)
		+ (SELECT count(*) FROM snapshot WHERE id = $n)
		+ (SELECT count(*) FROM thread WHERE id = $n)
		+ (SELECT count(*) FROM comment WHERE id = $n)
		+ (SELECT count(*) FROM reviewed WHERE snapshot_id = $n)
		+ (SELECT count(*) FROM snapshot_mark WHERE snapshot_id = $n);`, "$n", string(rune('0'+n))))
}

func newReviewDB(t *testing.T, version string, repos ...string) string {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("no sqlite3")
	}
	db := filepath.Join(t.TempDir(), "review.db")
	script := reviewSchemaSQL + "PRAGMA user_version = " + version + ";\n"
	for i, path := range repos {
		script += seedRepo(path, filepath.Base(path), i+1)
	}
	sqlite(t, db, script)
	return db
}

func TestForget(t *testing.T) {
	const gone, kept = "/home/u/prr/host/o's-repo-11", "/home/u/prr/host/repo-12"
	db := newReviewDB(t, reviewSchema, gone, kept)
	ctx := context.Background()

	found, err := forget(ctx, db, gone)
	if err != nil || !found {
		t.Fatalf("forget = %v, %v, want true, nil", found, err)
	}
	if n := sqlite(t, db, "SELECT count(*) FROM repo WHERE path = "+sqlString(gone)+";"); n != "0" {
		t.Errorf("the repository is still there")
	}
	if n := rows(t, db, 1); n != "0" {
		t.Errorf("%s rows of the forgotten repository are left, want 0", n)
	}
	if n := rows(t, db, 2); n != "7" {
		t.Errorf("%s rows of the other repository are left, want 7", n)
	}

	found, err = forget(ctx, db, gone)
	if err != nil || found {
		t.Errorf("forget again = %v, %v, want false, nil", found, err)
	}
}

func TestForgetOtherSchema(t *testing.T) {
	const path = "/home/u/prr/host/repo-11"
	db := newReviewDB(t, "6", path)

	if _, err := forget(context.Background(), db, path); err == nil ||
		!strings.Contains(err.Error(), "schema version 6") {
		t.Fatalf("forget = %v, want a schema version error", err)
	}
	if n := rows(t, db, 1); n != "7" {
		t.Errorf("%s rows are left, want all 7", n)
	}
}

func TestForgetNoDatabase(t *testing.T) {
	db := filepath.Join(t.TempDir(), "review.db")
	if found, err := forget(context.Background(), db, "/x"); err != nil || found {
		t.Errorf("forget = %v, %v, want false, nil", found, err)
	}
}

// A table of a later schema that points at the rows makes forget fail and
// leave everything in place, rather than leave the table dangling.
func TestForgetUnknownReference(t *testing.T) {
	const path = "/home/u/prr/host/repo-11"
	db := newReviewDB(t, reviewSchema, path)
	sqlite(t, db, `CREATE TABLE note (snapshot_id INTEGER NOT NULL REFERENCES snapshot(id));
INSERT INTO note VALUES (1);`)

	if _, err := forget(context.Background(), db, path); err == nil {
		t.Fatal("forget succeeded, want a foreign key error")
	}
	if n := rows(t, db, 1); n != "7" {
		t.Errorf("%s rows are left, want all 7", n)
	}
}
