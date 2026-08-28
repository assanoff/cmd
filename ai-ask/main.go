// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
)

// Exit codes. Scripts need to tell "you called me wrong" apart from "the model
// stack is missing", so these are part of the interface and must stay stable.
const (
	exitError    = 1
	exitUsage    = 2
	exitNotSetup = 3
)

// errNotSetup marks a failure the user can fix by installing something. It
// surfaces as exit code 3.
var errNotSetup = errors.New("model stack not installed")

var (
	flagPrompt     = flag.String("p", "", "the instruction to apply; without it the input is the prompt")
	flagPromptFile = flag.String("P", "", "read the instruction from this file")
	flagModel      = flag.String("m", "", `model: a role ("fast", "smart", "code") or a canonical provider/modelID`)
	flagSystem     = flag.String("s", "", "system message")
	flagTemp       = flag.Float64("T", -1, "sampling temperature; negative leaves the model default")
	flagMaxTokens  = flag.Int("max-tokens", 4096, "cap the answer length in tokens")
	flagThink      = flag.Bool("think", false, "ask a reasoning model to think (reasoning goes to standard error)")
	flagNoThink    = flag.Bool("no-think", false, "ask a reasoning model not to think")
	flagFormat     = flag.String("f", "text", "output format: text, md, json")
	flagRecurse    = flag.Bool("r", false, "descend into subdirectories of directory arguments")
	flagExts       = flag.String("ext", defaultExts, "comma-separated extensions to accept when walking directories")
	flagOut        = flag.String("o", "", "write the answer to this file instead of standard output")
	flagOutDir     = flag.String("O", "", "write answers below this directory, preserving the input layout")
	flagSuffix     = flag.String("suffix", "", "append this to each output base name")
	flagDry        = flag.Bool("n", false, "print the plan and exit without asking the model")
	flagForce      = flag.Bool("force", false, "overwrite existing output files instead of skipping them")
	flagQuiet      = flag.Bool("q", false, "suppress progress on standard error")
	flagVersion    = flag.Bool("version", false, "print the ai-ask and Kronk SDK versions")
)

