// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package index

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Hit is one place a query was found: the passage, and the moment in it.
type Hit struct {
	MediaID    int64
	Path       string
	Title      string
	Start, End time.Duration // the passage
	Text       string        // the passage
	Score      float64       // reciprocal rank fusion; only the order means anything

	At    time.Duration // where in the passage the answer is said
	Quote string        // what is said there

	// Similarity is the cosine similarity of the query and what is said at
	// At, from -1 to 1; 0 when there was no vector to compare.
	Similarity float64
}

// candidates is how many chunks each half of a search brings to the merge.
// Fusion can only reorder what the halves found, so this has to be well above
// the number of results asked for.
const candidates = 50

// rrfK is the constant of reciprocal rank fusion: each list scores a chunk
// 1/(rrfK+rank). 60 is the value of the paper that introduced it, and it keeps
// the top of one list from drowning a chunk both lists rank well.
const rrfK = 60

// Search finds the passages nearest to vec and the passages that share words
// with query, merges the two lists by rank, and returns the best limit of
// them, each pinned to its moment. The ranks are merged rather than the scores
// because a cosine distance and a bm25 score are on unrelated scales.
//
// Either half may be empty: a nil vec searches the words alone, and a query
// with no words in it searches the vector alone.
func (x *Index) Search(ctx context.Context, vec []float32, query string, limit int) ([]Hit, error) {
	n := max(candidates, 4*limit)
	score := map[int64]float64{}
	add := func(ids []int64) {
		for rank, id := range ids {
			score[id] += 1 / float64(rrfK+rank+1)
		}
	}

	if len(vec) > 0 {
		ids, err := x.nearest(ctx, vec, n)
		if err != nil {
			return nil, err
		}
		add(ids)
	}
	if q := matchQuery(query); q != "" {
		ids, err := x.words(ctx, q, n)
		if err != nil {
			return nil, err
		}
		add(ids)
	}
	if len(score) == 0 {
		return nil, nil
	}

	hits, err := x.hits(ctx, score)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(hits, func(a, b Hit) int {
		return cmp.Or(
			cmp.Compare(b.Score, a.Score),
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Start, b.Start),
		)
	})
	// Every candidate is pinpointed before any is dropped: how near the query
	// a hit is, which decides whether it is shown, is known only then.
	hits = distinct(hits, len(hits))
	if err := x.pinpoint(ctx, vec, hits); err != nil {
		return nil, err
	}
	return relevant(hits, limit), nil
}

// closeEnough is how near the best hit's similarity another hit has to come
// to be shown at all, as a fraction of it.
//
// The nearest passages are always some distance away, so without a cut a
// search for a place mentioned once lists that place first and then, below
// it, every other file there is. The cut is relative because no absolute one
// works: a one-word query is far from everything, so "Домбай" scores 0.29
// against the only passage that mentions it, while a query about nothing in
// the index scores 0.28 against its nearest miss. Measured on that index, the
// misses below a real answer came to 0.5–0.84 of its similarity, with one
// exception at 0.87; the answers themselves were alone at the top.
const closeEnough = 0.85

// relevant keeps the first limit hits that come within closeEnough of the
// best one. Hits that were never compared with a vector — a search by words
// alone, or an index made before pieces — all have zero and are all kept.
func relevant(hits []Hit, limit int) []Hit {
	var best float64
	for _, h := range hits {
		best = max(best, h.Similarity)
	}
	var out []Hit
	for _, h := range hits {
		if len(out) == limit {
			break
		}
		if h.Similarity >= closeEnough*best {
			out = append(out, h)
		}
	}
	return out
}

// pinpoint moves each hit from the start of its passage to the piece of it
// nearest vec.
//
// A passage is a minute of speech, and its start is up to a minute early: the
// remark about taxes it was found for may be its last sentence. The pieces are
// ten seconds or so, embedded when the file was indexed, so finding the
// nearest is one query a hit and no call to the model. Embedding them here
// instead, per search, cost half a second for three hits on a laptop GPU.
//
// A hit with no pieces to compare — found by its words alone, or in an index
// made before pieces — keeps the start and the text of its passage.
func (x *Index) pinpoint(ctx context.Context, vec []float32, hits []Hit) error {
	for i := range hits {
		hits[i].At, hits[i].Quote = hits[i].Start, hits[i].Text
	}
	if len(vec) == 0 {
		return nil
	}
	if ok, err := exists(ctx, x.db, "pieces_vec"); err != nil || !ok {
		return err
	}

	q := blob(vec)
	for i, h := range hits {
		var at int64
		var quote string
		var dist float64
		err := x.db.QueryRowContext(ctx, `
			select p.start_ms, p.text, vec_distance_cosine(v.embedding, ?) as d
			from pieces p join pieces_vec v on v.piece_id = p.id
			where p.media_id = ? and p.start_ms >= ? and p.start_ms < ?
			order by d limit 1`,
			q, h.MediaID, h.Start.Milliseconds(), h.End.Milliseconds()).Scan(&at, &quote, &dist)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		hits[i].At, hits[i].Quote, hits[i].Similarity = ms(at), quote, 1-dist
	}
	return nil
}

