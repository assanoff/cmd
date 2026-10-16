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
	"runtime/debug"
	"syscall"
	"text/tabwriter"
)

// exitFailure is the exit code for a run that went wrong, a check that could
// not run or a wrong call alike. The bash go-audit this replaces used 2 for
// both, and scripts rely on it.
const exitFailure = 2

// command is one subcommand with its own flag set, so that go-audit -h lists
// the commands and go-audit reports -h the flags of reports.
type command struct {
	name  string
	usage string
	short string
	flags *flag.FlagSet
	run   func(ctx context.Context, args []string) error
}

var commands []*command

func init() {
	commands = []*command{
		reportsCommand(),
		versionCommand(),
	}
}

// stdout is where the commands write their results; a variable so that tests
// can read it.
var stdout io.Writer = os.Stdout

func usage() {
	fmt.Fprint(os.Stderr, `usage: go-audit <command> [options]

Keeps the reports of Go code checks in one place: its own and those of the
go-style Claude Code plugin.

commands:
`)
	w := tabwriter.NewWriter(os.Stderr, 0, 0, 2, ' ', 0)
	for _, c := range commands {
		_, _ = fmt.Fprintf(w, "  %s\t%s\n", c.usage, c.short)
	}
	// The usage is the last thing printed before exiting; a failure to print
	// it leaves nothing to do differently.
	_ = w.Flush()
	fmt.Fprint(os.Stderr, `
Run "go-audit <command> -h" for a command's own flags.
`)
}

func main() {
	log.SetPrefix("go-audit: ")
	log.SetFlags(0)

	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(exitFailure)
	}
	switch args[0] {
	case "-h", "-help", "--help":
		usage()
		return
	}
	cmd := lookup(args[0])
	if cmd == nil {
		log.Printf("unknown command %q", args[0])
		usage()
		os.Exit(exitFailure)
	}

	cmd.flags.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: go-audit %s\n\n%s\n", cmd.usage, cmd.short)
		if hasFlags(cmd.flags) {
			fmt.Fprint(os.Stderr, "\noptions:\n")
			cmd.flags.PrintDefaults()
		}
	}
	if err := cmd.flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(exitFailure)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cmd.run(ctx, cmd.flags.Args()); err != nil {
		log.Print(err)
		if _, ok := errors.AsType[usageError](err); ok {
			fmt.Fprintf(os.Stderr, "run \"go-audit %s -h\" for usage\n", cmd.name)
		}
		os.Exit(exitFailure)
	}
}

func lookup(name string) *command {
	for _, c := range commands {
		if c.name == name {
			return c
		}
	}
	return nil
}

func hasFlags(fs *flag.FlagSet) bool {
	n := 0
	fs.VisitAll(func(*flag.Flag) { n++ })
	return n > 0
}

// usageError is a wrong call rather than a failure of the run.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

func usagef(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

func versionCommand() *command {
	return &command{
		name:  "version",
		usage: "version",
		short: "Print the version of go-audit and of the Go it was built with",
		flags: flag.NewFlagSet("version", flag.ContinueOnError),
		run: func(context.Context, []string) error {
			v, goVersion := "(devel)", "unknown"
			if bi, ok := debug.ReadBuildInfo(); ok {
				v, goVersion = bi.Main.Version, bi.GoVersion
			}
			_, err := fmt.Fprintf(stdout, "go-audit %s\ngo       %s\n", v, goVersion)
			return err
		},
	}
}
