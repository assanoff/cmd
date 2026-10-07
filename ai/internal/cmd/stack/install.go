// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package stack

import (
	"context"
	"fmt"
	"os"

	buckylibs "github.com/ardanlabs/kronk/sdk/tools/bucky/libs"
	"github.com/ardanlabs/kronk/sdk/tools/libs"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/config"
)

type installCommand struct {
	ctx context.Context

	LibsOnly bool `long:"libs-only" description:"install the native libraries but no models"`
	Quiet    bool `short:"q" long:"quiet" description:"suppress progress on standard error"`
}

func (c *installCommand) Execute(args []string) error {
	if err := noArgs("install", args); err != nil {
		return err
	}
	p := printer(c.Quiet)

	if err := installLibs(c.ctx, p); err != nil {
		return err
	}
	if c.LibsOnly {
		return nil
	}
	return syncModels(c.ctx, p)
}

type updateCommand struct {
	ctx context.Context

	Quiet bool `short:"q" long:"quiet" description:"suppress progress on standard error"`
}

func (c *updateCommand) Execute(args []string) error {
	if err := noArgs("update", args); err != nil {
		return err
	}
	if err := installLibs(c.ctx, printer(c.Quiet)); err != nil {
		return err
	}

	// Deliberately no "track the latest llama.cpp" option. Kronk, its yzma
	// binding and llama.cpp are a tested set; upgrading one member alone is
	// how model loading breaks. The pinned version is whatever this binary was
	// built against, which is why updating the binary comes first.
	fmt.Fprint(os.Stderr, `
ai stack: libraries now match this build of ai.
ai stack: to move the whole set forward, update the command first:
ai stack:     go install github.com/assanoff/cmd/ai@latest
ai stack: then run ai stack update again, and ai stack doctor to confirm.
`)
	return nil
}

// installLibs installs both native bundles: llama.cpp for text models and
// whisper.cpp for speech. Each is a no-op when already at the pinned version.
func installLibs(ctx context.Context, p *cli.Printer) error {
	log := p.Logger()

	lib, err := libs.New(libs.WithDetect(ctx, log))
	if err != nil {
		return cli.NotSetupf("selecting llama.cpp libraries: %w", err)
	}
	llama, err := lib.Download(ctx, log)
	if err != nil {
		return cli.NotSetupf("installing llama.cpp libraries: %w", err)
	}
	fmt.Printf("llama.cpp   %s  %s/%s/%s\n", llama.Version, llama.OS, llama.Arch, llama.Processor)

	blib, err := buckylibs.New(buckylibs.WithDetect(ctx, log))
	if err != nil {
		return cli.NotSetupf("selecting whisper.cpp libraries: %w", err)
	}
	whisper, err := blib.Download(ctx, log)
	if err != nil {
		return cli.NotSetupf("installing whisper.cpp libraries: %w", err)
	}
	fmt.Printf("whisper.cpp %s  %s/%s/%s\n", whisper.Version, whisper.OS, whisper.Arch, whisper.Processor)

	return nil
}

// syncModels downloads whatever a bound role names and is not on disk yet.
// Already-present models cost one index lookup, so this is safe to re-run.
func syncModels(ctx context.Context, p *cli.Printer) error {
	log := p.Logger()

	llama, whisper, err := openStores()
	if err != nil {
		return err
	}

	inst, err := openInstalled(llama, whisper)
	if err != nil {
		return err
	}

	var missing int
	for _, r := range config.BoundRoles() {
		name := r.Model()

		if inst.has(r) {
			fmt.Printf("%-9s %-42s ok\n", r.Name, name)
			continue
		}

		missing++
		fmt.Printf("%-9s %-42s downloading\n", r.Name, name)

		switch r.Backend {
		case config.BackendWhisper:
			if _, err := whisper.Download(ctx, log, name); err != nil {
				return cli.NotSetupf("role %s: %w", r.Name, err)
			}
		default:
			if _, err := llama.Download(ctx, log, name); err != nil {
				return cli.NotSetupf("role %s: %w", r.Name, err)
			}
		}
	}

	if missing == 0 {
		fmt.Println("every role already has its model")
	}
	return nil
}
