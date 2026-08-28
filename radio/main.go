// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"text/tabwriter"
)

// Exit codes. Scripts need to tell "you called me wrong" apart from a runtime
// failure, so these are part of the interface and must stay stable.
const (
	exitError = 1
	exitUsage = 2
)

// command is one subcommand. Each owns its own flag set, the way the go tool
// does it, which keeps -h useful at both levels: radio -h lists the
// subcommands and radio gen -h lists that subcommand's flags.
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
		genCommand(),
		serveCommand(),
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: radio <command> [options]

Publishes a directory of audio as a podcast: RSS feeds with chapters, cover
art and transcripts, from plain files and no database.

commands:
`)
	w := tabwriter.NewWriter(os.Stderr, 0, 0, 2, ' ', 0)
	for _, c := range commands {
		fmt.Fprintf(w, "  %s\t%s\n", c.usage, c.short)
	}
	w.Flush()
	fmt.Fprint(os.Stderr, `
Run "radio <command> -h" for a command's own flags.
`)
	os.Exit(exitUsage)
}

func main() {
	log.SetPrefix("radio: ")
	log.SetFlags(0)
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
	}

	if args[0] == "-version" || args[0] == "--version" || args[0] == "version" {
		printVersion()
		return
	}

	cmd := lookup(args[0])
	if cmd == nil {
		fmt.Fprintf(os.Stderr, "radio: unknown command %q\n\n", args[0])
		usage()
	}

	cmd.flags.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: radio %s\n\n%s\n\noptions:\n", cmd.usage, cmd.short)
		cmd.flags.PrintDefaults()
		os.Exit(exitUsage)
	}
	if err := cmd.flags.Parse(args[1:]); err != nil {
		os.Exit(exitUsage)
	}

	// Serving is a long-running job and generating a large archive is not
	// instant; Ctrl-C has to reach both.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cmd.run(ctx, cmd.flags.Args()); err != nil {
		log.Print(err)
		os.Exit(exitCode(err))
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

// usageError is a caller mistake rather than a runtime failure.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

func usagef(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

func exitCode(err error) int {
	var ue usageError
	if errors.As(err, &ue) {
		return exitUsage
	}
	return exitError
}

func printVersion() {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Println("radio (devel)")
		return
	}
	fmt.Printf("radio %s\n", bi.Main.Version)
	fmt.Printf("go    %s\n", bi.GoVersion)
}
