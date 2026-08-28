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
	flagTo         = flag.String("t", "en", "language to translate into, by short code such as en or ru")
	flagFrom       = flag.String("F", "", "source language; empty lets the model work it out")
	flagModel      = flag.String("m", "", `model: a role ("fast", "smart", "code") or a canonical provider/modelID`)
	flagKeepFormat = flag.Bool("keep-format", false, "for srt and vtt input, translate only the text and leave every timing alone")
	flagMapTokens  = flag.Int("map-tokens", 0, "chunk size in tokens for long input; defaults to $AI_MAP_TOKENS")
	flagTemp       = flag.Float64("T", -1, "sampling temperature; negative leaves the model default")
	flagMaxTokens  = flag.Int("max-tokens", 4096, "cap each generation step in tokens")
	flagThink      = flag.Bool("think", false, "let a reasoning model think (off by default: it does not reach the translation)")
	flagRecurse    = flag.Bool("r", false, "descend into subdirectories of directory arguments")
	flagExts       = flag.String("ext", defaultExts, "comma-separated extensions to accept when walking directories")
	flagOut        = flag.String("o", "", "write the translation to this file instead of standard output")
	flagOutDir     = flag.String("O", "", "write translations below this directory, preserving the input layout")
	flagSuffix     = flag.String("suffix", "", "append this to each output base name; defaults to the target language")
	flagDry        = flag.Bool("n", false, "print the plan and exit without translating")
	flagForce      = flag.Bool("force", false, "overwrite existing output files instead of skipping them")
	flagQuiet      = flag.Bool("q", false, "suppress progress on standard error")
	flagVersion    = flag.Bool("version", false, "print the ai-tr and Kronk SDK versions")
)

// Long aliases for the short flags people type most. The flag package has no
// notion of a short/long pair, so each alias is a second name bound to the
// same variable.
func init() {
	flag.StringVar(flagTo, "to", "en", "alias for -t")
	flag.StringVar(flagFrom, "from", "", "alias for -F")
	flag.StringVar(flagModel, "model", "", "alias for -m")
	flag.BoolVar(flagDry, "dry", false, "alias for -n")
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: ai-tr [options] [file ...]

Translates text with a local language model. With no file arguments ai-tr reads
standard input and writes the translation to standard output.

options:
`)
	flag.PrintDefaults()
	os.Exit(exitUsage)
}

func main() {
	log.SetPrefix("ai-tr: ")
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
	to := strings.ToLower(strings.TrimSpace(*flagTo))
	if to == "" {
		return usagef("-t needs a language code such as en or ru")
	}
	if *flagOut != "" && *flagOutDir != "" {
		return usagef("-o and -O are mutually exclusive")
	}

	limit := mapTokens()
	if limit < 128 {
		return usagef("-map-tokens %d is too small to translate anything; want 128 or more", limit)
	}

	// Naming a translation after its target language is what the old to-eng
	// script did, and it is the only suffix that stays right when the same
	// input is translated twice into different languages.
	if *flagSuffix == "" {
		*flagSuffix = "-" + to
	}

	jobs, err := plan(flag.Args())
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		fmt.Fprintln(os.Stderr, "ai-tr: nothing to translate")
		return nil
	}

	name := resolveModel(*flagModel)
	if *flagDry {
		describe(jobs, name, to, *flagFrom, *flagKeepFormat)
		return nil
	}

	// Ctrl-C has to reach generation: a long document makes many calls, and
	// the user needs to be able to stop it.
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
		if err := translateJob(ctx, eng, j, to, limit); err != nil {
			if ctx.Err() != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "ai-tr: %s: %v\n", j.in, err)
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

// skipExisting honours the default of leaving finished work alone, so
// re-running a batch after an interruption only does what is left.
func skipExisting(j job) (bool, error) {
	if j.out == "" || *flagForce {
		return false, nil
	}
	switch _, err := os.Stat(j.out); {
	case err == nil:
		fmt.Fprintf(os.Stderr, "ai-tr: skip %s: %s exists (use -force)\n", j.in, j.out)
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, err
	}
}

func translateJob(ctx context.Context, eng *engine, j job, to string, limit int) error {
	text, err := readInput(j.in)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("input is empty")
	}

	if !*flagQuiet {
		fmt.Fprintf(os.Stderr, "ai-tr: %s\n", describeInput(j.in))
	}

	var out string
	switch {
	case *flagKeepFormat && subtitles(text):
		preamble, cues := parseSubtitles(text)
		if len(cues) == 0 {
			return errors.New("-keep-format was given but no subtitle cues were found")
		}
		if err := eng.translateCues(ctx, cues, to); err != nil {
			return err
		}
		out = renderSubtitles(preamble, cues)

	case *flagKeepFormat:
		// Saying nothing here would leave the user believing the timings were
		// protected when there were none to protect.
		fmt.Fprintf(os.Stderr, "ai-tr: %s: -keep-format ignored: not subtitles\n", describeInput(j.in))
		fallthrough

	default:
		out, err = eng.translate(ctx, text, to, limit)
		if err != nil {
			return err
		}
		out += "\n"
	}

	w, closeOut, err := openOutput(j.out)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, out); err != nil {
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
		fmt.Println("ai-tr (devel)")
		return
	}
	fmt.Printf("ai-tr %s\n", bi.Main.Version)
	fmt.Printf("kronk %s\n", moduleVersion(bi, "github.com/ardanlabs/kronk"))
	fmt.Printf("go    %s\n", bi.GoVersion)
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
