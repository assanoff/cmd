// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
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
	"time"
	"unicode/utf8"
)

// ctxFiles collects the repeated -c flag.
type ctxFiles []string

func (c *ctxFiles) String() string     { return strings.Join(*c, ", ") }
func (c *ctxFiles) Set(v string) error { *c = append(*c, v); return nil }

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
	flagStyle     = flag.String("s", "chapters", "summary style, or several separated by commas: brief, chapters, facts, actions, terms, tips")
	flagLang      = flag.String("L", "", "language to write the summary in; defaults to $AI_OUT_LANG")
	flagModel     = flag.String("m", "", `model: a role ("smart", "fast", "code") or a canonical provider/modelID`)
	flagMapTokens = flag.Int("map-tokens", 0, "chunk size in tokens for the map phase; defaults to $AI_MAP_TOKENS")
	flagNoReduce  = flag.Bool("no-reduce", false, "stop after compressing each chunk, without folding them into one summary")
	flagTemp      = flag.Float64("T", -1, "sampling temperature; negative leaves the model default")
	flagMaxTokens = flag.Int("max-tokens", 4096, "cap each generation step in tokens")
	flagThink     = flag.Bool("think", false, "let a reasoning model think (off by default: it does not reach the summary)")
	flagFormat    = flag.String("f", "text", "output format: text, md")
	flagStamps    = flag.Duration("ts", time.Minute, "with subtitle input, open a paragraph with its [HH:MM:SS] about this often; 0 drops the times")
	flagCtx       ctxFiles
	flagCtxTokens = flag.Int("ctx-tokens", 2000, "cap the supporting material from -c at this many tokens")
	flagRecurse   = flag.Bool("r", false, "descend into subdirectories of directory arguments")
	flagExts      = flag.String("ext", defaultExts, "comma-separated extensions to accept when walking directories")
	flagOut       = flag.String("o", "", "write the summary to this file instead of standard output")
	flagOutDir    = flag.String("O", "", "write summaries below this directory, preserving the input layout")
	flagSuffix    = flag.String("suffix", "-sum", "append this to each output base name")
	flagDry       = flag.Bool("n", false, "print the plan and exit without summarizing")
	flagForce     = flag.Bool("force", false, "overwrite existing output files instead of skipping them")
	flagQuiet     = flag.Bool("q", false, "suppress progress on standard error")
	flagVersion   = flag.Bool("version", false, "print the ai-sum and Kronk SDK versions")
)

// Long aliases for the short flags people type most. The flag package has no
// notion of a short/long pair, so each alias is a second name bound to the
// same variable.
func init() {
	// Supporting material is a list: slides, a syllabus and a reading all
	// belong to the same lecture.
	flag.Var(&flagCtx, "c", "file of supporting material for the summary (slides, notes); repeatable")

	flag.StringVar(flagStyle, "style", "chapters", "alias for -s")
	flag.StringVar(flagLang, "lang", "", "alias for -L")
	flag.StringVar(flagModel, "model", "", "alias for -m")
	flag.StringVar(flagFormat, "format", "text", "alias for -f")
	flag.BoolVar(flagDry, "dry", false, "alias for -n")
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: ai-sum [options] [file ...]

Summarizes text with a local language model, folding long input down in
several passes. With no file arguments ai-sum reads standard input and writes
the summary to standard output.

Subtitles are recognized by their content and read as timestamped prose, so
    ai-hear -f srt talk.mkv | ai-sum -s chapters
gives a chapter list that says where each chapter starts.

Several styles separated by commas produce one document with a heading per
style, from a single fold of the input:
    ai-sum -s brief,terms,chapters,facts,tips -f md lecture.srt

options:
`)
	flag.PrintDefaults()
	os.Exit(exitUsage)
}

func main() {
	log.SetPrefix("ai-sum: ")
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
	styles, err := parseStyles(*flagStyle)
	if err != nil {
		return err
	}

	format := strings.ToLower(*flagFormat)
	switch format {
	case "text", "md":
	default:
		return usagef("unknown -f %q: want text or md", format)
	}
	if *flagOut != "" && *flagOutDir != "" {
		return usagef("-o and -O are mutually exclusive")
	}

	limit := mapTokens()
	if limit < 256 {
		return usagef("-map-tokens %d is too small to summarize anything; want 256 or more", limit)
	}

	jobs, err := plan(flag.Args(), format)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		fmt.Fprintln(os.Stderr, "ai-sum: nothing to summarize")
		return nil
	}

	name := resolveModel(*flagModel)
	lang := outputLang()
	if *flagDry {
		describe(jobs, name, styles, lang, limit)
		return nil
	}

	// Read the supporting material before loading the model: a typo in a -c
	// path should fail in a second, not after a minute of model load.
	material, err := readContext(flagCtx)
	if err != nil {
		return err
	}

	// Ctrl-C has to reach generation: a fold over a long document makes many
	// calls, and the user needs to be able to stop it.
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
		if err := summarizeJob(ctx, eng, j, styles, lang, limit, material); err != nil {
			if ctx.Err() != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "ai-sum: %s: %v\n", j.in, err)
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
		fmt.Fprintf(os.Stderr, "ai-sum: skip %s: %s exists (use -force)\n", j.in, j.out)
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, err
	}
}

func summarizeJob(ctx context.Context, eng *engine, j job, styles []string, lang string, limit int, material string) error {
	text, err := readInput(j.in)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("input is empty")
	}

	if !*flagQuiet {
		fmt.Fprintf(os.Stderr, "ai-sum: %s\n", describeInput(j.in))
	}

	// Subtitles are prose plus times. Turn them back into prose and keep the
	// times as markers the prompts know to carry through; summarizing the
	// cue numbers and timing lines themselves would be nonsense.
	if subtitles(text) {
		_, cues := parseSubtitles(text)
		if len(cues) == 0 {
			return errors.New("input looks like subtitles but has no cues")
		}
		text = stampedParagraphs(cues, *flagStamps)
		if !*flagQuiet {
			fmt.Fprintf(os.Stderr, "ai-sum: subtitles: %d cues\n", len(cues))
		}
	}

	summary, err := eng.summarize(ctx, text, styles, lang, limit, material)
	if err != nil {
		return err
	}

	w, closeOut, err := openOutput(j.out)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, summary); err != nil {
		closeOut()
		return err
	}
	return closeOut()
}

// readContext loads the -c files into one block of supporting material. Each
// gets a named heading, because "the slides say X" is only useful to the model
// if it can tell the slides from the syllabus.
//
// Trimming happens later, against the model's tokenizer; here the job is only
// to read and label.
func readContext(paths []string) (string, error) {
	var sb strings.Builder
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		// A PDF or a .docx handed to -c is a mistake with an expensive
		// failure: llama.cpp's tokenizer segfaults on a hundred kilobytes of
		// binary, taking the run down after the fold has already been paid
		// for. Cheaper to notice here and name the tool that converts it.
		if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
			return "", usagef("-c %s is not text; convert it first (ai-read %s)", p, filepath.Base(p))
		}
		if strings.TrimSpace(string(b)) == "" {
			fmt.Fprintf(os.Stderr, "ai-sum: -c %s is empty, ignoring\n", p)
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "--- %s ---\n%s", filepath.Base(p), strings.TrimSpace(string(b)))
	}
	return sb.String(), nil
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
		fmt.Println("ai-sum (devel)")
		return
	}
	fmt.Printf("ai-sum %s\n", bi.Main.Version)
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
