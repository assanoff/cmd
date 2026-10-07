// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package ask runs one prompt against a local language model.
package ask

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/config"
	"github.com/assanoff/cmd/ai/internal/llm"
	"github.com/assanoff/cmd/ai/internal/walk"
)

// Name is how this subcommand labels its own progress.
const Name = "ai ask"

// Short and Long are what go-flags prints for this subcommand.
const (
	Short = "Run one prompt against a local language model"
	Long  = `Applies one instruction to text using a local language model.

With no file arguments ask reads standard input and writes the answer to
standard output, so it takes the tail of a pipeline:

    ai hear lecture.mkv | ai ask -p 'list every command mentioned'

The instruction comes from -p, or from a file with -P. Without either, the
input itself is the prompt. Given files or directories, the same prompt is
applied to each one; the model is loaded once for the whole run, so a batch
does not pay the load cost per file.`
)

// Command is the subcommand's flags. go-flags fills it in from the command
// line and then calls Execute.
type Command struct {
	// ctx carries the interrupt signal from main. go-flags calls Execute with
	// no context of its own, so the only way in is a field.
	ctx context.Context

	Prompt     string  `short:"p" long:"prompt" value-name:"TEXT" description:"the instruction to apply; without it the input is the prompt"`
	PromptFile string  `short:"P" long:"prompt-file" value-name:"FILE" description:"read the instruction from this file"`
	Model      string  `short:"m" long:"model" value-name:"NAME" description:"a role (fast, smart, code) or a canonical provider/modelID"`
	System     string  `short:"s" long:"system" value-name:"TEXT" description:"system message"`
	Temp       float64 `short:"T" long:"temperature" default:"-1" description:"sampling temperature; negative leaves the model default"`
	MaxTokens  int     `long:"max-tokens" default:"4096" description:"cap the answer length in tokens"`
	Think      bool    `long:"think" description:"ask a reasoning model to think (reasoning goes to standard error)"`
	NoThink    bool    `long:"no-think" description:"ask a reasoning model not to think"`
	Format     string  `short:"f" long:"format" default:"text" choice:"text" choice:"md" choice:"json" description:"output format"` //nolint:staticcheck // go-flags takes one choice tag per allowed value; SA5008 reads the repeat as a mistake
	Exts       string  `long:"ext" default:"txt,md,srt,vtt" description:"comma-separated extensions to accept when walking directories"`
	Suffix     string  `long:"suffix" value-name:"S" description:"append this to each output base name"`

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
	if c.Think && c.NoThink {
		return cli.Usagef("--think and --no-think are mutually exclusive")
	}

	p := cli.NewPrinter(Name, c.Quiet)

	prompt, err := c.instruction()
	if err != nil {
		return err
	}

	jobs, err := cli.Jobs(walk.Request{
		Args:    c.Args.Files,
		Recurse: c.Recurse,
		Exts:    c.Exts,
		Out:     c.Out,
		OutDir:  c.OutDir,
		Suffix:  c.Suffix,
		OutExt:  cli.FormatExt(c.Format),
		Report:  p.Printf,
	})
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		p.Printf("nothing to ask about")
		return nil
	}

	model := config.Resolve(c.Model, config.RoleFast)
	if c.DryRun {
		c.describe(jobs, model, prompt)
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

	return cli.Each(c.ctx, p, jobs, func(j walk.Job) error {
		return c.one(eng, p, j, prompt)
	})
}

// one answers for a single input.
func (c *Command) one(eng *llm.Engine, p *cli.Printer, j walk.Job, prompt string) error {
	text, err := cli.ReadText(j.In)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" && prompt == "" {
		return cli.Usagef("no prompt and no input: give -p, or pipe some text in")
	}

	p.Printf("%s", cli.DescribeInput(j.In))

	w, closeOut, err := cli.OpenOutput(j.Out)
	if err != nil {
		return err
	}
	if err := c.answer(eng, p, w, prompt, text); err != nil {
		// The write error is the one worth reporting; a close failure on top of
		// it would only hide it.
		_ = closeOut()
		return err
	}
	return closeOut()
}

// answer runs the model and writes the result.
func (c *Command) answer(eng *llm.Engine, p *cli.Printer, w io.Writer, prompt, text string) error {
	req := llm.Request{
		System:      c.System,
		User:        join(prompt, text),
		Temperature: c.Temp,
		MaxTokens:   c.MaxTokens,
		Thinking:    c.thinking(),
	}

	if c.Format == "json" {
		return eng.ChatJSON(c.ctx, w, req)
	}

	think := &thinkWriter{p: p}
	wrote, err := eng.Stream(c.ctx, w, req, think)
	if err != nil {
		return err
	}
	think.done()

	// A trailing newline so the output behaves like every other text filter.
	if wrote {
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
	}
	return nil
}

// thinking turns the two flags into the tri-state the engine takes: think,
// do not think, or leave the model to its own default. The two are rejected as
// mutually exclusive above, so Think alone carries the answer.
func (c *Command) thinking() *bool {
	if c.Think || c.NoThink {
		return &c.Think
	}
	return nil
}

// thinkWriter prints streamed reasoning to standard error, labelling the block
// once rather than once per chunk.
type thinkWriter struct {
	p    *cli.Printer
	open bool
}

func (t *thinkWriter) Write(b []byte) (int, error) {
	if !t.open {
		t.p.Open("thinking: ")
		t.open = true
	}
	t.p.Raw(string(b))
	return len(b), nil
}

// done closes the block if the model reasoned but never produced an answer.
func (t *thinkWriter) done() {
	if t.open {
		t.p.Close()
		t.open = false
	}
}

// instruction resolves the prompt from -p or -P. An empty result is allowed:
// the input text then serves as the prompt.
func (c *Command) instruction() (string, error) {
	if c.Prompt != "" && c.PromptFile != "" {
		return "", cli.Usagef("-p and -P are mutually exclusive")
	}
	if c.PromptFile == "" {
		return c.Prompt, nil
	}
	s, err := cli.ReadText(c.PromptFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

// join puts the instruction before the input, which is the order
// instruction-tuned models expect. Either may be empty: with no -p the input
// is the whole prompt, and with no input the prompt stands alone.
func join(prompt, text string) string {
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

// describe prints what a run would do. This is the payload of -n, so it goes
// to standard output.
func (c *Command) describe(jobs []walk.Job, model, prompt string) {
	fmt.Printf("model  %s\n", model)
	fmt.Printf("format %s\n", c.Format)
	if prompt == "" {
		fmt.Println("prompt (the input itself)")
	} else {
		fmt.Printf("prompt %s\n", firstLine(prompt))
	}
	cli.PrintPlan(jobs)
}

// firstLine keeps the plan readable when the prompt is a paragraph.
func firstLine(s string) string {
	if head, _, ok := strings.Cut(s, "\n"); ok {
		return head + " ..."
	}
	return s
}
