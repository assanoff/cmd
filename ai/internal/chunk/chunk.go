// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package chunk packs text into pieces a model can take in one call.
//
// sum and tr both need this and both used to carry a copy. The copies were
// identical but for a comment, which is the usual way two copies start to
// differ.
package chunk

import (
	"context"
	"strings"
	"unicode"
)

// Tokenizer counts text the way the model that will read it counts text.
// *llm.Engine satisfies it.
type Tokenizer interface {
	Tokens(ctx context.Context, text string) (int, error)
}

// Split packs the text into pieces of at most limit tokens, breaking at
// paragraph boundaries so a piece is never cut mid-thought. Translating a
// document longer than the context window means translating it in pieces, and
// a piece that ends mid-sentence translates badly.
//
// Sizes come from the tokenizer rather than a bytes-per-token guess. That guess
// is what makes naive chunkers overflow on Cyrillic: UTF-8 spends two bytes per
// letter there, so a byte estimate can be off by a factor of two in either
// direction depending on the tokenizer's vocabulary.
func Split(ctx context.Context, tk Tokenizer, text string, limit int) ([]string, error) {
	paras := paragraphs(text)
	if len(paras) == 0 {
		return nil, nil
	}

	// One tokenize call per paragraph, then greedy packing. Tokenizing each
	// candidate window instead would be quadratic for no extra accuracy.
	sized := make([]sizedPart, 0, len(paras))
	for _, p := range paras {
		n, err := tk.Tokens(ctx, p)
		if err != nil {
			return nil, err
		}
		if n <= limit {
			sized = append(sized, sizedPart{text: p, tokens: n})
			continue
		}
		// A single paragraph over the limit has to be broken further.
		split, err := splitOversized(ctx, tk, p, limit)
		if err != nil {
			return nil, err
		}
		sized = append(sized, split...)
	}

	return pack(sized, limit), nil
}

type sizedPart struct {
	text   string
	tokens int
}

// pack fills chunks greedily, never exceeding the limit and never merging
// across a piece that is already at the limit on its own.
func pack(parts []sizedPart, limit int) []string {
	var (
		chunks  []string
		current []string
		running int
	)

	flush := func() {
		if len(current) > 0 {
			chunks = append(chunks, strings.Join(current, "\n\n"))
			current = nil
			running = 0
		}
	}

	for _, p := range parts {
		if running > 0 && running+p.tokens > limit {
			flush()
		}
		current = append(current, p.text)
		running += p.tokens
	}
	flush()

	return chunks
}

// splitOversized breaks one long paragraph down: sentences first, then words
// if a single sentence is still too big. Word splitting is a last resort and
// only happens on text with no sentence punctuation at all, such as an
// unbroken transcript.
func splitOversized(ctx context.Context, tk Tokenizer, para string, limit int) ([]sizedPart, error) {
	units := sentences(para)
	if len(units) <= 1 {
		units = strings.Fields(para)
	}

	var (
		out     []sizedPart
		current []string
		running int
	)

	flush := func() {
		if len(current) > 0 {
			out = append(out, sizedPart{text: strings.Join(current, " "), tokens: running})
			current = nil
			running = 0
		}
	}

	for _, u := range units {
		n, err := tk.Tokens(ctx, u)
		if err != nil {
			return nil, err
		}
		if running > 0 && running+n > limit {
			flush()
		}
		current = append(current, u)
		running += n
	}
	flush()

	return out, nil
}

// paragraphs splits on blank lines and drops empty results.
//
// Runs of more than one blank line need no collapsing first: a longer run
// only yields blocks that are empty or whitespace, and those are dropped
// here anyway.
func paragraphs(text string) []string {
	var out []string
	for block := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		if block = strings.TrimSpace(block); block != "" {
			out = append(out, block)
		}
	}
	return out
}

// sentences splits on terminal punctuation. It is deliberately simple: the
// only job here is to find a break point that reads better than a word
// boundary, not to be linguistically correct.
func sentences(para string) []string {
	var (
		out []string
		sb  strings.Builder
	)

	runes := []rune(para)
	for i, r := range runes {
		sb.WriteRune(r)

		if r != '.' && r != '!' && r != '?' && r != '…' {
			continue
		}
		// A terminator only ends a sentence when whitespace follows, which
		// keeps "1.5" and "ai hear -f json." intact.
		if i+1 < len(runes) && !unicode.IsSpace(runes[i+1]) {
			continue
		}
		if s := strings.TrimSpace(sb.String()); s != "" {
			out = append(out, s)
		}
		sb.Reset()
	}

	if s := strings.TrimSpace(sb.String()); s != "" {
		out = append(out, s)
	}
	return out
}
