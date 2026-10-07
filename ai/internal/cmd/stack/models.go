// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package stack

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/ardanlabs/kronk/sdk/applog"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/config"
)

// modelsCommand lists installed models when given no subcommand of its own.
//
// Under the flag package this was "ai-stack models [sync|gc]", with the verb
// as a positional argument and a hand-written check for flags typed after it
// ("models gc -y" silently left -y unset). go-flags makes sync and gc real
// subcommands, which is what makes "ai stack models gc -y" parse the way it
// reads.
type modelsCommand struct {
	Sync *modelsSyncCommand `command:"sync" description:"Download whatever a bound role names and is not on disk yet"`
	GC   *modelsGCCommand   `command:"gc" description:"Remove installed models no role and no keep list claims"`
}

func newModelsCommand(ctx context.Context) *modelsCommand {
	return &modelsCommand{
		Sync: &modelsSyncCommand{ctx: ctx},
		GC:   &modelsGCCommand{ctx: ctx},
	}
}

func (c *modelsCommand) Execute(args []string) error {
	if len(args) > 0 {
		return cli.Usagef("unknown models argument %q: want sync or gc", args[0])
	}
	return listModels()
}

type modelsSyncCommand struct {
	ctx context.Context

	Quiet bool `short:"q" long:"quiet" description:"suppress progress on standard error"`
}

func (c *modelsSyncCommand) Execute(args []string) error {
	if err := noArgs("sync", args); err != nil {
		return err
	}
	return syncModels(c.ctx, printer(c.Quiet))
}

type modelsGCCommand struct {
	ctx context.Context

	Yes    bool `short:"y" long:"yes" description:"delete without asking"`
	DryRun bool `short:"n" long:"dry-run" description:"list what would be removed, delete nothing"`
	Quiet  bool `short:"q" long:"quiet" description:"suppress progress on standard error"`
}

func (c *modelsGCCommand) Execute(args []string) error {
	if err := noArgs("gc", args); err != nil {
		return err
	}
	return gcModels(c.ctx, printer(c.Quiet), c.Yes, c.DryRun)
}

// listModels shows every installed model and which role, if any, claims it.
func listModels() error {
	llama, whisper, err := openStores()
	if err != nil {
		return err
	}

	claimed := claimedModels()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer func() { _ = w.Flush() }()

	_, _ = fmt.Fprintln(w, "BACKEND\tMODEL\tSIZE\tCLAIMED BY")

	llamaFiles, err := llama.Files()
	if err != nil {
		return fmt.Errorf("listing models: %w", err)
	}
	for _, f := range llamaFiles {
		id := f.OwnedBy + "/" + f.ID
		_, _ = fmt.Fprintf(w, "llama\t%s\t%s\t%s\n", id, humanSize(f.Size), claimed[id])
	}

	whisperFiles, err := whisper.Files()
	if err != nil {
		return fmt.Errorf("listing whisper models: %w", err)
	}
	for _, f := range whisperFiles {
		_, _ = fmt.Fprintf(w, "whisper\t%s\t%s\t%s\n", f.ID, humanSize(f.Size), claimed[f.ID])
	}

	return nil
}

// gcModels removes installed models nothing claims. It asks first, because a
// model is a multi-gigabyte download and the roles could simply be
// misconfigured — deleting on that basis would be the wrong kind of helpful.
//
// "Claimed" is a narrow word here: a bound role, or a name in AI_MODELS_KEEP.
// A caller that talks to the Kronk server directly — an editor, a one-off
// script — asks for a model by name and never touches a role, so its models
// are invisible to this function. The keep list is how those are protected,
// and it holds even under -y.
func gcModels(ctx context.Context, p *cli.Printer, yes, dry bool) error {
	// Without a config file the roles are only their built-in defaults, and
	// everything else on disk looks like garbage. That is a guess, not
	// knowledge, and not one to delete gigabytes on.
	if config.Path() == "" {
		return fmt.Errorf("no config file found: gc has nothing to call claimed beyond the built-in defaults")
	}

	llama, whisper, err := openStores()
	if err != nil {
		return err
	}

	claimed := claimedModels()

	type victim struct {
		backend config.Backend
		id      string
		size    int64
	}
	var (
		victims []victim
		total   int64
	)

	llamaFiles, err := llama.Files()
	if err != nil {
		return fmt.Errorf("listing models: %w", err)
	}
	for _, f := range llamaFiles {
		id := f.OwnedBy + "/" + f.ID
		if claimed[id] == "" {
			victims = append(victims, victim{config.BackendLlama, id, f.Size})
			total += f.Size
		}
	}

	whisperFiles, err := whisper.Files()
	if err != nil {
		return fmt.Errorf("listing whisper models: %w", err)
	}
	for _, f := range whisperFiles {
		if claimed[f.ID] == "" {
			victims = append(victims, victim{config.BackendWhisper, f.ID, f.Size})
			total += f.Size
		}
	}

	if len(victims) == 0 {
		fmt.Println("nothing to remove: every installed model is claimed")
		return nil
	}

	for _, v := range victims {
		fmt.Printf("  %-8s %-42s %s\n", v.backend, v.id, humanSize(v.size))
	}
	fmt.Printf("%d model(s), %s\n", len(victims), humanSize(total))

	if dry {
		fmt.Println("dry run: nothing removed")
		return nil
	}

	if !yes && !confirm("remove them?") {
		fmt.Println("nothing removed")
		return nil
	}

	log := p.Logger()
	for _, v := range victims {
		if v.backend == config.BackendWhisper {
			mp, err := whisper.FullPath(v.id)
			if err != nil {
				return fmt.Errorf("locating %s: %w", v.id, err)
			}
			if err := whisper.Remove(mp, log); err != nil {
				return fmt.Errorf("removing %s: %w", v.id, err)
			}
		} else if err := llama.RemoveCatalogEntry(ctx, v.id, applog.DiscardLogger); err != nil {
			return fmt.Errorf("removing %s: %w", v.id, err)
		}
		fmt.Printf("removed %s\n", v.id)
	}

	return nil
}

// confirm asks on the terminal. A closed or non-interactive standard input
// answers no: an unattended run must not delete models because nobody was
// there to object.
func confirm(question string) bool {
	fmt.Printf("%s [y/N] ", question)

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		fmt.Println()
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
