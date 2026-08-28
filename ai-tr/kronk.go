// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ardanlabs/kronk/sdk/kronk"
	kmodel "github.com/ardanlabs/kronk/sdk/kronk/model"
	"github.com/ardanlabs/kronk/sdk/tools/libs"
	"github.com/ardanlabs/kronk/sdk/tools/models"
)

// engine owns the loaded language model. One engine serves every job in a run,
// which is the reason batching lives in this command rather than in a shell
// loop: a loop would pay the model load per file, and a long document already
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
// worth failing the run over: the translation is already written.
func (e *engine) close() {
	if err := e.krn.Unload(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "ai-tr: unloading %s: %v\n", e.name, err)
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

// chat runs one prompt over one piece of text and returns the answer.
func (e *engine) chat(ctx context.Context, instruction, text string) (string, error) {
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
		"max_tokens": *flagMaxTokens,
	}
	if *flagTemp >= 0 {
		d["temperature"] = *flagTemp
	}
	// Reasoning triples the token spend on what is a mechanical job, and none
	// of it reaches the translation. Off unless asked for.
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
		fmt.Fprintln(os.Stderr, "ai-tr: a step hit -max-tokens and was cut short")
	}

	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

// translate renders a whole document. Anything longer than one call is
// translated piece by piece and the pieces are joined back together — unlike a
// summary there is nothing to fold, because the output is as long as the input.
func (e *engine) translate(ctx context.Context, text string, to string, limit int) (string, error) {
	instruction, err := prompt("text", to)
	if err != nil {
		return "", err
	}
	if *flagFrom != "" {
		instruction += "\n\nThe source language is " + languageName(*flagFrom) + "."
	}

	chunks, err := splitChunks(ctx, e, text, limit)
	if err != nil {
		return "", err
	}
	if len(chunks) == 0 {
		return "", fmt.Errorf("nothing to translate")
	}

	parts := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		if len(chunks) > 1 {
			e.report("part %d/%d", i+1, len(chunks))
		}
		part, err := e.chat(ctx, instruction, chunk)
		if err != nil {
			return "", err
		}
		parts = append(parts, part)
	}

	return strings.Join(parts, "\n\n"), nil
}

// cueBatch is how many cues go into one call. Large enough that a 500-cue
// subtitle file is not 500 round trips, small enough that the model keeps the
// numbering straight and a mismatch costs little to redo.
const cueBatch = 20

// translateCues renders subtitle text while leaving every timing untouched.
//
// Cues go out numbered and must come back numbered. A model that drops, merges
// or invents an item would silently shift every subsequent subtitle against
// its timing, so a batch whose numbering does not come back intact is redone
// one cue at a time rather than trusted.
func (e *engine) translateCues(ctx context.Context, cues []cue, to string) error {
	instruction, err := prompt("subtitles", to)
	if err != nil {
		return err
	}
	if *flagFrom != "" {
		instruction += "\n\nThe source language is " + languageName(*flagFrom) + "."
	}

	for start := 0; start < len(cues); start += cueBatch {
		end := min(start+cueBatch, len(cues))
		batch := cues[start:end]
		e.report("cues %d-%d of %d", start+1, end, len(cues))

		got, err := e.translateBatch(ctx, instruction, batch)
		if err != nil {
			return err
		}
		if got != nil {
			for i := range batch {
				batch[i].text = got[i]
			}
			continue
		}

		// The numbering came back wrong. One call per cue cannot misalign.
		e.report("numbering came back wrong; redoing %d cues one by one", len(batch))
		for i := range batch {
			if strings.TrimSpace(batch[i].text) == "" {
				continue
			}
			out, err := e.chat(ctx, instruction, "1. "+batch[i].text)
			if err != nil {
				return err
			}
			batch[i].text = stripNumber(out)
		}
	}

	return nil
}

// translateBatch returns one translation per cue, or nil when the numbering
// did not survive the round trip.
func (e *engine) translateBatch(ctx context.Context, instruction string, batch []cue) ([]string, error) {
	var sb strings.Builder
	for i, c := range batch {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, strings.TrimSpace(c.text))
	}

	out, err := e.chat(ctx, instruction, sb.String())
	if err != nil {
		return nil, err
	}

	got := make([]string, len(batch))
	seen := 0
	for _, line := range strings.Split(out, "\n") {
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

// report writes progress to standard error, never to standard output: the
// translation on standard output has to stay clean for the next command in the
// pipeline.
func (e *engine) report(format string, args ...any) {
	if *flagQuiet {
		return
	}
	fmt.Fprintf(os.Stderr, "ai-tr: "+format+"\n", args...)
}
