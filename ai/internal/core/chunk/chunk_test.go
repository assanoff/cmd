// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package chunk

import (
	"context"
	"strings"
	"testing"
)

// words counts a token per whitespace-separated word. The real tokenizer is a
// cgo call into llama.cpp and a loaded model; this is the whole reason Split
// takes an interface, and the reason it can be tested at all now that it is
// not wired directly to a global engine.
type words struct{ calls int }

func (w *words) Tokens(_ context.Context, text string) (int, error) {
	w.calls++
	return len(strings.Fields(text)), nil
}

func para(n, size int) string {
	body := strings.TrimSpace(strings.Repeat("word ", size))
	parts := make([]string, n)
	for i := range parts {
		parts[i] = body
	}
	return strings.Join(parts, "\n\n")
}

// Text that already fits comes back as one piece, untouched. That is what lets
// a short document reach the style prompt exactly as it was written.
func TestShortTextIsOneChunk(t *testing.T) {
	got, err := Split(context.Background(), &words{}, "one two three", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "one two three" {
		t.Errorf("got %q, want the text unchanged in one chunk", got)
	}
}

func TestEmptyTextYieldsNothing(t *testing.T) {
	got, err := Split(context.Background(), &words{}, "   \n\n  \n", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %q, want no chunks", got)
	}
}

// No chunk may exceed the limit, or the call it is packed into overflows the
// context window.
func TestNoChunkExceedsTheLimit(t *testing.T) {
	const limit = 30
	text := para(10, 12)

	got, err := Split(context.Background(), &words{}, text, limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("got %d chunks, want several", len(got))
	}
	for i, c := range got {
		if n := len(strings.Fields(c)); n > limit {
			t.Errorf("chunk %d is %d tokens, over the limit of %d", i, n, limit)
		}
	}
}

// Packing is greedy: pieces that fit together travel together, so a chunk is
// not needlessly small.
func TestPackingIsGreedy(t *testing.T) {
	// Six paragraphs of 10 tokens, limit 30: three should fit per chunk.
	got, err := Split(context.Background(), &words{}, para(6, 10), 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2", len(got))
	}
}

// Breaking at a blank line is what keeps a [HH:MM:SS] marker glued to the text
// it introduces, which is the only reason the times survive a fold.
func TestChunksBreakOnParagraphs(t *testing.T) {
	text := "[00:00:01] first paragraph here\n\n[00:01:00] second paragraph here"

	got, err := Split(context.Background(), &words{}, text, 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		if strings.Count(c, "[") != strings.Count(c, "]") {
			t.Errorf("a chunk split a marker: %q", c)
		}
		if i := strings.Index(c, "["); i > 0 {
			t.Errorf("a marker is no longer at the start of its text: %q", c)
		}
	}
}

// A paragraph bigger than the limit on its own has to be broken further, on
// sentences before words.
func TestOversizedParagraphBreaksOnSentences(t *testing.T) {
	text := "One two three. Four five six. Seven eight nine."

	got, err := Split(context.Background(), &words{}, text, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d chunks, want 3 (one per sentence)", len(got))
	}
	for _, c := range got {
		if !strings.HasSuffix(c, ".") {
			t.Errorf("chunk %q does not end at a sentence boundary", c)
		}
	}
}

// Text with no sentence punctuation at all — an unbroken transcript — still
// has to be cut somewhere, and words are the last resort.
func TestOversizedParagraphFallsBackToWords(t *testing.T) {
	text := strings.TrimSpace(strings.Repeat("word ", 20))

	got, err := Split(context.Background(), &words{}, text, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 4 {
		t.Fatalf("got %d chunks, want at least 4", len(got))
	}
	for _, c := range got {
		if n := len(strings.Fields(c)); n > 5 {
			t.Errorf("chunk of %d tokens is over the limit", n)
		}
	}
}

// A terminator only ends a sentence when whitespace follows, or "1.5" and
// "ai hear -f json." get cut in half.
func TestSentencesKeepDecimalsIntact(t *testing.T) {
	got := sentences("Version 1.5 shipped. Next one soon.")
	if len(got) != 2 {
		t.Fatalf("got %d sentences, want 2: %q", len(got), got)
	}
	if !strings.Contains(got[0], "1.5") {
		t.Errorf("split a decimal: %q", got[0])
	}
}

// Runs of blank lines must not become empty chunks.
func TestBlankLineRunsCollapse(t *testing.T) {
	got := paragraphs("one\n\n\n\n\ntwo\r\n\r\nthree")
	if len(got) != 3 {
		t.Fatalf("got %d paragraphs, want 3: %q", len(got), got)
	}
	for _, p := range got {
		if strings.TrimSpace(p) == "" {
			t.Error("produced an empty paragraph")
		}
	}
}

// Tokenizing each candidate window instead of each paragraph would be
// quadratic, and every call is a trip into llama.cpp.
func TestTokenizerIsCalledPerParagraphNotPerWindow(t *testing.T) {
	w := &words{}
	if _, err := Split(context.Background(), w, para(8, 5), 20); err != nil {
		t.Fatal(err)
	}
	if w.calls > 8 {
		t.Errorf("tokenized %d times for 8 paragraphs; packing should not re-measure", w.calls)
	}
}

// Nothing is lost between input and output: a fold that silently dropped a
// paragraph would read as a model that ignored it.
func TestNoWordsAreLost(t *testing.T) {
	text := para(7, 9)

	got, err := Split(context.Background(), &words{}, text, 20)
	if err != nil {
		t.Fatal(err)
	}

	var n int
	for _, c := range got {
		n += len(strings.Fields(c))
	}
	if want := len(strings.Fields(text)); n != want {
		t.Errorf("chunks hold %d words, input had %d", n, want)
	}
}
