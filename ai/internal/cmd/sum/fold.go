// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package sum

import (
	"context"
	"fmt"
	"strings"

	"github.com/assanoff/cmd/ai/internal/chunk"
	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/llm"
)

// maxReduceRounds is the hard stop on the fold.
//
// minShrink is the soft one, and it matters more. A round that fails to shrink
// the material by at least this much is a round that has run out of things to
// cut: the map prompt keeps every fact, so once the text is all facts there is
// nothing left to compress and further rounds only burn minutes to produce the
// same tokens. Measured on a real document with a small chunk size, rounds 2
// and 3 both returned the same size — that is what this guard stops.
const (
	maxReduceRounds = 3
	minShrink       = 0.8
)

// folder carries what every step of a summary needs, so the methods below read
// as the algorithm rather than as a parameter list.
type folder struct {
	cmd *Command
	eng *llm.Engine
	p   *cli.Printer
}

// summarize turns a document of any length into one summary, or into one
// document with a section per style.
//
// Text that fits in a chunk goes straight to the style prompt. Anything longer
// is compressed piece by piece (the map phase) and the pieces are then folded
// together (the reduce phase), repeating until what is left fits in a single
// call.
//
// The fold happens once no matter how many styles were asked for. It is the
// expensive half by a wide margin — many calls over the whole document, where
// a style pass is one call over what the fold left — and every style wants the
// same folded material anyway. That is what makes
//
//	ai sum -s brief,terms,chapters,facts,tips lecture.srt
//
// cost about as much as a single style.
//
// material arrives already capped at --ctx-tokens: it is the same for every
// input in a batch, so it is trimmed once before the run rather than here.
func (f folder) summarize(ctx context.Context, text string, styles []string, lang string, limit int, material string) (string, error) {
	folded, err := f.fold(ctx, text, lang, limit)
	if err != nil {
		return "", err
	}
	// --no-reduce asked for the per-part compressions, not a summary.
	if f.cmd.NoReduce {
		return folded, nil
	}

	var sections []string
	for _, style := range styles {
		stylePrompt, err := render(style, lang)
		if err != nil {
			return "", err
		}
		// Supporting material rides with the style prompt and never with the
		// map prompt. Compression has to be faithful to what was actually
		// said; the slides only help at the point where the summary is being
		// worded, where a name spelled correctly on a slide beats the same
		// name misheard by the transcriber.
		if material != "" {
			stylePrompt += "\n\n" + materialPreamble(lang) + "\n\n" + material
		}

		if len(styles) == 1 {
			f.p.Printf("summarizing")
		} else {
			f.p.Printf("section %s", style)
		}

		out, err := f.chat(ctx, stylePrompt, folded, f.cmd.MaxTokens)
		if err != nil {
			return "", err
		}
		if out = strings.TrimSpace(out); out == "" {
			continue
		}

		// A lone style is the pipeline case and stays exactly as it was: no
		// heading, so `| head -1` still reads the summary.
		if len(styles) == 1 {
			return out, nil
		}
		sections = append(sections, "## "+sectionTitle(style, lang)+"\n\n"+out)
	}

	if len(sections) == 0 {
		return "", fmt.Errorf("the model produced no sections")
	}
	return strings.Join(sections, "\n\n"), nil
}

// fold reduces the document until it fits in one call, and returns what is
// left. Input that already fits comes back untouched, which is what lets a
// short text reach the style prompt exactly as written.
func (f folder) fold(ctx context.Context, text string, lang string, limit int) (string, error) {
	mapPrompt, err := render("map", lang)
	if err != nil {
		return "", err
	}

	chunks, err := chunk.Split(ctx, f.eng, text, limit)
	if err != nil {
		return "", err
	}
	if len(chunks) == 0 {
		return "", fmt.Errorf("nothing to summarize")
	}
	if len(chunks) == 1 {
		return chunks[0], nil
	}

	var previous int

	for round := 1; ; round++ {
		parts := make([]string, 0, len(chunks))
		for i, c := range chunks {
			f.p.Printf("round %d: part %d/%d", round, i+1, len(chunks))
			part, err := f.chat(ctx, mapPrompt, c, f.mapBudget(limit))
			if err != nil {
				return "", err
			}
			parts = append(parts, part)
		}

		joined := strings.Join(parts, "\n\n")
		if f.cmd.NoReduce {
			return joined, nil
		}

		n, err := f.eng.Tokens(ctx, joined)
		if err != nil {
			return "", err
		}

		if done, why := foldDone(n, previous, limit, round); done {
			if why != "" {
				f.p.Printf("%s at %d tokens; summarizing anyway", why, n)
			}
			return joined, nil
		}
		previous = n

		chunks, err = chunk.Split(ctx, f.eng, joined, limit)
		if err != nil {
			return "", err
		}
	}
}

