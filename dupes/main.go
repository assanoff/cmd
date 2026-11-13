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
	"syscall"

	"github.com/google/subcommands"
)

// stdout is where the reports go; a variable so that tests can read it.
var stdout io.Writer = os.Stdout

func main() {
	log.SetPrefix("dupes: ")
	log.SetFlags(0)

	cdr := subcommands.NewCommander(flag.CommandLine, "dupes")
	cdr.Register(&scanCmd{}, "")
	cdr.Register(&filesCmd{}, "")
	cdr.Register(&dirsCmd{}, "")
	cdr.Register(&statsCmd{}, "")
	cdr.Register(&forgetCmd{}, "")
	cdr.Register(&resetCmd{}, "")
	cdr.Register(&webCmd{}, "")
	cdr.Register(&versionCmd{}, "")
	cdr.Register(cdr.HelpCommand(), "help")
	cdr.Register(cdr.CommandsCommand(), "help")
	cdr.Explain = func(w io.Writer) {
		_, _ = fmt.Fprint(w, `usage: dupes <command> [flags] [args]

Finds duplicate files and directories: scan records what the paths hold in a
SQLite database, and the other commands report from it. Nothing on the disks
is deleted, moved or changed.

`)
		cdr.VisitGroups(func(g *subcommands.CommandGroup) {
			cdr.ExplainGroup(w, g)
		})
		_, _ = fmt.Fprint(w, "Run \"dupes help <command>\" for the flags of a command.\n")
	}

	flag.Parse()

	// A scan of a whole disk takes a while, and Ctrl-C has to stop it at a
	// point where the database is still consistent: between batches.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	status := cdr.Execute(ctx)
	stop()
	os.Exit(int(status))
}

// usageError is a caller mistake rather than a runtime failure.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

func usagef(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

// exit turns the error of a command into its exit status: 0 for none, 2 for
// a wrong call, 1 for everything else, the codes subcommands uses itself.
func exit(err error) subcommands.ExitStatus {
	if err == nil {
		return subcommands.ExitSuccess
	}
	log.Print(err)
	if ue := (usageError{}); errors.As(err, &ue) {
		return subcommands.ExitUsageError
	}
	return subcommands.ExitFailure
}

// dbFlag adds -db to the flags of a command. Every command has it rather than
// dupes itself, so that it can be written after the command name like any
// other flag of the command.
func dbFlag(f *flag.FlagSet, p *string) {
	f.StringVar(p, "db", defaultDB(), "the database `file`; $DUPES_DB overrides the default")
}

// defaultDB is where the database lives unless -db or $DUPES_DB say
// otherwise: ~/.local/share/dupes, where ai keeps its index too.
func defaultDB() string {
	if p := os.Getenv("DUPES_DB"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "dupes.db"
	}
	return filepath.Join(home, ".local", "share", "dupes", "dupes.db")
}

type versionCmd struct{}

func (*versionCmd) Name() string           { return "version" }
func (*versionCmd) Synopsis() string       { return "print the version" }
func (*versionCmd) Usage() string          { return "version\n" }
func (*versionCmd) SetFlags(*flag.FlagSet) {}

func (*versionCmd) Execute(context.Context, *flag.FlagSet, ...any) subcommands.ExitStatus {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		_, _ = fmt.Fprintln(stdout, "dupes (devel)")
		return subcommands.ExitSuccess
	}
	_, _ = fmt.Fprintf(stdout, "dupes %s\n", bi.Main.Version)
	_, _ = fmt.Fprintf(stdout, "go    %s\n", bi.GoVersion)
	return subcommands.ExitSuccess
}
