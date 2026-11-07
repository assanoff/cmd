// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Index {
	t.Helper()
	x, err := Open(filepath.Join(t.TempDir(), "sub", "seek.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = x.Close() })
	return x
}

// put indexes a file whose chunks are one per text, a minute apart, with the
// given vectors, and each passage is a single piece.
func put(t *testing.T, x *Index, path, model string, texts []string, vecs [][]float32) {
	t.Helper()
	var segs []Segment
	var chunks []Chunk
	for i, text := range texts {
		start := time.Duration(i) * time.Minute
		segs = append(segs, Segment{Start: start, End: start + time.Minute, Text: text})
		chunks = append(chunks, Chunk{Start: start, End: start + time.Minute, Text: text, Vec: vecs[i]})
	}
	m := Media{
		Path:     path,
		Title:    filepath.Base(path),
		Size:     int64(len(texts)),
		ModTime:  time.Unix(1700000000, 0),
		Duration: time.Duration(len(texts)) * time.Minute,
	}
	if err := x.Put(context.Background(), m, segs, chunks, chunks, model); err != nil {
		t.Fatal(err)
	}
}

func TestSearchFindsByMeaningAndByWords(t *testing.T) {
	ctx := context.Background()
	x := open(t)
	put(t, x, "/a.mp3", "m", []string{"рассказывали про налоги", "погода в Москве"},
		[][]float32{{1, 0, 0}, {0, 1, 0}})
	put(t, x, "/b.mp3", "m", []string{"про отпуск и море"},
		[][]float32{{0, 0, 1}})

	// The vector alone: nearest to the third axis is the sea.
	hits, err := x.Search(ctx, []float32{0.1, 0, 0.9}, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != "/b.mp3" {
		t.Fatalf("vector search: got %+v, want /b.mp3", hits)
	}

	// The words alone, inflected: "налогами" has to find "налоги".
	hits, err = x.Search(ctx, nil, "говорили о налогами", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != "/a.mp3" || hits[0].Start != 0 {
		t.Fatalf("word search: got %+v, want the first minute of /a.mp3", hits)
	}

	// Both: a chunk the two halves agree on comes first.
	hits, err = x.Search(ctx, []float32{0, 0.9, 0.1}, "Москва", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Text != "погода в Москве" {
		t.Fatalf("hybrid search: got %+v, want the weather first", hits)
	}
}

func TestPutReplacesAFile(t *testing.T) {
	ctx := context.Background()
	x := open(t)
	put(t, x, "/a.mp3", "m", []string{"one", "two"}, [][]float32{{1, 0}, {0, 1}})
	put(t, x, "/a.mp3", "m", []string{"three"}, [][]float32{{1, 1}})

	ms, err := x.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].Chunks != 1 {
		t.Fatalf("got %+v, want one file with one chunk", ms)
	}
	// The page of a file is /media/{id}; indexing the file again must not
	// break a link to it.
	if ms[0].ID != 1 {
		t.Errorf("the file got id %d when indexed again, want its old id 1", ms[0].ID)
	}
	if segs, err := x.Transcript(ctx, ms[0].ID); err != nil || len(segs) != 1 || segs[0].Text != "three" {
		t.Errorf("transcript after replacing: %+v, %v; want only the new one", segs, err)
	}
	if hits, _ := x.Search(ctx, nil, "two", 5); len(hits) != 0 {
		t.Errorf("the replaced text is still found: %+v", hits)
	}
	if hits, _ := x.Search(ctx, []float32{1, 0}, "", 5); len(hits) != 1 {
		t.Errorf("got %d vector hits, want only the new chunk's", len(hits))
	}

	// The vector tables are outside the foreign keys; their rows have to go
	// by hand, and must have.
	for _, table := range []string{"chunks_vec", "pieces_vec"} {
		var n int
		if err := x.db.QueryRow(`select count(*) from ` + table).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s holds %d rows (%v), want the new file's one", table, n, err)
		}
	}
}

func TestPutRefusesAnotherModel(t *testing.T) {
	x := open(t)
	put(t, x, "/a.mp3", "m", []string{"one"}, [][]float32{{1, 0}})

	m := Media{Path: "/b.mp3"}
	err := x.Put(context.Background(), m, nil, []Chunk{{Text: "two", Vec: []float32{1, 0}}}, nil, "other")
	if !errors.Is(err, ErrModel) {
		t.Errorf("another model: got %v, want ErrModel", err)
	}
	err = x.Put(context.Background(), m, nil, []Chunk{{Text: "two", Vec: []float32{1, 0, 0}}}, nil, "m")
	if !errors.Is(err, ErrModel) {
		t.Errorf("another width: got %v, want ErrModel", err)
	}
	pieces := []Chunk{{Text: "two", Vec: []float32{1, 0, 0}}}
	err = x.Put(context.Background(), m, nil, []Chunk{{Text: "two", Vec: []float32{1, 0}}}, pieces, "m")
	if err == nil {
		t.Error("pieces wider than their passages: got no error")
	}

	name, dims, err := x.Model(context.Background())
	if err != nil || name != "m" || dims != 2 {
		t.Errorf("Model = %q, %d, %v; want m, 2", name, dims, err)
	}
}

func TestLookupGetRemove(t *testing.T) {
	ctx := context.Background()
	x := open(t)
	put(t, x, "/a.mp3", "m", []string{"one", "two"}, [][]float32{{1, 0}, {0, 1}})

	m, ok, err := x.Lookup(ctx, "/a.mp3")
	if err != nil || !ok {
		t.Fatalf("Lookup: %v, %v", ok, err)
	}
	if !m.ModTime.Equal(time.Unix(1700000000, 0)) || m.Duration != 2*time.Minute {
		t.Errorf("Lookup lost the file's state: %+v", m)
	}

	if got, err := x.Get(ctx, m.ID); err != nil || got.Path != "/a.mp3" {
		t.Errorf("Get: %+v, %v", got, err)
	}
	if _, err := x.Get(ctx, m.ID+1); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Get of an unknown id: %v, want os.ErrNotExist", err)
	}
	segs, err := x.Transcript(ctx, m.ID)
	if err != nil || len(segs) != 2 || segs[1].Text != "two" || segs[1].Start != time.Minute {
		t.Errorf("Transcript: %+v, %v", segs, err)
	}

	if gone, err := x.Remove(ctx, "/a.mp3"); err != nil || !gone {
		t.Fatalf("Remove: %v, %v", gone, err)
	}
	if gone, err := x.Remove(ctx, "/a.mp3"); err != nil || gone {
		t.Errorf("Remove twice: %v, %v; want false", gone, err)
	}
	if hits, _ := x.Search(ctx, []float32{1, 0}, "one", 5); len(hits) != 0 {
		t.Errorf("a removed file is still found: %+v", hits)
	}
}

// A minute of speech where taxes come up 30 seconds in: the hit is the
// minute, and its moment the piece about taxes.
func TestSearchPinpoints(t *testing.T) {
	ctx := context.Background()
	x := open(t)

	var segs []Segment
	var pieces []Chunk
	for i := range 6 {
		start := time.Duration(i) * 10 * time.Second
		s := Segment{Start: start, End: start + 10*time.Second, Text: "о погоде"}
		vec := []float32{0, 1}
		if i == 3 {
			s.Text, vec = "про налоги", []float32{1, 0}
		}
		segs = append(segs, s)
		pieces = append(pieces, Chunk{Start: s.Start, End: s.End, Text: s.Text, Vec: vec})
	}
	passage := Chunk{Start: 0, End: time.Minute, Text: "весь разговор", Vec: []float32{0.6, 0.8}}
	if err := x.Put(ctx, Media{Path: "/a.mp3"}, segs, []Chunk{passage}, pieces, "m"); err != nil {
		t.Fatal(err)
	}

	hits, err := x.Search(ctx, []float32{1, 0}, "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Start != 0 || hits[0].At != 30*time.Second || hits[0].Quote != "про налоги" {
		t.Fatalf("got %+v, want the minute pinned to 30s", hits)
	}

	// Found by its words alone there is nothing to compare the pieces with:
	// the hit keeps its passage.
	hits, err = x.Search(ctx, nil, "разговор", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].At != 0 || hits[0].Quote != "весь разговор" {
		t.Errorf("words alone: got %+v, want the passage", hits)
	}
}

// A file far from the query is not listed below the one that answers it just
// because it is the next nearest; one nearly as near is.
func TestSearchDropsFarHits(t *testing.T) {
	ctx := context.Background()
	x := open(t)
	put(t, x, "/answer.mp3", "m", []string{"в Домбай"}, [][]float32{{1, 0}})
	put(t, x, "/close.mp3", "m", []string{"в горы"}, [][]float32{{0.9, 0.436}})
	put(t, x, "/far.mp3", "m", []string{"про базы данных"}, [][]float32{{0.6, 0.8}})

	hits, err := x.Search(ctx, []float32{1, 0}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range hits {
		got = append(got, h.Path)
	}
	if len(got) != 2 || got[0] != "/answer.mp3" || got[1] != "/close.mp3" {
		t.Errorf("got %v, want the answer and the close one, not the far one", got)
	}
	if hits[0].Similarity < 0.99 {
		t.Errorf("similarity of the answer: %v, want 1", hits[0].Similarity)
	}
}

func TestRelevantKeepsUncompared(t *testing.T) {
	hits := []Hit{{Path: "/a"}, {Path: "/b"}, {Path: "/c"}}
	if got := relevant(hits, 2); len(got) != 2 {
		t.Errorf("hits with no similarity: got %d, want the limit of them", len(got))
	}
}

// An index nothing was put in yet has no vector table; searching it is not an
// error, it just finds nothing.
func TestSearchAnEmptyIndex(t *testing.T) {
	hits, err := open(t).Search(context.Background(), []float32{1, 0}, "anything", 5)
	if err != nil || len(hits) != 0 {
		t.Errorf("got %v, %v; want nothing", hits, err)
	}
}

func TestDistinctDropsOverlappingWindows(t *testing.T) {
	hits := []Hit{
		{MediaID: 1, Start: 45 * time.Second, End: 105 * time.Second},
		{MediaID: 1, Start: 0, End: 60 * time.Second}, // overlaps the first
		{MediaID: 2, Start: 0, End: 60 * time.Second}, // another file
		{MediaID: 1, Start: 120 * time.Second, End: 180 * time.Second},
	}
	got := distinct(hits, 10)
	if len(got) != 3 || got[1].MediaID != 2 {
		t.Errorf("got %+v, want the overlapping window dropped", got)
	}
	if got := distinct(hits, 1); len(got) != 1 {
		t.Errorf("limit 1: got %d hits", len(got))
	}
}

func TestMatchQuery(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Про это говорили", `"про" OR "это" OR "говор"*`},
		{"налоги налогами налогов", `"нало"* OR "налог"*`},
		{"Москва в Москве", `"моск"* OR "в"`},
		{`"NOT" -налоги-`, `"not" OR "нало"*`},
		{"в 2024 году", `"в" OR "2024" OR "год"*`},
		{"...", ""},
	} {
		if got := matchQuery(tc.in); got != tc.want {
			t.Errorf("matchQuery(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
