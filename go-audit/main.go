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
	"strings"
	"syscall"
	"text/tabwriter"
)

// exitFailure is the exit code for a run that went wrong, a check that could
// not run or a wrong call alike. The bash go-audit this replaces used 2 for
// both, and scripts rely on it.
const exitFailure = 2

// command is one subcommand with its own flag set, so that go-audit -h lists
// the commands and go-audit reports -h the flags of reports. Running the
// checks is a command too, the one without a name.
type command struct {
	name  string
	usage string
	short string
	flags *flag.FlagSet
	run   func(ctx context.Context, args []string) error
}

var (
	auditCmd *command
	commands []*command
)

func init() {
	auditCmd = auditCommand()
	commands = []*command{
		reportsCommand(),
		showCommand(),
		webCommand(),
		configCommand(),
		versionCommand(),
	}
}

// stdout is where the commands write their results; a variable so that tests
// can read it.
var stdout io.Writer = os.Stdout

func usage() {
	fmt.Fprintf(os.Stderr, `usage: go-audit %s
       go-audit <command> [options]

Runs the checks of its config, go vet, nilaway, gopls and others, over the Go
module of the current directory and writes one report of them into the store
in the home directory, beside the reports of the go-style Claude Code plugin.
Exit status: 0 nothing found, 1 findings, 2 a check failed.

options:
`, auditUsage)
	auditCmd.flags.SetOutput(os.Stderr)
	auditCmd.flags.PrintDefaults()
	fmt.Fprint(os.Stderr, "\ncommands:\n")
	w := tabwriter.NewWriter(os.Stderr, 0, 0, 2, ' ', 0)
	for _, c := range commands {
		_, _ = fmt.Fprintf(w, "  %s\t%s\n", c.usage, c.short)
	}
	// The usage is the last thing printed before exiting; a failure to print
	// it leaves nothing to do differently.
	_ = w.Flush()
	fmt.Fprint(os.Stderr, `
Run "go-audit <command> -h" for a command's own flags and
"go doc github.com/assanoff/cmd/go-audit@latest" for all of it.
`)
}

func main() {
	log.SetPrefix("go-audit: ")
	log.SetFlags(0)

	cmd, args, err := pickCommand(os.Args[1:])
	if err != nil {
		log.Print(err)
		fmt.Fprintln(os.Stderr, `run "go-audit -h" for usage`)
		os.Exit(exitFailure)
	}
	if cmd == auditCmd {
		cmd.flags.Usage = usage
	} else {
		cmd.flags.Usage = func() {
			fmt.Fprintf(os.Stderr, "usage: go-audit %s\n\n%s\n", cmd.usage, cmd.short)
			if hasFlags(cmd.flags) {
				fmt.Fprint(os.Stderr, "\noptions:\n")
				cmd.flags.PrintDefaults()
			}
		}
	}
	if err := cmd.flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(exitFailure)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = cmd.run(ctx, cmd.flags.Args())
	if code, ok := errors.AsType[exitStatus](err); ok {
		stop()
		os.Exit(int(code))
	}
	if err != nil {
		log.Print(err)
		if _, ok := errors.AsType[usageError](err); ok {
			fmt.Fprintf(os.Stderr, "run \"%s -h\" for usage\n",
				strings.TrimSpace("go-audit "+cmd.name))
		}
		stop()
		os.Exit(exitFailure)
	}
}

// pickCommand returns the command that args call for, and its arguments.
// Without a command name go-audit runs the checks; a first argument that
// looks like a name and is none is a mistake rather than a package, as no
// one audits the standard library.
func pickCommand(args []string) (*command, []string, error) {
	if len(args) == 0 {
		return auditCmd, args, nil
	}
	if c := lookup(args[0]); c != nil {
		return c, args[1:], nil
	}
	name := args[0]
	if !strings.HasPrefix(name, "-") && !strings.ContainsAny(name, `./\`) {
		return nil, nil, fmt.Errorf("unknown command %q; packages are paths, such as ./%s",
			name, name)
	}
	return auditCmd, args, nil
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
