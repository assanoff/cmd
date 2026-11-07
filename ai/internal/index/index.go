// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package index keeps what was said in media files, and finds it again.
//
// One SQLite file holds it all: the transcript of each file as whisper cut it,
// the overlapping windows of that transcript — passages — with a vector each
// in a sqlite-vec table and their words in an FTS5 table, and the short pieces
// a passage is made of, with a vector each too. A search asks the vectors and
// the words for passages and merges the answers, because each misses what the
// other finds: a vector finds "talked about taxes" in a minute that never says
// "taxes", and the words find a name or a term the embedding model has never
// seen. Then, in each passage found, the piece nearest the query says where in
// the minute the answer is.
//
// SQLite and sqlite-vec are the CGo-free builds from modernc.org, so this
// package adds no C toolchain to a program that has none: Kronk itself loads
// llama.cpp without cgo.
package index

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"     // the database/sql driver
	_ "modernc.org/sqlite/vec" // the vec0 table and the vec_* functions
)

// Segment is a stretch of speech as the transcript has it: a whisper segment,
// or a subtitle cue.
type Segment struct {
	Start, End time.Duration
	Text       string
}

// Chunk is a window of consecutive segments: a passage, which a search finds,
// or a piece, which it points at. Vec is its embedding; Windows leaves it for
// the caller to fill.
type Chunk struct {
	Start, End time.Duration
	Text       string
	Vec        []float32
}

// Media is one indexed file.
type Media struct {
	ID       int64
	Path     string // absolute
	Title    string
	Size     int64
	ModTime  time.Time
	Duration time.Duration
	Lang     string
	Chunks   int
	Indexed  time.Time
}

// ErrModel reports an index built with another embedding model. Vectors of two
// models live in two unrelated spaces, so a query embedded by one finds
// nothing meaningful among the passages of the other.
var ErrModel = errors.New("the index was built with another embedding model")

// Index is an open index file.
type Index struct {
	db *sql.DB
}

// schema is created on every open; each statement is a no-op on a file that
// already has it. The vec0 tables are not here: their width is that of the
// model's vectors, which only the first Put knows.
const schema = `
create table if not exists meta (
	key   text primary key,
	value text not null
);
create table if not exists media (
	id          integer primary key,
	path        text not null unique,
	title       text not null,
	size        integer not null,
	mtime       integer not null,
	duration_ms integer not null,
	lang        text not null,
	indexed_at  integer not null
);
create table if not exists segments (
	media_id integer not null references media(id) on delete cascade,
	start_ms integer not null,
	end_ms   integer not null,
	text     text not null
);
create index if not exists segments_media on segments(media_id, start_ms);
create table if not exists chunks (
	id       integer primary key,
	media_id integer not null references media(id) on delete cascade,
	start_ms integer not null,
	end_ms   integer not null,
	text     text not null
);
create index if not exists chunks_media on chunks(media_id);
create table if not exists pieces (
	id       integer primary key,
	media_id integer not null references media(id) on delete cascade,
	start_ms integer not null,
	end_ms   integer not null,
	text     text not null
);
create index if not exists pieces_media on pieces(media_id, start_ms);
create virtual table if not exists chunks_fts using fts5(
	text,
	tokenize = 'unicode61 remove_diacritics 2'
);
`

// Open opens the index at path, creating the file and its directory if they
// are not there yet.
func Open(path string) (*Index, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	// WAL lets ai seek web keep answering while an index run writes, and the
	// busy timeout makes the writer wait for a reader rather than fail.
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Index{db: db}, nil
}

// Close closes the file.
func (x *Index) Close() error { return x.db.Close() }

// Model reports the embedding model the index was built with and the width of
// its vectors, or "" when nothing has been indexed yet.
func (x *Index) Model(ctx context.Context) (string, int, error) {
	return model(ctx, x.db)
}

// querier is what both a database and a transaction answer.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// exists reports whether the index has a table, for the vec0 tables a file
// gets only with its first Put.
func exists(ctx context.Context, q querier, table string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `select count(*) from sqlite_master where name = ?`, table).Scan(&n)
	return n > 0, err
}

func model(ctx context.Context, q querier) (string, int, error) {
	var name, dims string
	err := q.QueryRowContext(ctx, `select
		coalesce((select value from meta where key = 'model'), ''),
		coalesce((select value from meta where key = 'dims'), '0')`).Scan(&name, &dims)
	if err != nil {
		return "", 0, err
	}
	n, err := strconv.Atoi(dims)
	if err != nil {
		return "", 0, fmt.Errorf("meta dims %q: %w", dims, err)
	}
	return name, n, nil
}

