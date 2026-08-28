// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ardanlabs/kronk/sdk/kronk"
	kmodel "github.com/ardanlabs/kronk/sdk/kronk/model"
	"github.com/ardanlabs/kronk/sdk/tools/libs"
	"github.com/ardanlabs/kronk/sdk/tools/models"
)

// engine owns the loaded language model. One engine serves every job in a run,
// which is the reason batching lives in this command rather than in a shell
// loop: a loop would pay the model load per file.
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

	// AutoTune lets the SDK size the context window and KV cache from the
	// model metadata and this machine's memory, which is what we want: this
	// command has no opinion about either.
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
// worth failing the run over: the answer is already written.
func (e *engine) close() {
	if err := e.krn.Unload(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "ai-ask: unloading %s: %v\n", e.name, err)
	}
}

// ask runs one prompt and writes the answer to w.
func (e *engine) ask(ctx context.Context, w io.Writer, prompt, text, format string) error {
	if format == "json" {
		return e.askJSON(ctx, w, prompt, text)
	}
	return e.askStream(ctx, w, prompt, text)
}

// askStream writes the answer as it is generated, so a long reply shows
// progress instead of sitting silent. Reasoning goes to standard error: it is
// worth watching but it is not the answer.
func (e *engine) askStream(ctx context.Context, w io.Writer, prompt, text string) error {
	ch, err := e.krn.ChatStreaming(ctx, e.request(prompt, text))
	if err != nil {
		return err
	}

	reasoning := false
	wrote := false

	for resp := range ch {
		if len(resp.Choices) == 0 {
			continue // a usage-only chunk
		}
		choice := resp.Choices[0]

		if choice.FinishReason() == kmodel.FinishReasonError {
			return fmt.Errorf("model error: %s", delta(choice).Content)
		}

		d := delta(choice)
		if d.Reasoning != "" {
			if !*flagQuiet {
				if !reasoning {
					fmt.Fprint(os.Stderr, "ai-ask: thinking: ")
					reasoning = true
				}
				fmt.Fprint(os.Stderr, d.Reasoning)
			}
			continue
		}
		if reasoning {
			reasoning = false
			if !*flagQuiet {
				fmt.Fprintln(os.Stderr)
			}
		}
		if d.Content != "" {
			if _, err := io.WriteString(w, d.Content); err != nil {
				return err
			}
			wrote = true
		}

		if choice.FinishReason() == kmodel.FinishReasonLength && !*flagQuiet {
			fmt.Fprintln(os.Stderr, "ai-ask: answer hit -max-tokens and was cut short")
		}
	}

	// A trailing newline so the output behaves like every other text filter.
	if wrote {
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// askJSON returns the whole response, token usage included. This is the first
// thing to look at when a prompt misbehaves.
func (e *engine) askJSON(ctx context.Context, w io.Writer, prompt, text string) error {
	resp, err := e.krn.Chat(ctx, e.request(prompt, text))
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(resp)
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

// request builds the chat payload. The prompt goes first and the input follows
// it, which is the order instruction-tuned models expect.
func (e *engine) request(prompt, text string) kmodel.D {
	var msgs []kmodel.D
	if *flagSystem != "" {
		msgs = append(msgs, kmodel.TextMessage(kmodel.RoleSystem, *flagSystem))
	}
	msgs = append(msgs, kmodel.TextMessage(kmodel.RoleUser, userContent(prompt, text)))

	d := kmodel.D{
		"messages":   msgs,
		"max_tokens": *flagMaxTokens,
	}
	if *flagTemp >= 0 {
		d["temperature"] = *flagTemp
	}
	if *flagThink {
		d["enable_thinking"] = true
	}
	if *flagNoThink {
		d["enable_thinking"] = false
	}
	return d
}

// userContent joins the instruction and the text. Either may be empty: with no
// -p the input is the whole prompt, and with no input the prompt stands alone.
func userContent(prompt, text string) string {
	prompt = strings.TrimSpace(prompt)
	text = strings.TrimSpace(text)

	switch {
	case prompt == "":
		return text
	case text == "":
		return prompt
	default:
		return prompt + "\n\n" + text
	}
}
