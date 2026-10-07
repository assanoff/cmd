// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package llm is the one place in this program that runs a language model.
//
// It owns inference only. Installing and inspecting the model stack is package
// stack's job, and that one talks to the SDK's tools packages directly.
//
// ask, sum and tr all run text models, and each used to carry its own copy of
// the setup dance — pick a llama.cpp bundle, download it, initialize, open the
// model store, download the model, load it with AutoTune. Three copies meant
// three places to edit when the SDK moved, and they had already drifted in
// their error messages. There is one copy now, and it is this file.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/ardanlabs/kronk/sdk/kronk"
	kmodel "github.com/ardanlabs/kronk/sdk/kronk/model"
	"github.com/ardanlabs/kronk/sdk/tools/libs"
	"github.com/ardanlabs/kronk/sdk/tools/models"

	"github.com/assanoff/cmd/ai/internal/cli"
)

// Engine owns the loaded language model. One engine serves every job in a run,
// which is the reason batching lives in the commands rather than in a shell
// loop: a loop would pay the model load per file, and a map-reduce over one
// document already makes many calls against one loaded model.
type Engine struct {
	krn    *kronk.Kronk
	name   string
	report *cli.Printer
}

// New installs whatever is missing and loads the model. The SDK picks a
// llama.cpp bundle for this machine, downloads it and the model into ~/.kronk
// if they are not there yet, and reports progress through log.
func New(ctx context.Context, name string, log kronk.Logger, report *cli.Printer) (*Engine, error) {
	lib, err := libs.New(libs.WithDetect(ctx, log))
	if err != nil {
		return nil, cli.NotSetupf("selecting llama.cpp libraries: %w", err)
	}
	if _, err := lib.Download(ctx, log); err != nil {
		return nil, cli.NotSetupf("installing llama.cpp libraries: %w", err)
	}
	if err := kronk.Init(kronk.WithLibPath(lib.LibsPath())); err != nil {
		return nil, cli.NotSetupf("initializing llama.cpp: %w", err)
	}

	mdls, err := models.New()
	if err != nil {
		return nil, cli.NotSetupf("opening the model store: %w", err)
	}

	mp, err := mdls.Download(ctx, log, name)
	if err != nil {
		return nil, cli.NotSetupf("model %q: %w", name, err)
	}
	if len(mp.ModelFiles) == 0 {
		return nil, cli.NotSetupf("model %q has no files on disk", name)
	}

	// AutoTune lets the SDK size the context window and KV cache from the
	// model metadata and this machine's memory, which is what we want: no
	// subcommand here has an opinion about either.
	krn, err := kronk.New(
		kmodel.WithModelFiles(mp.ModelFiles),
		kmodel.WithAutoTune(true),
	)
	if err != nil {
		return nil, cli.NotSetupf("loading model %q: %w", name, err)
	}

	return &Engine{krn: krn, name: name, report: report}, nil
}

// Close unloads the model. Failing to unload is worth reporting but never
// worth failing the run over: the answer is already written, which is why this
// returns nothing and reports instead.
func (e *Engine) Close() {
	// Deliberately not the run's context: Close runs on the way out of an
	// interrupted run too, and a cancelled context cannot unload a model.
	if err := e.krn.Unload(context.Background()); err != nil {
		e.report.Printf("unloading %s: %v", e.name, err)
	}
}

// Tokens counts the text the way this model will count it.
//
// Sizes have to come from the model's own tokenizer rather than a
// bytes-per-token guess. That guess is what makes naive chunkers overflow on
// Cyrillic: UTF-8 spends two bytes per letter there, so a byte estimate can be
// off by a factor of two in either direction depending on the vocabulary.
func (e *Engine) Tokens(ctx context.Context, text string) (int, error) {
	resp, err := e.krn.Tokenize(ctx, kmodel.D{"input": text})
	if err != nil {
		return 0, fmt.Errorf("tokenize: %w", err)
	}
	return resp.Tokens, nil
}

// Request is one call to the model.
//
// System and User are separate on purpose. Gluing an instruction and its text
// into one user message reads as a single block to the model, and a small one
// will answer by echoing the instruction back before the result — which lands
// the prompt on standard output and corrupts the pipeline. Separate roles keep
// the instruction out of the answer.
type Request struct {
	System      string
	User        string
	Temperature float64 // negative leaves the model default
	MaxTokens   int
	Thinking    *bool // nil leaves the model default
}

