// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package stack

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	buckylibs "github.com/ardanlabs/kronk/sdk/tools/bucky/libs"
	"github.com/ardanlabs/kronk/sdk/tools/libs"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/config"
)

// legacy names the commands this one replaced. They are checked for because
// they do not merely duplicate the work: each carried its own build of the
// Kronk SDK and installed native libraries into the same ~/.kronk, so a
// leftover binary on an older SDK will keep pulling that shared bundle back to
// its own version. One stale command is enough to break the other.
var legacy = []string{"ai-ask", "ai-hear", "ai-sum", "ai-tr", "ai-stack"}

type doctorCommand struct {
	ctx context.Context
}

func (c *doctorCommand) Execute(args []string) error {
	if err := noArgs("doctor", args); err != nil {
		return err
	}

	var r report

	fmt.Println("command")
	checkSelf(&r)

	fmt.Println("\nexternal tools")
	checkTools(&r)

	fmt.Println("\nlibraries")
	checkLibs(c.ctx, &r)

	fmt.Println("\nconfig")
	checkConfig(&r)

	fmt.Println("\nmodels")
	checkModels(&r)

	fmt.Println()
	if r.problems > 0 {
		return cli.NotSetupf("%d problem(s) above", r.problems)
	}
	fmt.Println("no problems found")
	return nil
}

// report accumulates findings so every check runs even after one fails. A
// doctor that stops at the first problem makes you run it five times.
type report struct {
	problems int
}

// ok and warn ignore the receiver: a finding that is not a problem changes
// nothing but the terminal. Only bad counts.
func (r *report) ok(format string, args ...any) {
	fmt.Printf("  ok    %s\n", fmt.Sprintf(format, args...))
}

func (r *report) warn(format string, args ...any) {
	fmt.Printf("  warn  %s\n", fmt.Sprintf(format, args...))
}

func (r *report) bad(format string, args ...any) {
	r.problems++
	fmt.Printf("  FAIL  %s\n", fmt.Sprintf(format, args...))
}

// checkSelf reports this build's Kronk version and looks for the commands this
// one replaced.
//
// Under the old layout this check compared five binaries against each other,
// because five modules each pinned their own SDK. There is one module now, so
// the disagreement it hunted for cannot happen — but the binaries it hunted
// through may still be installed, and a stale one still fights over ~/.kronk.
func checkSelf(r *report) {
	if v := cli.KronkVersion(); v != "" {
		r.ok("ai built against kronk %s", v)
	} else {
		r.warn("could not read this build's kronk version")
	}

	var found []string
	for _, name := range legacy {
		if path, err := exec.LookPath(name); err == nil {
			found = append(found, fmt.Sprintf("%s (%s)", name, path))
		}
	}
	if len(found) == 0 {
		r.ok("no superseded ai-* commands in PATH")
		return
	}
	r.warn("superseded commands still in PATH: %s", strings.Join(found, ", "))
	fmt.Println("        they share ~/.kronk and pin their own SDK build; remove them:")
	fmt.Println("        rm $(go env GOBIN)/ai-ask $(go env GOBIN)/ai-hear $(go env GOBIN)/ai-sum \\")
	fmt.Println("           $(go env GOBIN)/ai-tr $(go env GOBIN)/ai-stack")
}

// checkTools verifies the external programs ai reaches for. There is one, and
// it is not required: the SDK's decoder reads WAV, MP3 and FLAC in process and
// shells out to ffmpeg only for a container it cannot open itself -- WebM/Opus,
// MP4/AAC, OGG, M4A. A missing ffmpeg narrows what hear accepts rather than
// breaking it, so it is a warning and not a failure.
//
// ffprobe was checked here too and should not have been. Nothing in ai runs it;
// the chapters and radio commands do, and those are separate binaries.
func checkTools(r *report) {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		r.warn("ffmpeg: not in PATH; ai hear is limited to WAV, MP3 and FLAC")
		return
	}
	r.ok("ffmpeg at %s", path)
}

// checkLibs verifies both native bundles are installed. A missing whisper
// bundle is the quiet failure worth catching: transcription simply does not
// work, and nothing says why until you try.
func checkLibs(ctx context.Context, r *report) {
	if lib, err := libs.New(libs.WithDetect(ctx, cli.Discard)); err != nil {
		r.bad("llama.cpp: %v", err)
	} else if tags := toTags(lib.List()); len(tags) == 0 {
		r.bad("llama.cpp: no bundle installed (run ai stack install)")
	} else {
		r.ok("llama.cpp %s at %s", tags[0].Version, lib.LibsPath())
	}

	if blib, err := buckylibs.New(buckylibs.WithDetect(ctx, cli.Discard)); err != nil {
		r.bad("whisper.cpp: %v", err)
	} else if tags := toTags(blib.List()); len(tags) == 0 {
		r.bad("whisper.cpp: no bundle installed (run ai stack install)")
	} else {
		r.ok("whisper.cpp %s at %s", tags[0].Version, blib.LibsPath())
	}
}

func checkConfig(r *report) {
	if path := config.Path(); path != "" {
		r.ok("reading %s", path)
	} else {
		r.warn("no ~/.config/ai/config; every role falls back to its built-in default")
	}

	repo, ok := config.RepoPath()
	if !ok {
		r.warn("dev-env repository not found; ai stack use will have nothing to edit")
		return
	}
	r.ok("dev-env config at %s", repo)
}

// checkModels verifies every bound role has its model on disk. This is where a
// typo in a model id surfaces, rather than in the middle of a pipeline.
func checkModels(r *report) {
	llama, whisper, err := openStores()
	if err != nil {
		r.bad("%v", err)
		return
	}

	bound := config.BoundRoles()
	if len(bound) == 0 {
		r.bad("no role resolves to a model; check the config")
		return
	}

	inst, err := openInstalled(llama, whisper)
	if err != nil {
		r.bad("%v", err)
		return
	}

	for _, role := range bound {
		if inst.has(role) {
			r.ok("role %s -> %s", role.Name, role.Model())
			continue
		}
		r.bad("role %s -> %s not installed (run ai stack models sync)", role.Name, role.Model())
	}
}
