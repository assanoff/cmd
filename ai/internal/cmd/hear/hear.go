// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package hear transcribes speech to text.
package hear

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/core/asr"
	"github.com/assanoff/cmd/ai/internal/core/config"
	"github.com/assanoff/cmd/ai/internal/core/walk"
)

// Name is how this subcommand labels its own progress.
const Name = "ai hear"

const (
	Short = "Transcribe speech to text"
	Long  = `Transcribes speech to text with whisper.cpp.

With no file arguments hear reads standard input and writes the transcript to
standard output. Anything ffmpeg can read is accepted; -f srt carries the
times, which is what lets the rest of the pipeline keep them:

    ai hear -f srt lecture.mkv | ai sum -s chapters`
)

// Command is the subcommand's flags.
//
// The --ext default is the media set the old whisper wrapper accepted. It only
// filters directory walks; a file named on the command line is transcribed
// whatever it is called. It is spelled out in the tag rather than taken from a
// constant because a struct tag has to be a literal.
type Command struct {
	ctx context.Context

	Model  string `short:"m" long:"model" value-name:"NAME" description:"a role (asr, asr-fast) or an explicit whisper model (large-v3-turbo, base.en)"`
	Lang   string `short:"l" long:"lang" value-name:"CODE" description:"language hint such as ru or en; empty autodetects"`
	Format string `short:"f" long:"format" default:"text" choice:"text" choice:"json" choice:"srt" choice:"vtt" description:"output format"` //nolint:staticcheck // go-flags takes one choice tag per allowed value; SA5008 reads the repeat as a mistake
	Prompt string `short:"p" long:"prompt" value-name:"TEXT" description:"seed the decoder with prior context: terminology, names, acronyms"`
	Xlate  bool   `short:"t" long:"translate" description:"translate the speech to English (whisper translates only into English)"`
	Words  bool   `long:"words" description:"include word-level timestamps (json and vtt)"`
	Exts   string `long:"ext" default:"mp3,mp4,wav,m4a,flac,ogg,opus,mkv,avi,mov,wmv,flv,webm,m4v,mpg,mpeg,aac" description:"comma-separated extensions to accept when walking directories"`
	Suffix string `long:"suffix" value-name:"S" description:"append this to each output base name"`

	cli.Batch

	Args struct {
		Files []string `positional-arg-name:"file" description:"files or directories; none reads standard input"`
	} `positional-args:"yes"`
}

// New returns the subcommand bound to the run's context.
func New(ctx context.Context) *Command { return &Command{ctx: ctx} }

// Execute is the go-flags entry point.
func (c *Command) Execute([]string) error {
	if err := c.Check(); err != nil {
		return err
	}

	p := cli.NewPrinter(Name, c.Quiet)

	jobs, err := walk.Jobs(walk.Request{
		Args:    c.Args.Files,
		Recurse: c.Recurse,
		Exts:    c.Exts,
		Out:     c.Out,
		OutDir:  c.OutDir,
		Suffix:  c.Suffix,
		Ext:     func(string) string { return formatExt(c.Format) },
		Report:  p.Printf,
	})
	switch {
	case errors.Is(err, walk.ErrTerminalInput):
		return cli.Usagef("%v", err)
	case err != nil:
		var be *walk.BatchError
		if errors.As(err, &be) {
			return cli.Usagef("%v", be)
		}
		return err
	}
	if len(jobs) == 0 {
		p.Printf("nothing to transcribe")
		return nil
	}

	model := config.Resolve(c.Model, config.RoleASR)
	if c.DryRun {
		c.describe(jobs, model)
		return nil
	}

	eng, err := asr.New(c.ctx, model, p.Logger(), p)
	if err != nil {
		return err
	}
	defer eng.Close()

	var failed bool
	for _, j := range jobs {
		skip, err := cli.SkipExisting(j.Out, c.Force, p, j.In)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		if err := c.one(eng, p, j); err != nil {
			if c.ctx.Err() != nil {
				return err
			}
			p.Printf("%s: %v", j.In, err)
			failed = true
			continue
		}
		if j.Out != "" {
			fmt.Println(j.Out)
		}
	}
	if failed {
		return errors.New("one or more inputs failed")
	}
	return nil
}

// one transcribes a single input.
func (c *Command) one(eng *asr.Engine, p *cli.Printer, j walk.Job) error {
	p.Printf("%s", cli.DescribeInput(j.In))

	r, closeIn, err := cli.OpenInput(j.In)
	if err != nil {
		return err
	}
	defer closeIn()

	tr, err := eng.Transcribe(c.ctx, r, c.options(p))
	if err != nil {
		return err
	}

	w, closeOut, err := cli.OpenOutput(j.Out)
	if err != nil {
		return err
	}
	if err := render(w, tr, c.Format); err != nil {
		// The write error is the one worth reporting; a close failure on top of
		// it would only hide it.
		_ = closeOut()
		return err
	}
	return closeOut()
}

func (c *Command) options(p *cli.Printer) asr.Options {
	o := asr.Options{
		Language:  config.FirstNonEmpty(c.Lang, config.Value("AI_LANG")),
		Prompt:    c.Prompt,
		Translate: c.Xlate,
		Words:     c.Words,
	}
	if !p.Quiet() {
		// Decoding shows as it happens, on standard error so the transcript on
		// standard output stays clean.
		o.OnSegment = func(seg asr.Segment) {
			text := strings.TrimSpace(seg.Text)
			if text == "" {
				return
			}
			p.Raw(fmt.Sprintf("  %s  %s\n", stamp(seg.StartMs, "."), text))
		}
	}
	return o
}

func formatExt(format string) string {
	switch format {
	case "json":
		return ".json"
	case "srt":
		return ".srt"
	case "vtt":
		return ".vtt"
	default:
		return ".txt"
	}
}

// describe prints what a run would do. This is the payload of -n, so it goes
// to standard output.
func (c *Command) describe(jobs []walk.Job, model string) {
	fmt.Printf("model  %s\n", model)
	fmt.Printf("format %s\n", c.Format)
	for _, j := range jobs {
		out := j.Out
		if out == "" {
			out = "(standard output)"
		}
		fmt.Printf("%s -> %s\n", cli.DescribeInput(j.In), out)
	}
	fmt.Printf("%d input(s)\n", len(jobs))
}