// Lookup returns the indexed file at path.
func (x *Index) Lookup(ctx context.Context, path string) (Media, bool, error) {
	found, err := x.media(ctx, "where m.path = ?", path)
	if err != nil || len(found) == 0 {
		return Media{}, false, err
	}
	return found[0], true, nil
}

// List returns every indexed file, by path.
func (x *Index) List(ctx context.Context) ([]Media, error) {
	return x.media(ctx, "")
}

// Get returns one indexed file. An id the index does not hold is an error that
// wraps os.ErrNotExist.
func (x *Index) Get(ctx context.Context, id int64) (Media, error) {
	found, err := x.media(ctx, "where m.id = ?", id)
	if err != nil {
		return Media{}, err
	}
	if len(found) == 0 {
		return Media{}, fmt.Errorf("media %d: %w", id, os.ErrNotExist)
	}
	return found[0], nil
}

// Transcript returns the whole transcript of one indexed file.
func (x *Index) Transcript(ctx context.Context, id int64) ([]Segment, error) {
	return x.Segments(ctx, id, 0, math.MaxInt64)
}

// Segments returns the segments of one indexed file that start in [from, to):
// those of a hit, given its Start and End.
func (x *Index) Segments(ctx context.Context, id int64, from, to time.Duration) ([]Segment, error) {
	rows, err := x.db.QueryContext(ctx, `select start_ms, end_ms, text from segments
		where media_id = ? and start_ms >= ? and start_ms < ? order by start_ms`,
		id, from.Milliseconds(), to.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var segs []Segment
	for rows.Next() {
		var start, end int64
		var s Segment
		if err := rows.Scan(&start, &end, &s.Text); err != nil {
			return nil, err
		}
		s.Start, s.End = ms(start), ms(end)
		segs = append(segs, s)
	}
	return segs, rows.Err()
}

func (x *Index) media(ctx context.Context, where string, args ...any) ([]Media, error) {
	rows, err := x.db.QueryContext(ctx, `
		select m.id, m.path, m.title, m.size, m.mtime, m.duration_ms, m.lang, m.indexed_at,
			(select count(*) from chunks c where c.media_id = m.id)
		from media m `+where+` order by m.path`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Media
	for rows.Next() {
		var m Media
		var mtime, dur, indexed int64
		err := rows.Scan(&m.ID, &m.Path, &m.Title, &m.Size, &mtime, &dur, &m.Lang, &indexed, &m.Chunks)
		if err != nil {
			return nil, err
		}
		m.ModTime = time.Unix(0, mtime)
		m.Duration = ms(dur)
		m.Indexed = time.Unix(indexed, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

// Put stores one file in place of whatever the index held for the same path:
// its transcript, its passages and its pieces, each passage and piece with its
// vector. model names the embedding model that made the vectors; the first Put
// fixes it for the index, and a later one from another model fails with
// ErrModel.
//
// It is one transaction, so an interrupted run leaves each file either indexed
// whole or not at all, and the next run picks up where this one stopped.
func (x *Index) Put(ctx context.Context, m Media, segs []Segment, passages, pieces []Chunk, model string) error {
	if len(passages) == 0 {
		return errors.New("nothing to index")
	}
	dims := len(passages[0].Vec)
	for _, c := range slices.Concat(passages, pieces) {
		if len(c.Vec) == 0 || len(c.Vec) != dims {
			return fmt.Errorf("a %d-wide vector among %d-wide ones", len(c.Vec), dims)
		}
	}

	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := claim(ctx, tx, model, dims); err != nil {
		return err
	}
	if err := clearFile(ctx, tx, m.Path); err != nil {
		return err
	}

	// An upsert rather than a delete and an insert: a file indexed again keeps
	// its id, so a link to its page — /media/{id} in a bookmark, or in a tab
	// still showing results from before — leads to it still.
	var id int64
	err = tx.QueryRowContext(ctx, `
		insert into media (path, title, size, mtime, duration_ms, lang, indexed_at) values (?, ?, ?, ?, ?, ?, ?)
		on conflict (path) do update set
			title = excluded.title, size = excluded.size, mtime = excluded.mtime,
			duration_ms = excluded.duration_ms, lang = excluded.lang, indexed_at = excluded.indexed_at
		returning id`,
		m.Path, m.Title, m.Size, m.ModTime.UnixNano(), m.Duration.Milliseconds(), m.Lang, time.Now().Unix(),
	).Scan(&id)
	if err != nil {
		return err
	}

	for _, s := range segs {
		// Whisper emits empty segments over silence; they are no part of
		// the transcript and nothing a hit could point at.
		if strings.TrimSpace(s.Text) == "" {
			continue
		}
		_, err := tx.ExecContext(ctx, `insert into segments (media_id, start_ms, end_ms, text) values (?, ?, ?, ?)`,
			id, s.Start.Milliseconds(), s.End.Milliseconds(), strings.TrimSpace(s.Text))
		if err != nil {
			return err
		}
	}

	for _, c := range passages {
		cid, err := insert(ctx, tx, "chunks", id, c)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into chunks_fts (rowid, text) values (?, ?)`, cid, c.Text); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `insert into chunks_vec (chunk_id, embedding) values (?, ?)`, cid, blob(c.Vec))
		if err != nil {
			return err
		}
	}
	for _, c := range pieces {
		pid, err := insert(ctx, tx, "pieces", id, c)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `insert into pieces_vec (piece_id, embedding) values (?, ?)`, pid, blob(c.Vec))
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// insert adds a passage or a piece of one file to its table, and returns its id.
func insert(ctx context.Context, tx *sql.Tx, table string, media int64, c Chunk) (int64, error) {
	res, err := tx.ExecContext(ctx, `insert into `+table+` (media_id, start_ms, end_ms, text) values (?, ?, ?, ?)`,
		media, c.Start.Milliseconds(), c.End.Milliseconds(), c.Text)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// claim checks the vectors against the model the index was built with, and on
// the first Put records the model and creates the vector tables at its width.
func claim(ctx context.Context, tx *sql.Tx, name string, dims int) error {
	have, width, err := model(ctx, tx)
	if err != nil {
		return err
	}
	if have != "" {
		if have != name {
			return fmt.Errorf("%w: %s, not %s", ErrModel, have, name)
		}
		if width != dims {
			return fmt.Errorf("%w: %s made %d-wide vectors, these are %d", ErrModel, have, width, dims)
		}
	}

	// Cosine rather than the default L2: it is what the embedding models are
	// trained for, and it reads as a similarity. "if not exists" because an
	// index made before pieces had only the first table.
	for _, t := range []struct{ table, key string }{
		{"chunks_vec", "chunk_id"},
		{"pieces_vec", "piece_id"},
	} {
		create := fmt.Sprintf(`create virtual table if not exists %s using vec0(
			%s integer primary key,
			embedding float[%d] distance_metric=cosine
		)`, t.table, t.key, dims)
		if _, err := tx.ExecContext(ctx, create); err != nil {
			return err
		}
	}
	if have != "" {
		return nil
	}
	_, err = tx.ExecContext(ctx, `insert into meta (key, value) values ('model', ?), ('dims', ?)`,
		name, strconv.Itoa(dims))
	return err
}

// Remove drops the file at path from the index, and reports whether it was
// there.
func (x *Index) Remove(ctx context.Context, path string) (bool, error) {
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	var n int
	err = tx.QueryRowContext(ctx, `select count(*) from media where path = ?`, path).Scan(&n)
	if err != nil || n == 0 {
		return false, err
	}
	if err := clearFile(ctx, tx, path); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `delete from media where path = ?`, path); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// clearFile deletes what the index holds for the file at path, all but the
// file's own row. The virtual tables take no part in the foreign keys, so they
// go by hand like the rest.
func clearFile(ctx context.Context, tx *sql.Tx, path string) error {
	const (
		file   = `select id from media where path = ?`
		chunks = `select id from chunks where media_id in (` + file + `)`
		pieces = `select id from pieces where media_id in (` + file + `)`
	)
	for _, del := range []struct{ table, stmt string }{
		{"chunks_fts", `delete from chunks_fts where rowid in (` + chunks + `)`},
		{"chunks_vec", `delete from chunks_vec where chunk_id in (` + chunks + `)`},
		{"pieces_vec", `delete from pieces_vec where piece_id in (` + pieces + `)`},
		{"chunks", `delete from chunks where media_id in (` + file + `)`},
		{"pieces", `delete from pieces where media_id in (` + file + `)`},
		{"segments", `delete from segments where media_id in (` + file + `)`},
	} {
		ok, err := exists(ctx, tx, del.table)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, del.stmt, path); err != nil {
			return err
		}
	}
	return nil
}

// blob is the little-endian float32 array sqlite-vec takes a vector as.
func blob(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

func ms(n int64) time.Duration { return time.Duration(n) * time.Millisecond }