// Long aliases for the short flags people type most. The flag package has no
// notion of a short/long pair, so each alias is a second name bound to the
// same variable.
func init() {
	flag.StringVar(flagPrompt, "prompt", "", "alias for -p")
	flag.StringVar(flagModel, "model", "", "alias for -m")
	flag.StringVar(flagFormat, "format", "text", "alias for -f")
	flag.BoolVar(flagDry, "dry", false, "alias for -n")
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: ai-ask [options] [file ...]

Applies one instruction to text using a local language model. With no file
arguments ai-ask reads standard input and writes the answer to standard output.

options:
`)
	flag.PrintDefaults()
	os.Exit(exitUsage)
}

func main() {
	log.SetPrefix("ai-ask: ")
	log.SetFlags(0)
	flag.Usage = usage
	flag.Parse()

	if *flagVersion {
		printVersion()
		return
	}

	if err := run(); err != nil {
		log.Print(err)
		os.Exit(exitCode(err))
	}
}

// usageError is a caller mistake rather than a runtime failure.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

func usagef(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

func exitCode(err error) int {
	var ue usageError
	switch {
	case errors.As(err, &ue):
		return exitUsage
	case errors.Is(err, errNotSetup):
		return exitNotSetup
	default:
		return exitError
	}
}

func run() error {
	format := strings.ToLower(*flagFormat)
	switch format {
	case "text", "md", "json":
	default:
		return usagef("unknown -f %q: want text, md or json", format)
	}
	if *flagOut != "" && *flagOutDir != "" {
		return usagef("-o and -O are mutually exclusive")
	}
	if *flagThink && *flagNoThink {
		return usagef("-think and -no-think are mutually exclusive")
	}

	prompt, err := instruction()
	if err != nil {
		return err
	}

	jobs, err := plan(flag.Args(), format)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		fmt.Fprintln(os.Stderr, "ai-ask: nothing to ask about")
		return nil
	}

	name := resolveModel(*flagModel)
	if *flagDry {
		describe(jobs, name, format, prompt)
		return nil
	}

	// Ctrl-C has to reach generation so a long answer can be stopped and the
	// model unloaded instead of leaving the process to be killed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	eng, err := newEngine(ctx, name, logger())
	if err != nil {
		return err
	}
	defer eng.close()

	var failed bool
	for _, j := range jobs {
		if skip, err := skipExisting(j); err != nil {
			return err
		} else if skip {
			continue
		}
		if err := askJob(ctx, eng, j, prompt, format); err != nil {
			if ctx.Err() != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "ai-ask: %s: %v\n", j.in, err)
			failed = true
			continue
		}
		if j.out != "" {
			fmt.Println(j.out)
		}
	}
	if failed {
		return errors.New("one or more inputs failed")
	}
	return nil
}

// instruction resolves the prompt from -p or -P. An empty result is allowed:
// the input text then serves as the prompt.
func instruction() (string, error) {
	if *flagPrompt != "" && *flagPromptFile != "" {
		return "", usagef("-p and -P are mutually exclusive")
	}
	if *flagPromptFile == "" {
		return *flagPrompt, nil
	}
	b, err := os.ReadFile(*flagPromptFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// skipExisting honours the default of leaving finished work alone, so
// re-running a batch after an interruption only does what is left.
func skipExisting(j job) (bool, error) {
	if j.out == "" || *flagForce {
		return false, nil
	}
	switch _, err := os.Stat(j.out); {
	case err == nil:
		fmt.Fprintf(os.Stderr, "ai-ask: skip %s: %s exists (use -force)\n", j.in, j.out)
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, err
	}
}

func askJob(ctx context.Context, eng *engine, j job, prompt string, format string) error {
	text, err := readInput(j.in)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" && prompt == "" {
		return usagef("no prompt and no input: give -p, or pipe some text in")
	}

	if !*flagQuiet {
		fmt.Fprintf(os.Stderr, "ai-ask: %s\n", describeInput(j.in))
	}

	w, closeOut, err := openOutput(j.out)
	if err != nil {
		return err
	}
	if err := eng.ask(ctx, w, prompt, text, format); err != nil {
		closeOut()
		return err
	}
	return closeOut()
}

func describeInput(in string) string {
	if in == "-" {
		return "standard input"
	}
	return in
}

func readInput(in string) (string, error) {
	if in == "-" {
		b, err := io.ReadAll(os.Stdin)
		return string(b), err
	}
	b, err := os.ReadFile(in)
	return string(b), err
}

// openOutput returns the destination writer and a closer that reports a write
// error. Standard output is never closed.
func openOutput(out string) (io.Writer, func() error, error) {
	if out == "" {
		return os.Stdout, func() error { return nil }, nil
	}
	if dir := filepath.Dir(out); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, err
		}
	}
	f, err := os.Create(out)
	if err != nil {
		return nil, nil, err
	}
	return f, f.Close, nil
}

func printVersion() {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Println("ai-ask (devel)")
		return
	}
	fmt.Printf("ai-ask %s\n", bi.Main.Version)
	fmt.Printf("kronk  %s\n", moduleVersion(bi, "github.com/ardanlabs/kronk"))
	fmt.Printf("go     %s\n", bi.GoVersion)
}

// moduleVersion finds a dependency's version in the build info. ai-stack
// doctor compares this across the installed ai-* commands: they all download
// native libraries into the same ~/.kronk, and Kronk, its yzma binding, and
// llama.cpp are only tested together.
func moduleVersion(bi *debug.BuildInfo, path string) string {
	for _, dep := range bi.Deps {
		if dep.Path == path {
			if dep.Replace != nil {
				return dep.Replace.Version + " (replaced)"
			}
			return dep.Version
		}
	}
	return "unknown"
}
