// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package llm

import (
	"context"
	"fmt"
	"strings"

	kmodel "github.com/ardanlabs/kronk/sdk/kronk/model"
)

// EmbedDocs returns one vector per passage, in order. The SDK normalizes each
// to unit length, so a cosine distance between two of them is all a search
// has to compute.
func (e *Engine) EmbedDocs(ctx context.Context, texts []string) ([][]float32, error) {
	_, doc := prefixes(e.name)
	in := make([]string, len(texts))
	for i, t := range texts {
		in[i] = doc + t
	}
	return e.embed(ctx, in)
}

// EmbedQuery returns the vector of a search query.
//
// A query and a passage are embedded differently on purpose. The models this
// is used with are asymmetric: trained to put "talked about taxes" near the
// minute of speech that answers it, provided the query says it is a query.
// Without the prefix the query lands among passages that look like it — other
// short questions — rather than among those that answer it.
func (e *Engine) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	query, _ := prefixes(e.name)
	vecs, err := e.embed(ctx, []string{query + q})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

func (e *Engine) embed(ctx context.Context, in []string) ([][]float32, error) {
	// truncate keeps one overlong passage from failing a whole file: the head
	// of a long monologue still says what it is about.
	resp, err := e.krn.Embeddings(ctx, kmodel.D{"input": in, "truncate": true})
	if err != nil {
		return nil, fmt.Errorf("embedding with %s: %w", e.name, err)
	}
	if len(resp.Data) != len(in) {
		return nil, fmt.Errorf("embedding with %s: %d vectors for %d inputs", e.name, len(resp.Data), len(in))
	}

	out := make([][]float32, len(in))
	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, fmt.Errorf("embedding with %s: vector for input %d of %d", e.name, d.Index, len(in))
		}
		out[d.Index] = d.Embedding
	}
	return out, nil
}

// prefixes are what an embedding model was trained to see before a query and
// before a passage. A model not named here gets none, which is right for the
// symmetric ones and merely a little worse for any other.
func prefixes(model string) (query, doc string) {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "qwen3-embedding"):
		// Qwen recommends the instruction in English whatever the language
		// of the text, since that is what it was trained with.
		return "Instruct: Given a search query, retrieve passages of a spoken transcript that answer it\nQuery: ", ""
	case strings.Contains(m, "nomic-embed"):
		return "search_query: ", "search_document: "
	}
	return "", ""
}
