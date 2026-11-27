// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// reviewSchema is the version of the review database (its user_version)
// whose tables forget knows.
const reviewSchema = "5"

// reviewDB returns the database review uses when not given -db.
func reviewDB() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "review", "review.db"), nil
}

// unposted returns the threads of the review in wt that hold remarks prr
// post has yet to send, the ones forget would lose.
func unposted(ctx context.Context, wt string) ([]*thread, error) {
	ts, err := threads(ctx, wt, false)
	if err != nil {
		return nil, err
	}
	var notes []*thread
	for _, t := range ts {
		if t.note() != "" {
			notes = append(notes, t)
		}
	}
	return notes, nil
}

// forget deletes from the review database db the repository at path with
// its changes, snapshots, threads and comments, so that the review server
// stops listing it. review has no command for that, so forget runs sqlite3,
// in one transaction that -bail rolls back on the first error. It reports
// whether the repository was there.
func forget(ctx context.Context, db, path string) (bool, error) {
	if _, err := os.Stat(db); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	v, err := output(command(ctx, "", "sqlite3", "-readonly", db, "PRAGMA user_version"))
	if err != nil {
		return false, err
	}
	if v != reviewSchema {
		return false, fmt.Errorf("%s has schema version %s, prr rm -forget knows %s",
			db, v, reviewSchema)
	}
	cmd := command(ctx, "", "sqlite3", "-bail", db)
	cmd.Stdin = strings.NewReader(forgetSQL(path))
	out, err := output(cmd)
	if err != nil {
		return false, err
	}
	return out == "1", nil
}

// sqlString quotes s as an SQL string literal.
func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// forgetSQL is the script that forgets the repository at path. The deletes
// go from the leaves up, and foreign_keys makes a table of a later schema
// that still points at the rows fail the transaction instead of dangling.
// It prints 1 if the repository was there, 0 if not.
func forgetSQL(path string) string {
	repo := sqlString(path)
	return `.timeout 5000
PRAGMA foreign_keys = ON;
BEGIN;
CREATE TEMP TABLE gone AS
	SELECT s.id FROM snapshot AS s JOIN change AS c ON c.id = s.change_id
	WHERE c.repo = ` + repo + `;
DELETE FROM comment WHERE thread_id IN
	(SELECT id FROM thread WHERE snapshot_id IN (SELECT id FROM gone));
DELETE FROM thread WHERE snapshot_id IN (SELECT id FROM gone);
DELETE FROM reviewed WHERE snapshot_id IN (SELECT id FROM gone);
DELETE FROM snapshot_mark WHERE snapshot_id IN (SELECT id FROM gone);
DELETE FROM snapshot WHERE id IN (SELECT id FROM gone);
DELETE FROM change WHERE repo = ` + repo + `;
DELETE FROM repo WHERE path = ` + repo + `;
SELECT changes();
COMMIT;
`
}
