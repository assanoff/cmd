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

// Exit codes. Scripts need to tell "you called me wrong" apart from "something
// is missing", so these are part of the interface and must stay stable.
const (
	exitError    = 1
	exitUsage    = 2
	exitNotSetup = 3
)

// errNotSetup marks a failure the user can fix by installing something. It
// surfaces as exit code 3, and doctor returns it when a check fails.
var errNotSetup = errors.New("the stack is not fully installed")

// command is one subcommand. Each owns its own flag set, the way the go tool
// does it, which keeps -h useful at both levels: ai-stack -h lists the
// subcommands and ai-stack models -h lists that subcommand's flags.
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
		installCommand(),
		updateCommand(),
		statusCommand(),
		doctorCommand(),
		modelsCommand(),
		useCommand(),
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: ai-stack <command> [options]

Installs and maintains the local model stack the ai-* commands run on.

commands:
`)
	// tabwriter rather than a fixed width: the usage lines differ enough in
	// length that any column guess is wrong for some of them.
	w := tabwriter.NewWriter(os.Stderr, 0, 0, 2, ' ', 0)
	for _, c := range commands {
		fmt.Fprintf(w, "  %s\t%s\n", c.usage, c.short)
	}
	w.Flush()
	fmt.Fprint(os.Stderr, `
Run "ai-stack <command> -h" for a command's own flags.
`)
	os.Exit(exitUsage)
}

func main() {
	log.SetPrefix("ai-stack: ")
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
		fmt.Fprintf(os.Stderr, "ai-stack: unknown command %q\n\n", args[0])
		usage()
	}

	cmd.flags.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ai-stack %s\n\n%s\n\noptions:\n", cmd.usage, cmd.short)
		cmd.flags.PrintDefaults()
		os.Exit(exitUsage)
	}
	if err := cmd.flags.Parse(args[1:]); err != nil {
		os.Exit(exitUsage)
	}

	// Downloads are long and interruptible; Ctrl-C has to reach them.
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
	switch {
	case errors.As(err, &ue):
		return exitUsage
	case errors.Is(err, errNotSetup):
		return exitNotSetup
	default:
		return exitError
	}
}

func printVersion() {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Println("ai-stack (devel)")
		return
	}
	fmt.Printf("ai-stack %s\n", bi.Main.Version)
	fmt.Printf("kronk    %s\n", moduleVersion(bi, "github.com/ardanlabs/kronk"))
	fmt.Printf("go       %s\n", bi.GoVersion)
}

// moduleVersion finds a dependency's version in the build info.
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