// payload builds the chat request the SDK takes.
func (r Request) payload() kmodel.D {
	var msgs []kmodel.D
	if r.System != "" {
		msgs = append(msgs, kmodel.TextMessage(kmodel.RoleSystem, r.System))
	}
	msgs = append(msgs, kmodel.TextMessage(kmodel.RoleUser, r.User))

	d := kmodel.D{
		"messages":   msgs,
		"max_tokens": r.MaxTokens,
	}
	if r.Temperature >= 0 {
		d["temperature"] = r.Temperature
	}
	if r.Thinking != nil {
		d["enable_thinking"] = *r.Thinking
	}
	return d
}

// Chat runs one request and returns the answer, trimmed.
func (e *Engine) Chat(ctx context.Context, req Request) (string, error) {
	resp, err := e.krn.Chat(ctx, req.payload())
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message == nil {
		return "", fmt.Errorf("the model returned no answer")
	}

	choice := resp.Choices[0]
	if choice.FinishReason() == kmodel.FinishReasonError {
		return "", fmt.Errorf("model error: %s", choice.Message.Content)
	}
	e.warnTruncated(choice)

	return strings.TrimSpace(choice.Message.Content), nil
}

// ChatJSON writes the whole response, token usage included. This is the first
// thing to look at when a prompt misbehaves.
func (e *Engine) ChatJSON(ctx context.Context, w io.Writer, req Request) error {
	resp, err := e.krn.Chat(ctx, req.payload())
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(resp)
}

// Stream writes the answer to w as it is generated, so a long reply shows
// progress instead of sitting silent.
//
// Reasoning does not go to w. It is worth watching but it is not the answer,
// and a pipeline that received it would be reading the model's notes as data;
// it goes to think instead, which may be nil to discard it.
//
// The bool reports whether anything was written, which is what lets the caller
// decide about a trailing newline.
func (e *Engine) Stream(ctx context.Context, w io.Writer, req Request, think io.Writer) (bool, error) {
	ch, err := e.krn.ChatStreaming(ctx, req.payload())
	if err != nil {
		return false, err
	}

	var wrote, reasoning bool
	for resp := range ch {
		if len(resp.Choices) == 0 {
			continue // a usage-only chunk
		}
		choice := resp.Choices[0]

		if choice.FinishReason() == kmodel.FinishReasonError {
			return wrote, fmt.Errorf("model error: %s", delta(choice).Content)
		}

		d := delta(choice)
		if d.Reasoning != "" {
			if think != nil {
				if _, err := io.WriteString(think, d.Reasoning); err != nil {
					return wrote, err
				}
				reasoning = true
			}
			continue
		}
		// The first content ends the reasoning block. Closing it here rather
		// than when the stream ends is what keeps the answer from running on
		// from the model's notes when both land on the same terminal.
		if reasoning && think != nil {
			reasoning = false
			if _, err := io.WriteString(think, "\n"); err != nil {
				return wrote, err
			}
		}
		if d.Content != "" {
			if _, err := io.WriteString(w, d.Content); err != nil {
				return wrote, err
			}
			wrote = true
		}

		e.warnTruncated(choice)
	}

	// An interrupted run ends the channel without an error of its own, so the
	// context is the only place the interruption is recorded.
	return wrote, ctx.Err()
}

// warnTruncated says so when an answer was cut off at its token cap. Silence
// here is the worst outcome: a summary that stops mid-sentence looks like a
// bad model rather than a budget that was too small.
func (e *Engine) warnTruncated(c kmodel.Choice) {
	if c.FinishReason() == kmodel.FinishReasonLength {
		e.report.Printf("a step hit --max-tokens and was cut short")
	}
}

// delta returns the incremental message of a streaming choice, or the complete
// one if the SDK filled that field instead.
func delta(c kmodel.Choice) kmodel.ResponseMessage {
	if c.Delta != nil {
		return *c.Delta
	}
	if c.Message != nil {
		return *c.Message
	}
	return kmodel.ResponseMessage{}
}
