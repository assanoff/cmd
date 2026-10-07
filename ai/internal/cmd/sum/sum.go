// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package sum summarizes text, folding long input down in several passes.
package sum

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/core/config"
	"github.com/assanoff/cmd/ai/internal/core/llm"
	"github.com/assanoff/cmd/ai/internal/core/subs"
	"github.com/assanoff/cmd/ai/internal/core/walk"
)

// Name is how this subcommand labels its own progress.
const Name = "ai sum"

const (
	Short = "Summarize text, map-reduce over long input"
	Long  = `Summarizes text with a local language model, folding long input down in
several passes.

With no file arguments sum reads standard input and writes the summary to
standard output. Subtitles are recognized by their content and read as
timestamped prose, so

    ai hear -f srt talk.mkv | ai sum -s chapters

gives a chapter list that says where each chapter starts. Several styles
separated by commas produce one document with a heading per style, from a
single fold of the input:

    ai sum -s brief,terms,chapters,facts,tips -f md lecture.srt`
)

// Defaults that are sum's own. The chunk size is a compromise: large enough
// that a chapter survives in one piece, small enough to leave the model room
// for the summary it has to write.
const (
	defaultOutLang   = "ru"
	defaultMapTokens = 6000
)

// Command is the subcommand's flags.
type Command struct {
	ctx context.Context

	Style     string        `short:"s" long:"style" default:"chapters" description:"summary style, or several separated by commas: brief, chapters, facts, actions, terms, tips"`
	Lang      string        `short:"L" long:"lang" value-name:"CODE" description:"language to write the summary in; defaults to $AI_OUT_LANG"`
	Model     string        `short:"m" long:"model" value-name:"NAME" description:"a role (smart, fast, code) or a canonical provider/modelID"`
	MapTokens int           `long:"map-tokens" description:"chunk size in tokens for the map phase; defaults to $AI_MAP_TOKENS"`
	NoReduce  bool          `long:"no-reduce" description:"stop after compressing each chunk, without folding them into one summary"`
	Temp      float64       `short:"T" long:"temperature" default:"-1" description:"sampling temperature; negative leaves the model default"`
	MaxTokens int           `long:"max-tokens" default:"4096" description:"cap each generation step in tokens"`
	Think     bool          `long:"think" description:"let a reasoning model think (off by default: it does not reach the summary)"`
	Format    string        `short:"f" long:"format" default:"text" choice:"text" choice:"md" description:"output format"` //nolint:staticcheck // go-flags takes one choice tag per allowed value; SA5008 reads the repeat as a mistake
	Stamps    time.Duration `long:"timestamps" default:"1m" description:"with subtitle input, open a paragraph with its [HH:MM:SS] about this often; 0 drops the times"`
	Ctx       []string      `short:"c" long:"context" value-name:"FILE" description:"file of supporting material for the summary (slides, notes); repeatable"`
	CtxTokens int           `long:"ctx-tokens" default:"2000" description:"cap the supporting material from -c at this many tokens"`
	Exts      string        `long:"ext" default:"txt,md,srt,vtt" description:"comma-separated extensions to accept when walking directories"`
	Suffix    string        `long:"suffix" default:"-sum" value-name:"S" description:"append this to each output base name"`

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

	styles, err := parseStyles(c.Style)
	if err != nil {
		return err
	}

	limit := c.mapTokens(p)
	if limit < 256 {
		return cli.Usagef("--map-tokens %d is too small to summarize anything; want 256 or more", limit)
	}

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
		p.Printf("nothing to summarize")
		return nil
	}

	model := config.Resolve(c.Model, config.RoleSmart)
	lang := config.FirstNonEmpty(c.Lang, config.Value("AI_OUT_LANG"), defaultOutLang)
	if c.DryRun {
		c.describe(jobs, model, styles, lang, limit)
		return nil
	}

	// Read the supporting material before loading the model: a typo in a -c
	// path should fail in a second, not after a minute of model load.
	material, err := c.readContext(p)
	if err != nil {
		return err
	}

	eng, err := llm.New(c.ctx, model, p.Logger(), p)
	if err != nil {
		return err
	}
	defer eng.Close()

	f := folder{cmd: c, eng: eng, p: p}

	var failed bool
	for _, j := range jobs {
		skip, err := cli.SkipExisting(j.Out, c.Force, p, j.In)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		if err := c.one(f, j, styles, lang, limit, material); err != nil {
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

// one summarizes a single input.
func (c *Command) one(f folder, j walk.Job, styles []string, lang string, limit int, material string) error {
	text, err := cli.ReadText(j.In)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("input is empty")
	}

	f.p.Printf("%s", cli.DescribeInput(j.In))

	// Subtitles are prose plus times. Turn them back into prose and keep the
	// times as markers the prompts know to carry through; summarizing the cue
	// numbers and timing lines themselves would be nonsense.
	if subs.Is(text) {
		_, cues := subs.Parse(text)
		if len(cues) == 0 {
			return errors.New("input looks like subtitles but has no cues")
		}
		text = subs.StampedParagraphs(cues, c.Stamps)
		f.p.Printf("subtitles: %d cues", len(cues))
	}

	summary, err := f.summarize(c.ctx, text, styles, lang, limit, material)
	if err != nil {
		return err
	}

	w, closeOut, err := cli.OpenOutput(j.Out)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, summary); err != nil {
		// The write error is the one worth reporting; a close failure on top of
		// it would only hide it.
		_ = closeOut()
		return err
	}
	return closeOut()
}

