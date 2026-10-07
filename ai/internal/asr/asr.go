// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package asr is the one place in this program that talks to Bucky, the
// whisper.cpp half of the Kronk SDK.
//
// It is the speech counterpart of package llm and deliberately mirrors its
// shape, because the two backends are easy to confuse and the cost of the
// confusion is high: they have separate library bundles and separate model
// stores, KRONK_LIB_PATH does not select whisper.cpp, and a whisper model is a
// GGML .bin rather than a GGUF. Keeping them in two packages that look alike
// but never mix is what stops that mistake.
package asr

import (
	"context"
	"io"
	"time"

	"github.com/ardanlabs/kronk/sdk/bucky"
	bmodel "github.com/ardanlabs/kronk/sdk/bucky/model"
	buckylibs "github.com/ardanlabs/kronk/sdk/tools/bucky/libs"
	buckymodels "github.com/ardanlabs/kronk/sdk/tools/bucky/models"

	"github.com/assanoff/cmd/ai/internal/cli"
)

// Transcription and Segment are re-exported so callers can render a result
// without importing the SDK themselves. Only this package knows which SDK is
// behind them.
type (
	Transcription = bmodel.Transcription
	Segment       = bmodel.Segment
)

// Engine owns the loaded whisper model. One engine serves every job in a run,
// which is the reason batching lives in the command rather than in a shell
// loop: a loop would pay the model load per file.
type Engine struct {
	b      *bucky.Bucky
	name   string
	report *cli.Printer
}

// New installs whatever is missing and loads the model. The SDK picks a
// whisper.cpp bundle for this machine, downloads it and the model into
// ~/.kronk if they are not there yet, and reports progress through log.
func New(ctx context.Context, name string, log bucky.Logger, report *cli.Printer) (*Engine, error) {
	lib, err := buckylibs.New(buckylibs.WithDetect(ctx, log))
	if err != nil {
		return nil, cli.NotSetupf("selecting whisper libraries: %w", err)
	}
	if _, err := lib.Download(ctx, log); err != nil {
		return nil, cli.NotSetupf("installing whisper libraries: %w", err)
	}

	// LogSilent keeps whisper.cpp's own diagnostics out of the terminal; the
	// segment callback is the progress the user actually wants.
	if err := bucky.Init(bucky.WithLibPath(lib.LibsPath()), bucky.WithLogLevel(bucky.LogSilent)); err != nil {
		return nil, cli.NotSetupf("initializing whisper: %w", err)
	}

	mdls, err := buckymodels.New()
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

	b, err := bucky.New(
		bmodel.WithModelPath(mp.ModelFiles[0]),
		bmodel.WithUseGPU(true),
		bmodel.WithLog(log),
	)
	if err != nil {
		return nil, cli.NotSetupf("loading model %q: %w", name, err)
	}

	return &Engine{b: b, name: name, report: report}, nil
}

// Close unloads the model. Failing to unload is worth reporting but never
// worth failing the run over: the transcript is already written.
func (e *Engine) Close() {
	if err := e.b.Unload(context.Background()); err != nil {
		e.report.Printf("unloading %s: %v", e.name, err)
	}
}

// Options are the decoder settings one transcription runs with.
type Options struct {
	// Language is a hint such as "ru"; empty autodetects.
	Language string
	// Prompt seeds the decoder with prior context: terminology, names,
	// acronyms it would otherwise mishear.
	Prompt string
	// Translate asks whisper to render the speech in English. Whisper
	// translates into English and nothing else, which is why this is a bool
	// rather than a language.
	Translate bool
	// Words asks for word-level timestamps.
	Words bool
	// OnSegment, when set, is called once per segment.
	//
	// Not as decoding happens, despite how it reads: the SDK runs whisper to
	// completion and only then walks the segments, calling this for each. It is
	// a replay of a finished transcript, which is why Waiting exists.
	OnSegment func(Segment)

	// Waiting, when set, is called every half minute for as long as the decoder
	// is running, with how long it has been going and how much audio it was
	// given. Nothing else reports progress: whisper.cpp can print its own, but
	// the SDK hard-codes that off, and no callback arrives until the end. A
	// nineteen-minute recording otherwise looks like a hung command.
	Waiting func(elapsed, audio time.Duration)
}

// Transcribe decodes one stream. A file on disk goes through TranscribePath
// instead, which can let ffmpeg seek; this is the path standard input takes.
func (e *Engine) Transcribe(ctx context.Context, r io.Reader, o Options) (Transcription, error) {
	samples, err := bmodel.Decode(ctx, r)
	if err != nil {
		return Transcription{}, err
	}
	return e.transcribe(ctx, samples, o)
}

// transcribe is where both input paths meet. Decoding happens before it so the
// length of the audio is known here, which is the only thing that makes the
// wait legible while whisper is busy.
func (e *Engine) transcribe(ctx context.Context, samples []float32, o Options) (Transcription, error) {
	defer o.waiting(ctx, time.Duration(len(samples))*time.Second/sampleRate)()
	return e.b.Transcribe(ctx, samples, o.options()...)
}

// waiting starts the heartbeat and returns the function that stops it.
func (o Options) waiting(ctx context.Context, audio time.Duration) func() {
	if o.Waiting == nil {
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		start := time.Now()
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				o.Waiting(time.Since(start).Round(time.Second), audio)
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() { close(done) }
}

func (o Options) options() []bmodel.TranscribeOption {
	var opts []bmodel.TranscribeOption

	if o.Language != "" {
		opts = append(opts, bmodel.WithLanguage(o.Language))
	}
	if o.Prompt != "" {
		opts = append(opts, bmodel.WithInitialPrompt(o.Prompt))
	}
	if o.Translate {
		opts = append(opts, bmodel.WithTranslate(true))
	}
	if o.Words {
		opts = append(opts, bmodel.WithWordTimestamps(true))
	}
	if o.OnSegment != nil {
		opts = append(opts, bmodel.WithOnSegment(o.OnSegment))
	}

	return opts
}
