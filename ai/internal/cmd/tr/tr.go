// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package tr translates text with a local language model.
package tr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/config"
	"github.com/assanoff/cmd/ai/internal/llm"
	"github.com/assanoff/cmd/ai/internal/subs"
	"github.com/assanoff/cmd/ai/internal/walk"
)

// Name is how this subcommand labels its own progress.
const Name = "ai tr"

const (
	Short = "Translate text with a local language model"
	Long  = `Translates text with a local language model.

With no file arguments tr reads standard input and writes the translation to
standard output. Given subtitles and --keep-format it translates only the
spoken text and leaves every timing untouched, which is what lets times
survive a pipeline:

    ai hear -f srt talk.mkv | ai tr -t en --keep-format | ai sum -s chapters`
)

// defaultMapTokens is the size of one translation piece. Smaller than sum's
// chunk because the answer here is as long as the input rather than a summary
// of it, and both have to fit in the context window at once.
const defaultMapTokens = 2000

// Command is the subcommand's flags.
type Command struct {
	ctx context.Context

	To         string  `short:"t" long:"to" default:"en" value-name:"CODE" description:"language to translate into, by short code such as en or ru"`
	From       string  `short:"F" long:"from" value-name:"CODE" description:"source language; empty lets the model work it out"`
	Model      string  `short:"m" long:"model" value-name:"NAME" description:"a role (fast, smart, code) or a canonical provider/modelID"`
	KeepFormat bool    `long:"keep-format" description:"for srt and vtt input, translate only the text and leave every timing alone"`
	MapTokens  int     `long:"map-tokens" description:"chunk size in tokens for long input; defaults to $AI_MAP_TOKENS"`
	Temp       float64 `short:"T" long:"temperature" default:"-1" description:"sampling temperature; negative leaves the model default"`
	MaxTokens  int     `long:"max-tokens" default:"4096" description:"cap each generation step in tokens"`
	Think      bool    `long:"think" description:"let a reasoning model think (off by default: it does not reach the translation)"`
	Exts       string  `long:"ext" default:"txt,md,srt,vtt" description:"comma-separated extensions to accept when walking directories"`
	Suffix     string  `long:"suffix" value-name:"S" description:"append this to each output base name; defaults to the target language"`

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

	to := strings.ToLower(strings.TrimSpace(c.To))
	if to == "" {
		return cli.Usagef("-t needs a language code such as en or ru")
	}

	p := cli.NewPrinter(Name, c.Quiet)

	limit := c.mapTokens(p)
	if limit < 128 {
		return cli.Usagef("--map-tokens %d is too small to translate anything; want 128 or more", limit)
	}

	// Naming a translation after its target language is what the old to-eng
	// script did, and it is the only suffix that stays right when the same
	// input is translated twice into different languages.
	suffix := c.Suffix
	if suffix == "" {
		suffix = "-" + to
	}

	jobs, err := cli.Jobs(walk.Request{
		Args:    c.Args.Files,
		Recurse: c.Recurse,
		Exts:    c.Exts,
		Out:     c.Out,
		OutDir:  c.OutDir,
		Suffix:  suffix,
		// Unlike the other subcommands tr chooses no extension of its own:
		// translating subtitles has to produce subtitles and translating
		// markdown has to stay markdown. An empty OutExt keeps each input's.
		OutExt: "",
		Report: p.Printf,
	})
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		p.Printf("nothing to translate")
		return nil
	}

	model := config.Resolve(c.Model, config.RoleFast)
	if c.DryRun {
		c.describe(jobs, model, to)
		return nil
	}

	// Before the model is loaded, not inside the run loop: a resumed batch
	// with nothing left to do should not pay for the load to discover that.
	jobs, err = cli.Pending(jobs, c.Force, p)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		return nil
	}

	eng, err := llm.New(c.ctx, model, p.Logger(), p)
	if err != nil {
		return err
	}
	defer eng.Close()

	t := translator{cmd: c, eng: eng, p: p, to: to}

	return cli.Each(c.ctx, p, jobs, func(j walk.Job) error {
		return t.one(c.ctx, j, limit)
	})
}

// one translates a single input.
func (t translator) one(ctx context.Context, j walk.Job, limit int) error {
	text, err := cli.ReadText(j.In)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("input is empty")
	}

	t.p.Printf("%s", cli.DescribeInput(j.In))

	var out string
	switch {
	case t.cmd.KeepFormat && subs.Is(text):
		preamble, cues := subs.Parse(text)
		if len(cues) == 0 {
			return errors.New("--keep-format was given but no subtitle cues were found")
		}
		if err := t.cues(ctx, cues); err != nil {
			return err
		}
		out = subs.Render(preamble, cues)

	case t.cmd.KeepFormat:
		// Saying nothing here would leave the user believing the timings were
		// protected when there were none to protect.
		t.p.Printf("%s: --keep-format ignored: not subtitles", cli.DescribeInput(j.In))
		fallthrough

	default:
		out, err = t.text(ctx, text, limit)
		if err != nil {
			return err
		}
		out += "\n"
	}

	w, closeOut, err := cli.OpenOutput(j.Out)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, out); err != nil {
		// The write error is the one worth reporting; a close failure on top of
		// it would only hide it.
		_ = closeOut()
		return err
	}
	return closeOut()
}

// mapTokens is the chunk size used when the input is longer than one call.
func (c *Command) mapTokens(p *cli.Printer) int {
	if c.MapTokens > 0 {
		return c.MapTokens
	}
	return config.Int("AI_MAP_TOKENS", defaultMapTokens, p.Printf)
}

// describe prints what a run would do. This is the payload of -n, so it goes
// to standard output.
func (c *Command) describe(jobs []walk.Job, model, to string) {
	fmt.Printf("model  %s\n", model)
	fmt.Printf("into   %s\n", to)
	if c.From == "" {
		fmt.Println("from   (detected)")
	} else {
		fmt.Printf("from   %s\n", c.From)
	}
	fmt.Printf("timing %v\n", c.KeepFormat)
	cli.PrintPlan(jobs)
}
