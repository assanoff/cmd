// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ardanlabs/kronk/sdk/kronk"
	kmodel "github.com/ardanlabs/kronk/sdk/kronk/model"
	"github.com/ardanlabs/kronk/sdk/tools/libs"
	"github.com/ardanlabs/kronk/sdk/tools/models"
)

// engine owns the loaded language model. One engine serves every job in a run,
// which is the reason batching lives in this command rather than in a shell
// loop: a loop would pay the model load per file, and map-reduce alone already
// makes many calls against one loaded model.
type engine struct {
	krn  *kronk.Kronk
	name string
}

// newEngine installs whatever is missing and loads the model. The SDK picks a
// llama.cpp bundle for this machine, downloads it and the model into ~/.kronk
// if they are not there yet, and reports progress through log.
func newEngine(ctx context.Context, name string, log kronk.Logger) (*engine, error) {
	lib, err := libs.New(libs.WithDetect(ctx, log))
	if err != nil {
		return nil, fmt.Errorf("%w: selecting llama.cpp libraries: %w", errNotSetup, err)
	}
	if _, err := lib.Download(ctx, log); err != nil {
		return nil, fmt.Errorf("%w: installing llama.cpp libraries: %w", errNotSetup, err)
	}
	if err := kronk.Init(kronk.WithLibPath(lib.LibsPath())); err != nil {
		return nil, fmt.Errorf("%w: initializing llama.cpp: %w", errNotSetup, err)
	}

	mdls, err := models.New()
	if err != nil {
		return nil, fmt.Errorf("opening the model store: %w", err)
	}

	mp, err := mdls.Download(ctx, log, name)
	if err != nil {
		return nil, fmt.Errorf("%w: model %q: %w", errNotSetup, name, err)
	}
	if len(mp.ModelFiles) == 0 {
		return nil, fmt.Errorf("%w: model %q has no files on disk", errNotSetup, name)
	}

	krn, err := kronk.New(
		kmodel.WithModelFiles(mp.ModelFiles),
		kmodel.WithAutoTune(true),
	)
	if err != nil {
		return nil, fmt.Errorf("loading model %q: %w", name, err)
	}

	return &engine{krn: krn, name: name}, nil
}

// close unloads the model. Failing to unload is worth reporting but never
// worth failing the run over: the summary is already written.
func (e *engine) close() {
	if err := e.krn.Unload(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "ai-sum: unloading %s: %v\n", e.name, err)
	}
}

// tokens counts the text the way this model will count it.
func (e *engine) tokens(ctx context.Context, text string) (int, error) {
	resp, err := e.krn.Tokenize(ctx, kmodel.D{"input": text})
	if err != nil {
		return 0, fmt.Errorf("tokenize: %w", err)
	}
	return resp.Tokens, nil
}

