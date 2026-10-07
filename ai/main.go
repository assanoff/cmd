// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/jessevdk/go-flags"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/cmd/ask"
	"github.com/assanoff/cmd/ai/internal/cmd/hear"
	"github.com/assanoff/cmd/ai/internal/cmd/stack"
	"github.com/assanoff/cmd/ai/internal/cmd/sum"
	"github.com/assanoff/cmd/ai/internal/cmd/tr"
)

// options are the flags that belong to no subcommand.
type options struct {
	Version bool `long:"version" description:"print the ai and Kronk SDK versions"`
}

func main() {
	log.SetPrefix("ai: ")
	log.SetFlags(0)

	// Generation, downloads and a fold over a long document are all long and
	// interruptible, so Ctrl-C has to reach them: the alternative is a process
	// killed with a model still loaded. go-flags calls Execute without a
	// context of its own, so each subcommand takes this one at construction.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(run(ctx, os.Args[1:]))
}

func run(ctx context.Context, args []string) int {
	var opts options

	// PrintErrors is deliberately not set. go-flags would write every error it
	// saw, including the ones a subcommand's Execute returned, and those are
	// reported below with the program's own prefix — leaving it on printed
	// each of them twice.
	parser := flags.NewNamedParser("ai", flags.HelpFlag|flags.PassDoubleDash)
	parser.LongDescription = description

	// Without a subcommand "ai --version" is still a legitimate call, so a
	// missing one is not an error here; it is answered with the help text
	// below.
	parser.SubcommandsOptional = true

	if _, err := parser.AddGroup("Application Options", "", &opts); err != nil {
		return fatal(err)
	}
	for _, c := range subcommands(ctx) {
		if _, err := parser.AddCommand(c.name, c.short, c.long, c.data); err != nil {
			return fatal(err)
		}
	}

	rest, err := parser.ParseArgs(args)
	if err != nil {
		var fe *flags.Error
		if errors.As(err, &fe) {
			// Asking for help is a success, and the help itself is the output
			// the caller wanted, so it goes to standard output.
			if fe.Type == flags.ErrHelp {
				fmt.Println(fe.Message)
				return 0
			}
			log.Print(fe.Message)
			fmt.Fprintf(os.Stderr, "ai: run %q for the options\n", helpFor(parser))
			return cli.ExitUsage
		}
		// Anything else came out of a subcommand's Execute.
		log.Print(err)
		return cli.ExitCode(err)
	}

	// Nothing ran: either --version, or no subcommand at all.
	if parser.Active == nil {
		switch {
		case opts.Version:
			cli.PrintVersion()
			return 0
		case len(rest) > 0:
			fmt.Fprintf(os.Stderr, "ai: unknown command %q\n\n", rest[0])
			parser.WriteHelp(os.Stderr)
			return cli.ExitUsage
		default:
			parser.WriteHelp(os.Stderr)
			return cli.ExitUsage
		}
	}
	return 0
}

// subcommand is one entry in the dispatch table. Keeping the table here rather
// than in struct tags is what lets each subcommand be built with the run's
// context.
type subcommand struct {
	name  string
	short string
	long  string
	data  any
}

func subcommands(ctx context.Context) []subcommand {
	return []subcommand{
		{"hear", hear.Short, hear.Long, hear.New(ctx)},
		{"ask", ask.Short, ask.Long, ask.New(ctx)},
		{"sum", sum.Short, sum.Long, sum.New(ctx)},
		{"tr", tr.Short, tr.Long, tr.New(ctx)},
		{"stack", stack.Short, stack.Long, stack.New(ctx)},
		// A pointer, like every other entry: go-flags scans the struct by
		// reflection and refuses a value.
		{"version", "Print the ai and Kronk SDK versions", versionLong, &versionCommand{}},
	}
}

// versionCommand answers "ai version" as well as "ai --version". Both spellings
// exist because the commands this replaced accepted both, and a version check
// is the one thing people type from memory.
type versionCommand struct{}

func (*versionCommand) Execute([]string) error {
	cli.PrintVersion()
	return nil
}

const versionLong = `Prints the version this binary was built from, the Kronk SDK
it was built against, and the Go toolchain that built it.

The Kronk line is the one that matters when something stops loading: the native
libraries under ~/.kronk are shared with the kronk command and the Kronk model
server, and Kronk, its yzma binding and llama.cpp are only tested together.`

// helpFor names the -h that would explain the mistake just made. A bad flag of
// "ai sum" is explained by "ai sum -h", not by the top-level help, and the
// parser already knows which subcommand it had reached.
func helpFor(parser *flags.Parser) string {
	name := "ai"
	for c := parser.Active; c != nil; c = c.Active {
		name += " " + c.Name
	}
	return name + " -h"
}

func fatal(err error) int {
	log.Print(err)
	return cli.ExitError
}

const description = `
ai runs language and speech models locally, through the Kronk SDK: llama.cpp
for text and whisper.cpp for speech. There is no server and no Python — the
SDK downloads the native libraries and model files on first use and runs
inference in this process.

Every subcommand reads standard input and writes standard output when given no
file arguments, so they compose:

    ai hear lecture.mkv | ai tr -t en | ai sum -s brief

Given file or directory arguments they process those instead, loading the
model once for the whole batch. Roles (fast, smart, asr, ...) map to concrete
model names through ~/.config/ai/config, so no subcommand hard-codes a model.

Exit status is 0 on success, 1 on a failure, 2 on a usage error, and 3 when
the llama.cpp or whisper.cpp libraries or the requested model are not
installed. "ai stack doctor" explains a 3.`
