// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/ardanlabs/kronk/sdk/applog"
	buckylibs "github.com/ardanlabs/kronk/sdk/tools/bucky/libs"
	buckymodels "github.com/ardanlabs/kronk/sdk/tools/bucky/models"
	"github.com/ardanlabs/kronk/sdk/tools/libs"
	"github.com/ardanlabs/kronk/sdk/tools/models"
)

// The two backends have separate library bundles and separate model stores,
// and mixing them up is the classic mistake here: KRONK_LIB_PATH does not
// select whisper.cpp, and a whisper model is a GGML .bin rather than a GGUF.
// Everything below keeps them explicitly apart.

func installCommand() *command {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.BoolVar(flagQuiet, "q", false, "suppress progress on standard error")
	noModels := fs.Bool("libs-only", false, "install the native libraries but no models")

	return &command{
		name:  "install",
		usage: "install [-libs-only] [-q]",
		short: "Install the native libraries and every model the roles name",
		flags: fs,
		run: func(ctx context.Context, args []string) error {
			if len(args) > 0 {
				return usagef("install takes no arguments")
			}
			if err := installLibs(ctx); err != nil {
				return err
			}
			if *noModels {
				return nil
			}
			return syncModels(ctx)
		},
	}
}

func updateCommand() *command {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.BoolVar(flagQuiet, "q", false, "suppress progress on standard error")

	return &command{
		name:  "update",
		usage: "update [-q]",
		short: "Bring the native libraries to the versions this build was tested with",
		flags: fs,
		run: func(ctx context.Context, args []string) error {
			if len(args) > 0 {
				return usagef("update takes no arguments")
			}
			if err := installLibs(ctx); err != nil {
				return err
			}

			// Deliberately no "track the latest llama.cpp" option. Kronk, its
			// yzma binding and llama.cpp are a tested set; upgrading one
			// member alone is how model loading breaks. The pinned version is
			// whatever this binary was built against, which is why updating
			// the binaries comes first.
			fmt.Fprint(os.Stderr, `
ai-stack: libraries now match this build of ai-stack.
ai-stack: to move the whole set forward, update the commands first:
ai-stack:     gup update ai-hear ai-ask ai-sum ai-tr ai-stack
ai-stack: then run ai-stack update again, and ai-stack doctor to confirm.
`)
			return nil
		},
	}
}

// installLibs installs both native bundles: llama.cpp for text models and
// whisper.cpp for speech. Each is a no-op when already at the pinned version.
func installLibs(ctx context.Context) error {
	log := logger()

	lib, err := libs.New(libs.WithDetect(ctx, log))
	if err != nil {
		return fmt.Errorf("%w: selecting llama.cpp libraries: %w", errNotSetup, err)
	}
	llama, err := lib.Download(ctx, log)
	if err != nil {
		return fmt.Errorf("%w: installing llama.cpp libraries: %w", errNotSetup, err)
	}
	fmt.Printf("llama.cpp   %s  %s/%s/%s\n", llama.Version, llama.OS, llama.Arch, llama.Processor)

	blib, err := buckylibs.New(buckylibs.WithDetect(ctx, log))
	if err != nil {
		return fmt.Errorf("%w: selecting whisper.cpp libraries: %w", errNotSetup, err)
	}
	whisper, err := blib.Download(ctx, log)
	if err != nil {
		return fmt.Errorf("%w: installing whisper.cpp libraries: %w", errNotSetup, err)
	}
	fmt.Printf("whisper.cpp %s  %s/%s/%s\n", whisper.Version, whisper.OS, whisper.Arch, whisper.Processor)

	return nil
}

// syncModels downloads whatever a bound role names and is not on disk yet.
// Already-present models cost one index lookup, so this is safe to re-run.
func syncModels(ctx context.Context) error {
	log := logger()

	llama, whisper, err := openStores()
	if err != nil {
		return err
	}

	var missing int
	for _, r := range boundRoles() {
		name := r.model()

		installed, err := roleInstalled(r, llama, whisper)
		if err != nil {
			return err
		}
		if installed {
			fmt.Printf("%-9s %-42s ok\n", r.name, name)
			continue
		}

		missing++
		fmt.Printf("%-9s %-42s downloading\n", r.name, name)

		switch r.backend {
		case backendWhisper:
			if _, err := whisper.Download(ctx, log, name); err != nil {
				return fmt.Errorf("%w: role %s: %w", errNotSetup, r.name, err)
			}
		default:
			if _, err := llama.Download(ctx, log, name); err != nil {
				return fmt.Errorf("%w: role %s: %w", errNotSetup, r.name, err)
			}
		}
	}

	if missing == 0 {
		fmt.Println("every role already has its model")
	}
	return nil
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

// roleInstalled reports whether the model a role names is on disk.
func roleInstalled(r role, llama *models.Models, whisper *buckymodels.Models) (bool, error) {
	name := r.model()
	if name == "" {
		return false, nil
	}

	if r.backend == backendWhisper {
		files, err := whisper.Files()
		if err != nil {
			return false, fmt.Errorf("listing whisper models: %w", err)
		}
		for _, f := range files {
			if f.ID == name {
				return true, nil
			}
		}
		return false, nil
	}

	downloaded, _ := llama.IndexState()
	return downloaded[name], nil
}
