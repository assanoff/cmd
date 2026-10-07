// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package stack installs and maintains the local model stack the other
// subcommands run on.
//
// It is the one subcommand that needs the whole role table: the others each
// know their own roles, while this one installs, checks and rebinds all of
// them.
package stack

import (
	"context"
	"fmt"

	"github.com/ardanlabs/kronk/sdk/applog"
	buckymodels "github.com/ardanlabs/kronk/sdk/tools/bucky/models"
	"github.com/ardanlabs/kronk/sdk/tools/models"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/config"
)

// Name is how this subcommand labels its own progress.
const Name = "ai stack"

const (
	Short = "Install and maintain the local model stack"
	Long  = `Installs and maintains the native libraries and models the other
subcommands run on.

Run "ai stack <command> -h" for a command's own flags.`
)

// Command is the subcommand group. go-flags reads the command tags below and
// builds the nested subcommands from them; the pointers are filled in by New
// so each one carries the run's context.
type Command struct {
	Install *installCommand `command:"install" description:"Install the native libraries and every model the roles name"`
	Update  *updateCommand  `command:"update" description:"Bring the native libraries to the versions this build was tested with"`
	Status  *statusCommand  `command:"status" description:"Show the installed libraries, models, roles and disk use"`
	Doctor  *doctorCommand  `command:"doctor" description:"Check everything the ai subcommands need, end to end"`
	Models  *modelsCommand  `command:"models" subcommands-optional:"yes" description:"List installed models, download what the roles need, remove what they do not"`
	Use     *useCommand     `command:"use" description:"Point a role at a different model"`
}

// New returns the group with every nested subcommand bound to the run's
// context.
func New(ctx context.Context) *Command {
	return &Command{
		Install: &installCommand{ctx: ctx},
		Update:  &updateCommand{ctx: ctx},
		Status:  &statusCommand{ctx: ctx},
		Doctor:  &doctorCommand{ctx: ctx},
		Models:  newModelsCommand(ctx),
		Use:     &useCommand{},
	}
}

// openStores opens both model stores and refreshes their indexes, so a model
// dropped in or deleted out of band is seen.
func openStores() (*models.Models, *buckymodels.Models, error) {
	llama, err := models.New()
	if err != nil {
		return nil, nil, fmt.Errorf("opening the model store: %w", err)
	}
	whisper, err := buckymodels.New()
	if err != nil {
		return nil, nil, fmt.Errorf("opening the whisper model store: %w", err)
	}
	if err := whisper.BuildIndex(applog.DiscardLogger, false); err != nil {
		return nil, nil, fmt.Errorf("indexing whisper models: %w", err)
	}
	return llama, whisper, nil
}

// installed is the set of models on disk, for each backend.
//
// It is built once per command rather than consulted once per role: both
// lookups behind it enumerate a store holding multi-gigabyte files, and the
// answer is the same for every role in the table.
type installed struct {
	llama   map[string]bool
	whisper map[string]bool
}

func openInstalled(llama *models.Models, whisper *buckymodels.Models) (installed, error) {
	files, err := whisper.Files()
	if err != nil {
		return installed{}, fmt.Errorf("listing whisper models: %w", err)
	}
	w := make(map[string]bool, len(files))
	for _, f := range files {
		w[f.ID] = true
	}

	downloaded, _ := llama.IndexState()
	return installed{llama: downloaded, whisper: w}, nil
}

// has reports whether the model a role names is on disk.
func (i installed) has(r config.Role) bool {
	name := r.Model()
	if name == "" {
		return false
	}
	if r.Backend == config.BackendWhisper {
		return i.whisper[name]
	}
	return i.llama[name]
}

// noArgs rejects the stray words a command that takes none was given.
func noArgs(name string, args []string) error {
	if len(args) > 0 {
		return cli.Usagef("%s takes no arguments", name)
	}
	return nil
}

// claimedModels maps a model id to whatever holds it: the roles pointing at it,
// or "keep" for a model named by AI_MODELS_KEEP. A model can be held by more
// than one role, which is why gc has to look at the whole set rather than one
// role at a time.
func claimedModels() map[string]string {
	claimed := map[string]string{}
	for _, r := range config.BoundRoles() {
		name := r.Model()
		if existing := claimed[name]; existing != "" {
			claimed[name] = existing + "," + r.Name
			continue
		}
		claimed[name] = r.Name
	}
	// The keep list comes second and never overwrites a role: "smart" says
	// more about why a model is on disk than "keep" does.
	for _, name := range config.KeptModels() {
		if claimed[name] == "" {
			claimed[name] = "keep"
		}
	}
	return claimed
}

// humanSize renders a byte count the way a person reads one.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PiB", value)
}

// printer is the progress sink the install paths share. Each subcommand that
// can download accepts -q and builds one.
func printer(quiet bool) *cli.Printer { return cli.NewPrinter(Name, quiet) }
