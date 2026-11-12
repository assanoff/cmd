// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"sync"
	"sync/atomic"
)

// partLen is how much of each end of a file the partial hash reads. Files of
// the same size mostly differ in their first or last blocks — a header, a
// trailer, an index — so a big file is read whole only when both ends match
// another's. A file no longer than both ends is read whole straight away, and
// its partial hash is its hash.
const partLen = 64 << 10

// The candidates for each pass, in the order of their paths, so that the
// files of one directory are read one after another.
const (
	partialQuery = `
select path, size from files
where partial is null and size in (
	select size from files group by size having count(*) > 1)
order by path`

	fullQuery = `
select path, size from files
where hash is null and partial is not null and (size, partial) in (
	select size, partial from files where partial is not null
	group by size, partial having count(*) > 1)
order by path`
)

type job struct {
	path string
	size int64
}

// hashed is a job done. hash is nil after a partial read of a big file.
type hashed struct {
	path    string
	size    int64
	partial []byte
	hash    []byte
	err     error
}

type hashStats struct {
	files  int
	bytes  int64
	unread int
	// offline is the roots scanned before that are not there now, a drive
	// unplugged: their files wait for it, rather than fail one by one.
	offline []string
}

// hashAll gives a partial hash to every file whose size is shared, and a
// hash to every file whose size and partial hash are. A file that cannot be
// read keeps no hash, which makes it unique: it is tried again next scan.
func hashAll(ctx context.Context, db *sql.DB, workers int, verbose bool, p *progress) (hashStats, error) {
	var st hashStats
	roots, err := listRoots(ctx, db)
	if err != nil {
		return st, err
	}
	for _, r := range roots {
		if _, err := os.Stat(r.path); err != nil {
			st.offline = append(st.offline, r.path)
		}
	}
	for _, pass := range []struct {
		name  string
		query string
		read  func(job) hashed
	}{
		{"comparing ends", partialQuery, readPartial},
		{"hashing", fullQuery, readFull},
	} {
		jobs, err := candidates(ctx, db, pass.query)
		if err != nil {
			return st, err
		}
		jobs = slices.DeleteFunc(jobs, func(j job) bool {
			return len(st.offline) > 0 && inAny(st.offline, j.path)
		})
		if len(jobs) == 0 {
			continue
		}
		err = runPass(ctx, db, jobs, workers, verbose, pass.name, pass.read, p, &st)
		if err != nil {
			return st, err
		}
	}
	return st, nil
}

func candidates(ctx context.Context, db *sql.DB, query string) ([]job, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.path, &j.size); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// runPass reads the jobs with a pool of workers and saves what they find
// from this goroutine alone, a batch at a time. On an interrupt the workers
// stop taking jobs and the hashes read so far are saved: the next scan has
// only the rest to do.
func runPass(ctx context.Context, db *sql.DB, jobs []job, workers int, verbose bool,
	name string, read func(job) hashed, p *progress, st *hashStats,
) error {
	var total int64
	for _, j := range jobs {
		total += j.size
	}

	todo := make(chan job)
	done := make(chan hashed)
	var wg sync.WaitGroup
	var bytes atomic.Int64
	for range workers {
		wg.Go(func() {
			for j := range todo {
				h := read(j)
				bytes.Add(j.size)
				done <- h
			}
		})
	}
	go func() {
		defer close(todo)
		for _, j := range jobs {
			select {
			case todo <- j:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(done)
	}()

	b := &batch{db: db, query: `update files set partial = coalesce(?, partial), hash = ? where path = ?`}
	var err error
	n := 0
	for h := range done {
		n++
		p.show("%s: %d of %d files, %s of %s", name, n, len(jobs),
			formatSize(bytes.Load()), formatSize(total))
		if h.err != nil {
			st.unread++
			if verbose {
				p.clear()
				log.Print(h.err)
			}
			continue
		}
		if h.hash != nil {
			st.files++
			st.bytes += h.size
		}
		if err == nil {
			err = b.exec(h.partial, h.hash, h.path)
		}
	}
	p.clear()
	if cerr := b.commit(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return fmt.Errorf("interrupted while %s; the hashes read are kept, scan again to finish", name)
	}
	return nil
}

// errChanged is a file whose size is not the one the walk recorded.
var errChanged = errors.New("changed since the walk")

func readPartial(j job) hashed {
	f, err := os.Open(j.path)
	if err != nil {
		return hashed{path: j.path, err: err}
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if j.size <= 2*partLen {
		n, err := io.Copy(h, f)
		if err == nil && n != j.size {
			err = errChanged
		}
		if err != nil {
			return hashed{path: j.path, err: fmt.Errorf("%s: %w", j.path, err)}
		}
		sum := h.Sum(nil)
		return hashed{path: j.path, size: j.size, partial: sum, hash: sum}
	}
	if _, err := io.CopyN(h, f, partLen); err != nil {
		return hashed{path: j.path, err: fmt.Errorf("%s: %w", j.path, err)}
	}
	if _, err := f.Seek(-partLen, io.SeekEnd); err != nil {
		return hashed{path: j.path, err: fmt.Errorf("%s: %w", j.path, err)}
	}
	if _, err := io.CopyN(h, f, partLen); err != nil {
		return hashed{path: j.path, err: fmt.Errorf("%s: %w", j.path, err)}
	}
	return hashed{path: j.path, partial: h.Sum(nil)}
}

func readFull(j job) hashed {
	f, err := os.Open(j.path)
	if err != nil {
		return hashed{path: j.path, err: err}
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	n, err := io.CopyBuffer(h, f, make([]byte, 1<<20))
	if err == nil && n != j.size {
		err = errChanged
	}
	if err != nil {
		return hashed{path: j.path, err: fmt.Errorf("%s: %w", j.path, err)}
	}
	return hashed{path: j.path, size: j.size, hash: h.Sum(nil)}
}
