// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package tr

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/assanoff/cmd/ai/internal/chunk"
	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/llm"
	"github.com/assanoff/cmd/ai/internal/prompt"
	"github.com/assanoff/cmd/ai/internal/subs"
)

// Prompts live in files, not in string literals: they are edited far more
// often than the code around them, and a diff of a prompt should read like a
// diff of prose.
//
//go:embed prompts/*.md
var promptFS embed.FS

var prompts, _ = fs.Sub(promptFS, "prompts")

// translator carries what every step needs, so the methods below read as the
// algorithm rather than as a parameter list.
type translator struct {
	cmd *Command
	eng *llm.Engine
	p   *cli.Printer
	to  string
}

// instruction renders a prompt. From is passed through because naming the
// source language is worth doing when it is known: a model told the source is
// Kazakh stops guessing at Turkish. Where that sentence goes, and whether it
// appears at all, is the prompt file's business rather than this function's.
func (t translator) instruction(name string) (string, error) {
	return prompt.Render(prompts, name, struct{ Lang, From string }{
		Lang: prompt.Name(t.to),
		From: prompt.Name(t.cmd.From),
	})
}

// chat runs one prompt over one piece of text.
func (t translator) chat(ctx context.Context, instruction, text string) (string, error) {
	// Reasoning triples the token spend on what is a mechanical job, and none
	// of it reaches the translation. Off unless asked for, which is why this
	// is an explicit false rather than nil.
	think := t.cmd.Think

	return t.eng.Chat(ctx, llm.Request{
		System:      instruction,
		User:        text,
		Temperature: t.cmd.Temp,
		MaxTokens:   t.cmd.MaxTokens,
		Thinking:    &think,
	})
}

// text renders a whole document. Anything longer than one call is translated
// piece by piece and the pieces are joined back together — unlike a summary
// there is nothing to fold, because the output is as long as the input.
func (t translator) text(ctx context.Context, body string, limit int) (string, error) {
	instruction, err := t.instruction("text")
	if err != nil {
		return "", err
	}

	chunks, err := chunk.Split(ctx, t.eng, body, limit)
	if err != nil {
		return "", err
	}
	if len(chunks) == 0 {
		return "", fmt.Errorf("nothing to translate")
	}

	parts := make([]string, 0, len(chunks))
	for i, c := range chunks {
		if len(chunks) > 1 {
			t.p.Printf("part %d/%d", i+1, len(chunks))
		}
		part, err := t.chat(ctx, instruction, c)
		if err != nil {
			return "", err
		}
		parts = append(parts, part)
	}

	return strings.Join(parts, "\n\n"), nil
}

// cueBatch is how many cues go into one call. Large enough that a 500-cue
// subtitle file is not 500 round trips, small enough that a mismatch costs
// little to redo.
//
// Twenty survived a measurement against ten. The model loses a number often
// enough that four batches of twenty out of seven came back misnumbered, which
// argues for a smaller batch — but ten fails too, about a quarter of the time,
// and halving a failure already recovers most of what a smaller batch would
// save. Over the same 130 cues: twenty with splitting took about 23 calls, ten
// would take about 22, and twenty with the old retry took 87. The first two
// are the same number; the batch size is not where the win was.
const cueBatch = 20

// cues renders subtitle text while leaving every timing untouched.
//
// Cues go out numbered and must come back numbered. A model that drops, merges
// or invents an item would silently shift every subsequent subtitle against
// its timing, so a batch whose numbering does not come back intact is split
// and redone rather than trusted.
func (t translator) cues(ctx context.Context, cues []subs.Cue) error {
	instruction, err := t.instruction("subtitles")
	if err != nil {
		return err
	}

	for start := 0; start < len(cues); start += cueBatch {
		end := min(start+cueBatch, len(cues))
		t.p.Printf("cues %d-%d of %d", start+1, end, len(cues))

		if err := t.span(ctx, instruction, cues[start:end]); err != nil {
			return err
		}
	}

	return nil
}

// span translates one run of cues, halving it whenever the numbering does not
// survive the round trip, until a half comes back intact or one cue is left.
//
// Going straight from a failed batch to one call per cue was the first version
// and it was far too harsh. On a real subtitle file 12 batches of 20 fail the
// check, and which ones differ from run to run -- the model simply drops a
// number now and then, so the failures are spread rather than tied to any
// particular stretch. Retrying each failure cue by cue turned 21 calls into
// roughly 250. Halving costs three calls where the old path cost twenty-one,
// and a half that comes back numbered correctly is just as safe: the guard
// that rejected the batch is the same one that accepts the half.
func (t translator) span(ctx context.Context, instruction string, batch []subs.Cue) error {
	got, err := t.batch(ctx, instruction, batch)
	if err != nil {
		return err
	}
	if got != nil {
		for i := range batch {
			batch[i].Text = got[i]
		}
		return nil
	}

	// One call for one cue cannot misalign, so this is where splitting stops.
	if len(batch) == 1 {
		if strings.TrimSpace(batch[0].Text) == "" {
			return nil
		}
		out, err := t.chat(ctx, instruction, "1. "+batch[0].Text)
		if err != nil {
			return err
		}
		batch[0].Text = stripNumber(out)
		return nil
	}

	half := len(batch) / 2
	t.p.Printf("numbering came back wrong; splitting %d cues into %d and %d", len(batch), half, len(batch)-half)
	if err := t.span(ctx, instruction, batch[:half]); err != nil {
		return err
	}
	return t.span(ctx, instruction, batch[half:])
}

// batch returns one translation per cue, or nil when the numbering did not
// survive the round trip.
func (t translator) batch(ctx context.Context, instruction string, batch []subs.Cue) ([]string, error) {
	var sb strings.Builder
	for i, c := range batch {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, strings.TrimSpace(c.Text))
	}

	out, err := t.chat(ctx, instruction, sb.String())
	if err != nil {
		return nil, err
	}

	got := make([]string, len(batch))
	seen := 0
	for line := range strings.SplitSeq(out, "\n") {
		n, text, ok := splitNumber(line)
		if !ok || n < 1 || n > len(batch) || got[n-1] != "" {
			continue
		}
		got[n-1] = text
		seen++
	}
	if seen != len(batch) {
		return nil, nil
	}
	return got, nil
}

// splitNumber parses a "12. text" line.
func splitNumber(line string) (n int, text string, ok bool) {
	line = strings.TrimSpace(line)
	i := strings.IndexAny(line, ".)")
	if i <= 0 {
		return 0, "", false
	}
	n, err := strconv.Atoi(line[:i])
	if err != nil {
		return 0, "", false
	}
	text = strings.TrimSpace(line[i+1:])
	if text == "" {
		return 0, "", false
	}
	return n, text, true
}

// stripNumber removes the "1. " a single-cue retry asked the model to echo.
func stripNumber(line string) string {
	if _, text, ok := splitNumber(line); ok {
		return text
	}
	return strings.TrimSpace(line)
}
