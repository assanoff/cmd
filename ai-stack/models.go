// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/ardanlabs/kronk/sdk/applog"
)

func modelsCommand() *command {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	fs.BoolVar(flagQuiet, "q", false, "suppress progress on standard error")
	yes := fs.Bool("y", false, "for gc: delete without asking")

	return &command{
		name:  "models",
		usage: "models [sync|gc] [-y] [-q]",
		short: "List installed models, download what the roles need, remove what they do not",
		flags: fs,
		run: func(ctx context.Context, args []string) error {
			switch {
			case len(args) == 0:
				return listModels()
			case len(args) > 1:
				return usagef("models takes at most one argument")
			}

			switch args[0] {
			case "sync":
				return syncModels(ctx)
			case "gc":
				return gcModels(ctx, *yes)
			default:
				return usagef("unknown models argument %q: want sync or gc", args[0])
			}
		},
	}
}

// listModels shows every installed model and which role, if any, claims it.
func listModels() error {
	llama, whisper, err := openStores()
	if err != nil {
		return err
	}

	claimed := claimedModels()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()

	fmt.Fprintln(w, "BACKEND\tMODEL\tSIZE\tROLE")

	llamaFiles, err := llama.Files()
	if err != nil {
		return fmt.Errorf("listing models: %w", err)
	}
	for _, f := range llamaFiles {
		id := f.OwnedBy + "/" + f.ID
		fmt.Fprintf(w, "llama\t%s\t%s\t%s\n", id, humanSize(f.Size), claimed[id])
	}

	whisperFiles, err := whisper.Files()
	if err != nil {
		return fmt.Errorf("listing whisper models: %w", err)
	}
	for _, f := range whisperFiles {
		fmt.Fprintf(w, "whisper\t%s\t%s\t%s\n", f.ID, humanSize(f.Size), claimed[f.ID])
	}

	return nil
}

// claimedModels maps a model id to the roles pointing at it. A model can be
// claimed by more than one role, which is why gc has to look at the whole set
// rather than one role at a time.
func claimedModels() map[string]string {
	claimed := map[string]string{}
	for _, r := range boundRoles() {
		name := r.model()
		if existing := claimed[name]; existing != "" {
			claimed[name] = existing + "," + r.name
			continue
		}
		claimed[name] = r.name
	}
	return claimed
}

// gcModels removes installed models no role points at. It asks first, because
// a model is a multi-gigabyte download and the roles could simply be
// misconfigured — deleting on that basis would be the wrong kind of helpful.
func gcModels(ctx context.Context, yes bool) error {
	llama, whisper, err := openStores()
	if err != nil {
		return err
	}

	claimed := claimedModels()

	type victim struct {
		backend backend
		id      string
		size    int64
	}
	var victims []victim
	var total int64

	llamaFiles, err := llama.Files()
	if err != nil {
		return fmt.Errorf("listing models: %w", err)
	}
	for _, f := range llamaFiles {
		id := f.OwnedBy + "/" + f.ID
		if claimed[id] == "" {
			victims = append(victims, victim{backendLlama, id, f.Size})
			total += f.Size
		}
	}

	whisperFiles, err := whisper.Files()
	if err != nil {
		return fmt.Errorf("listing whisper models: %w", err)
	}
	for _, f := range whisperFiles {
		if claimed[f.ID] == "" {
			victims = append(victims, victim{backendWhisper, f.ID, f.Size})
			total += f.Size
		}
	}

	if len(victims) == 0 {
		fmt.Println("nothing to remove: every installed model is claimed by a role")
		return nil
	}

	for _, v := range victims {
		fmt.Printf("  %-8s %-42s %s\n", v.backend, v.id, humanSize(v.size))
	}
	fmt.Printf("%d model(s), %s\n", len(victims), humanSize(total))

	if !yes && !confirm("remove them?") {
		fmt.Println("nothing removed")
		return nil
	}

	log := logger()
	for _, v := range victims {
		if v.backend == backendWhisper {
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