// nearest returns the ids of the n chunks closest to vec, closest first.
func (x *Index) nearest(ctx context.Context, vec []float32, n int) ([]int64, error) {
	_, dims, err := x.Model(ctx)
	if err != nil || dims == 0 {
		return nil, err // nothing indexed yet
	}
	return x.ids(ctx, `select chunk_id from chunks_vec where embedding match ? and k = ? order by distance`,
		blob(vec), n)
}

// words returns the ids of the n chunks that best match an FTS5 query.
func (x *Index) words(ctx context.Context, q string, n int) ([]int64, error) {
	return x.ids(ctx, `select rowid from chunks_fts where chunks_fts match ? order by rank limit ?`, q, n)
}

func (x *Index) ids(ctx context.Context, query string, args ...any) ([]int64, error) {
	rows, err := x.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// hits loads the chunks named by the keys of score, each with its score.
func (x *Index) hits(ctx context.Context, score map[int64]float64) ([]Hit, error) {
	args := make([]any, 0, len(score))
	for id := range score {
		args = append(args, id)
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")

	rows, err := x.db.QueryContext(ctx, `
		select c.id, m.id, m.path, m.title, c.start_ms, c.end_ms, c.text
		from chunks c join media m on m.id = c.media_id
		where c.id in (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	hits := make([]Hit, 0, len(args))
	for rows.Next() {
		var id, start, end int64
		var h Hit
		if err := rows.Scan(&id, &h.MediaID, &h.Path, &h.Title, &start, &end, &h.Text); err != nil {
			return nil, err
		}
		h.Start, h.End, h.Score = ms(start), ms(end), score[id]
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// distinct keeps the first limit hits that do not overlap a better hit in the
// same file. Windows overlap by design, so the minute that answers a query
// usually wins twice — once in each window that holds it — and the second
// copy would only push a different answer off the list.
func distinct(hits []Hit, limit int) []Hit {
	var out []Hit
	for _, h := range hits {
		if len(out) == limit {
			break
		}
		if !slices.ContainsFunc(out, func(o Hit) bool {
			return o.MediaID == h.MediaID && o.Start < h.End && h.Start < o.End
		}) {
			out = append(out, h)
		}
	}
	return out
}

// matchQuery turns free text into an FTS5 query that matches any of its
// words.
//
// Free text cannot go to MATCH as it is: a quote, a dash or a word like NOT is
// FTS5 syntax. Each word is quoted instead, and the words are joined with OR so
// that a chunk sharing some of them is still found; bm25 ranks one sharing
// more of them, and the rarer ones, higher.
//
// FTS5 has no Russian stemmer, and Russian inflects: "налоги" is "налогов" and
// "налогами" in the next sentence, "Москва" is "в Москве". So a word loses what
// is likely its ending and matches as a prefix; see term. It is crude, and it is
// enough: the vector half of the search is what understands meaning, this half
// only has to catch names and terms.
func matchQuery(q string) string {
	words := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})

	var terms []string
	for _, w := range words {
		t := term(w)
		if !slices.Contains(terms, t) {
			terms = append(terms, t)
		}
	}
	return strings.Join(terms, " OR ")
}

// term is one word of a query as FTS5 should match it. A word of three
// letters or fewer, or a number, matches whole. A word of four or five loses
// its last two letters, a longer one its last three but keeps four: Russian
// endings run to three letters ("налог-ами"), and a stem cut shorter than four
// matches half the vocabulary.
func term(w string) string {
	r := []rune(w)
	if len(r) < 4 || strings.IndexFunc(w, unicode.IsLetter) < 0 {
		return `"` + w + `"`
	}
	n := max(3, len(r)-2)
	if len(r) >= 6 {
		n = max(4, len(r)-3)
	}
	return `"` + string(r[:n]) + `"*`
}