// mapTokens is the chunk size for the map phase: the flag, then the config,
// then the default.
func (c *Command) mapTokens(p *cli.Printer) int {
	if c.MapTokens > 0 {
		return c.MapTokens
	}
	return config.Int("AI_MAP_TOKENS", defaultMapTokens, p.Printf)
}

// readContext loads the -c files into one block of supporting material. Each
// gets a named heading, because "the slides say X" is only useful to the model
// if it can tell the slides from the syllabus.
//
// Trimming happens later, against the model's tokenizer; here the job is only
// to read and label.
func (c *Command) readContext(p *cli.Printer) (string, error) {
	var sb strings.Builder
	for _, path := range c.Ctx {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		// A PDF or a .docx handed to -c is a mistake with an expensive
		// failure: llama.cpp's tokenizer segfaults on a hundred kilobytes of
		// binary, taking the run down after the fold has already been paid
		// for. Cheaper to notice here and name the tool that converts it.
		if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
			return "", cli.Usagef("-c %s is not text; convert it first (ai-read %s)", path, filepath.Base(path))
		}
		if strings.TrimSpace(string(b)) == "" {
			p.Printf("-c %s is empty, ignoring", path)
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "--- %s ---\n%s", filepath.Base(path), strings.TrimSpace(string(b)))
	}
	return sb.String(), nil
}

func formatExt(format string) string {
	if format == "md" {
		return ".md"
	}
	return ".txt"
}

// describe prints what a run would do. This is the payload of -n, so it goes
// to standard output.
func (c *Command) describe(jobs []walk.Job, model string, styles []string, lang string, limit int) {
	fmt.Printf("model      %s\n", model)
	fmt.Printf("styles     %s\n", strings.Join(styles, ", "))
	fmt.Printf("language   %s\n", lang)
	fmt.Printf("map-tokens %d\n", limit)
	if c.Stamps > 0 {
		fmt.Printf("timestamps every %s of subtitle input\n", c.Stamps)
	}
	for _, path := range c.Ctx {
		fmt.Printf("context    %s\n", path)
	}
	for _, j := range jobs {
		out := j.Out
		if out == "" {
			out = "(standard output)"
		}
		fmt.Printf("%s -> %s\n", cli.DescribeInput(j.In), out)
	}
	fmt.Printf("%d input(s)\n", len(jobs))
}