// chat runs one prompt over one piece of text and returns the answer, spending
// at most maxTokens on it.
func (e *engine) chat(ctx context.Context, instruction, text string, maxTokens int) (string, error) {
	// The instruction goes in as a system message and the text as the user
	// turn. Gluing the two into one user message reads as a single block to
	// the model, and a small one will answer by echoing the instruction back
	// before the result — which lands the prompt on standard output and
	// corrupts the pipeline. Separate roles keep the instruction out of the
	// answer.
	d := kmodel.D{
		"messages": []kmodel.D{
			kmodel.TextMessage(kmodel.RoleSystem, instruction),
			kmodel.TextMessage(kmodel.RoleUser, text),
		},
		"max_tokens": maxTokens,
	}
	if *flagTemp >= 0 {
		d["temperature"] = *flagTemp
	}
	// Reasoning triples the token spend on what is a mechanical compression
	// job, and none of it reaches the summary. Off unless asked for.
	d["enable_thinking"] = *flagThink

	resp, err := e.krn.Chat(ctx, d)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message == nil {
		return "", fmt.Errorf("model returned no answer")
	}
	if resp.Choices[0].FinishReason() == kmodel.FinishReasonError {
		return "", fmt.Errorf("model error: %s", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].FinishReason() == kmodel.FinishReasonLength && !*flagQuiet {
		fmt.Fprintln(os.Stderr, "ai-sum: a step hit -max-tokens and was cut short")
	}

	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

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
//	ai-sum -s brief,terms,chapters,facts,tips lecture.srt
//
// cost about as much as a single style, and it is why ai-notes asks for its
// five sections in one process instead of running ai-sum five times.
func (e *engine) summarize(ctx context.Context, text string, styles []string, lang string, limit int, material string) (string, error) {
	folded, err := e.fold(ctx, text, lang, limit)
	if err != nil {
		return "", err
	}
	// -no-reduce asked for the per-part compressions, not a summary.
	if *flagNoReduce {
		return folded, nil
	}

	material, err = e.trimContext(ctx, material)
	if err != nil {
		return "", err
	}

	var sections []string
	for _, style := range styles {
		stylePrompt, err := prompt(style, lang)
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
			e.report("summarizing")
		} else {
			e.report("section %s", style)
		}

		out, err := e.chat(ctx, stylePrompt, folded, *flagMaxTokens)
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
func (e *engine) fold(ctx context.Context, text string, lang string, limit int) (string, error) {
	mapPrompt, err := prompt("map", lang)
	if err != nil {
		return "", err
	}

	chunks, err := splitChunks(ctx, e, text, limit)
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
		for i, chunk := range chunks {
			e.report("round %d: part %d/%d", round, i+1, len(chunks))
			part, err := e.chat(ctx, mapPrompt, chunk, mapBudget(limit))
			if err != nil {
				return "", err
			}
			parts = append(parts, part)
		}

		joined := strings.Join(parts, "\n\n")
		if *flagNoReduce {
			return joined, nil
		}

		n, err := e.tokens(ctx, joined)
		if err != nil {
			return "", err
		}

		if done, why := foldDone(n, previous, limit, round); done {
			if why != "" && !*flagQuiet {
				fmt.Fprintf(os.Stderr, "ai-sum: %s at %d tokens; summarizing anyway\n", why, n)
			}
			return joined, nil
		}
		previous = n

		chunks, err = splitChunks(ctx, e, joined, limit)
		if err != nil {
			return "", err
		}
	}
}

// trimContext caps the supporting material at -ctx-tokens.
//
// The cap exists because -c is fed by a script that globs a directory, and one
// lecture folder can hold a 200-page PDF. Material that crowds out the
// transcript would make the summary worse, not better, so it is cut at a
// paragraph boundary and the loss is reported.
func (e *engine) trimContext(ctx context.Context, material string) (string, error) {
	if material == "" || *flagCtxTokens <= 0 {
		return "", nil
	}

	// Cut by bytes before counting tokens. Even at one byte per token this is
	// several times the cap, so nothing that would survive the cap is lost —
	// and the tokenizer is a cgo call into llama.cpp that does not enjoy being
	// handed a megabyte at once.
	if max := *flagCtxTokens * 8; len(material) > max {
		material = material[:max]
	}

	n, err := e.tokens(ctx, material)
	if err != nil {
		return "", err
	}
	if n <= *flagCtxTokens {
		return material, nil
	}

	parts, err := splitChunks(ctx, e, material, *flagCtxTokens)
	if err != nil {
		return "", err
	}
	if len(parts) == 0 {
		return "", nil
	}
	if !*flagQuiet {
		fmt.Fprintf(os.Stderr, "ai-sum: supporting material is %d tokens, using the first %d (-ctx-tokens)\n", n, *flagCtxTokens)
	}
	return parts[0], nil
}

// materialPreamble introduces the -c files to the model. It is written in the
// summary language for the same reason the section titles are: a model asked
// to answer in Russian follows Russian framing more consistently.
func materialPreamble(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "ru":
		return "Ниже — вспомогательные материалы к этому тексту (слайды, конспекты). " +
			"Используй их, чтобы правильно писать имена и термины и не упустить главное. " +
			"Не пересказывай их сами по себе: конспект делается по основному тексту."
	default:
		return "Below is supporting material for this text (slides, notes). " +
			"Use it to spell names and terms correctly and to avoid missing the point. " +
			"Do not summarize the material on its own: the summary is of the main text."
	}
}

// mapBudget caps one compression step.
//
// The map phase used to run on -max-tokens like everything else, and that is
// wrong in a way that only shows on a long document: with a 6000-token chunk
// and a 4096-token budget, a "compression" is allowed to come back nearly as
// long as its input. The fold then shrinks by a few per cent per round, spends
// its three rounds, and a forty-five minute lecture takes hours to summarize —
// most of it generating text that the next round throws away.
//
// A compression that may be half its input is a compression. Halving is also
// what makes the fold converge: two chunks in, one chunk out.
//
// -max-tokens still applies as a ceiling, so lowering it lowers this too.
func mapBudget(limit int) int {
	n := limit / 2
	if n > *flagMaxTokens {
		return *flagMaxTokens
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

// report writes progress to standard error, never to standard output: the
// summary on standard output has to stay clean for the next command in the
// pipeline.
func (e *engine) report(format string, args ...any) {
	if *flagQuiet {
		return
	}
	fmt.Fprintf(os.Stderr, "ai-sum: "+format+"\n", args...)
}