// chat runs one prompt over one piece of text.
func (f folder) chat(ctx context.Context, instruction, text string, maxTokens int) (string, error) {
	// Reasoning triples the token spend on what is a mechanical compression
	// job, and none of it reaches the summary. Off unless asked for, which is
	// why this is an explicit false rather than nil.
	think := f.cmd.Think

	return f.eng.Chat(ctx, llm.Request{
		System:      instruction,
		User:        text,
		Temperature: f.cmd.Temp,
		MaxTokens:   maxTokens,
		Thinking:    &think,
	})
}

// trimContext caps the supporting material at --ctx-tokens.
//
// The cap exists because -c is fed by a script that globs a directory, and one
// lecture folder can hold a 200-page PDF. Material that crowds out the
// transcript would make the summary worse, not better, so it is cut at a
// paragraph boundary and the loss is reported.
func (f folder) trimContext(ctx context.Context, material string) (string, error) {
	if material == "" || f.cmd.CtxTokens <= 0 {
		return "", nil
	}

	// Cut by bytes before counting tokens. Even at one byte per token this is
	// several times the cap, so nothing that would survive the cap is lost —
	// and the tokenizer is a cgo call into llama.cpp that does not enjoy being
	// handed a megabyte at once.
	if maxBytes := f.cmd.CtxTokens * 8; len(material) > maxBytes {
		material = material[:maxBytes]
	}

	n, err := f.eng.Tokens(ctx, material)
	if err != nil {
		return "", err
	}
	if n <= f.cmd.CtxTokens {
		return material, nil
	}

	parts, err := chunk.Split(ctx, f.eng, material, f.cmd.CtxTokens)
	if err != nil {
		return "", err
	}
	if len(parts) == 0 {
		return "", nil
	}
	f.p.Printf("supporting material is %d tokens, using the first %d (--ctx-tokens)", n, f.cmd.CtxTokens)
	return parts[0], nil
}

// mapBudget caps one compression step.
//
// The map phase used to run on --max-tokens like everything else, and that is
// wrong in a way that only shows on a long document: with a 6000-token chunk
// and a 4096-token budget, a "compression" is allowed to come back nearly as
// long as its input. The fold then shrinks by a few per cent per round, spends
// its three rounds, and a forty-five minute lecture takes hours to summarize —
// most of it generating text that the next round throws away.
//
// A compression that may be half its input is a compression. Halving is also
// what makes the fold converge: two chunks in, one chunk out.
//
// --max-tokens still applies as a ceiling, so lowering it lowers this too.
func (f folder) mapBudget(limit int) int {
	n := limit / 2
	if n > f.cmd.MaxTokens {
		return f.cmd.MaxTokens
	}
	if n < 256 {
		return 256
	}
	return n
}

// foldDone decides whether to stop folding. It reports why when the fold is
// ending early, so a summary assembled from material that never got small
// enough says so rather than looking like a clean result.
func foldDone(n, previous, limit, round int) (done bool, why string) {
	switch {
	case n <= limit:
		return true, ""
	case round >= maxReduceRounds:
		return true, fmt.Sprintf("stopping after %d rounds", round)
	case previous > 0 && float64(n) > float64(previous)*minShrink:
		return true, "the fold stopped shrinking"
	default:
		return false, ""
	}
}
