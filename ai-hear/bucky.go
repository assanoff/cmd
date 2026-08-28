// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ardanlabs/kronk/sdk/bucky"
	bmodel "github.com/ardanlabs/kronk/sdk/bucky/model"
	buckylibs "github.com/ardanlabs/kronk/sdk/tools/bucky/libs"
	buckymodels "github.com/ardanlabs/kronk/sdk/tools/bucky/models"
)

// engine owns the loaded whisper model. One engine serves every job in a run,
// which is the reason batching lives in this command rather than in a shell
// loop: a loop would pay the model load per file.
type engine struct {
	b    *bucky.Bucky
	name string
}

// newEngine installs whatever is missing and loads the model. The SDK picks a
// whisper.cpp bundle for this machine, downloads it and the model into
// ~/.kronk if they are not there yet, and reports progress through log.
func newEngine(ctx context.Context, name string, log bucky.Logger) (*engine, error) {
	lib, err := buckylibs.New(buckylibs.WithDetect(ctx, log))
	if err != nil {
		return nil, fmt.Errorf("%w: selecting whisper libraries: %w", errNotSetup, err)
	}
	if _, err := lib.Download(ctx, log); err != nil {
		return nil, fmt.Errorf("%w: installing whisper libraries: %w", errNotSetup, err)
	}

	// LogSilent keeps whisper.cpp's own diagnostics out of the terminal; the
	// segment callback below is the progress the user actually wants.
	if err := bucky.Init(bucky.WithLibPath(lib.LibsPath()), bucky.WithLogLevel(bucky.LogSilent)); err != nil {
		return nil, fmt.Errorf("%w: initializing whisper: %w", errNotSetup, err)
	}

	mdls, err := buckymodels.New()
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

	b, err := bucky.New(
		bmodel.WithModelPath(mp.ModelFiles[0]),
		bmodel.WithUseGPU(true),
		bmodel.WithLog(log),
	)
	if err != nil {
		return nil, fmt.Errorf("loading model %q: %w", name, err)
	}

	return &engine{b: b, name: name}, nil
}

// close unloads the model. Failing to unload is worth reporting but never
// worth failing the run over: the transcript is already written.
func (e *engine) close() {
	if err := e.b.Unload(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "ai-hear: unloading %s: %v\n", e.name, err)
	}
}

// transcribe decodes one input. TranscribeFile takes the whole stream, so
// there is no chunking here: anything ffmpeg can read goes in as it is, and
// the segment timings come back relative to the start of the file.
func (e *engine) transcribe(ctx context.Context, r io.Reader) (bmodel.Transcription, error) {
	return e.b.TranscribeFile(ctx, r, e.options()...)
}

func (e *engine) options() []bmodel.TranscribeOption {
	var opts []bmodel.TranscribeOption

	if lang := language(); lang != "" {
		opts = append(opts, bmodel.WithLanguage(lang))
	}
	if *flagPrompt != "" {
		opts = append(opts, bmodel.WithInitialPrompt(*flagPrompt))
	}
	if *flagXlate {
		opts = append(opts, bmodel.WithTranslate(true))
	}
	if *flagWords {
		opts = append(opts, bmodel.WithWordTimestamps(true))
	}
	if !*flagQuiet {
		opts = append(opts, bmodel.WithOnSegment(reportSegment))
	}

	return opts
}

// reportSegment shows decoding as it happens. It goes to standard error so the
// transcript on standard output stays clean.
func reportSegment(seg bmodel.Segment) {
	text := strings.TrimSpace(seg.Text)
	if text == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "  %s  %s\n", stamp(seg.StartMs, "."), text)
}
