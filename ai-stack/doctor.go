// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"os/exec"
	"runtime/debug"
	"sort"
	"strings"

	buckylibs "github.com/ardanlabs/kronk/sdk/tools/bucky/libs"
	"github.com/ardanlabs/kronk/sdk/tools/libs"
)

// siblings are the commands that share ~/.kronk with this one.
var siblings = []string{"ai-hear", "ai-ask", "ai-sum", "ai-tr"}

func doctorCommand() *command {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)

	return &command{
		name:  "doctor",
		usage: "doctor",
		short: "Check everything the ai-* commands need, end to end",
		flags: fs,
		run: func(ctx context.Context, args []string) error {
			if len(args) > 0 {
				return usagef("doctor takes no arguments")
			}
			return doctor(ctx)
		},
	}
}

// report accumulates findings so every check runs even after one fails. A
// doctor that stops at the first problem makes you run it five times.
type report struct {
	problems int
}

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

func doctor(ctx context.Context) error {
	var r report

	fmt.Println("commands")
	checkSiblings(&r)

	fmt.Println("\nexternal tools")
	checkTools(&r)

	fmt.Println("\nlibraries")
	checkLibs(ctx, &r)

	fmt.Println("\nconfig")
	checkConfig(&r)

	fmt.Println("\nmodels")
	checkModels(&r)

	fmt.Println()
	if r.problems > 0 {
		return fmt.Errorf("%w: %d problem(s) above", errNotSetup, r.problems)
	}
	fmt.Println("no problems found")
	return nil
}

// checkSiblings verifies the other commands are installed and, more
// importantly, that they were all built against the same Kronk. They share one
// ~/.kronk for native libraries, and Kronk, yzma and llama.cpp are only tested
// as a set — two commands on different Kronk versions will fight over the
// bundle they each want installed there.
func checkSiblings(r *report) {
	versions := map[string][]string{}

	for _, name := range siblings {
		path, err := exec.LookPath(name)
		if err != nil {
			r.bad("%s: not in PATH (go install github.com/assanoff/cmd/%s@latest)", name, name)
			continue
		}

		out, err := exec.Command(path, "-version").Output()
		if err != nil {
			r.bad("%s: -version failed: %v", name, err)
			continue
		}
		v := kronkVersion(string(out))
		if v == "" {
			r.warn("%s: could not read its Kronk version", name)
			continue
		}
		versions[v] = append(versions[v], name)
		r.ok("%s built against kronk %s", name, v)
	}

	self := selfKronkVersion()
	if self != "" {
		versions[self] = append(versions[self], "ai-stack")
		r.ok("ai-stack built against kronk %s", self)
	}

	if len(versions) > 1 {
		var lines []string
		for v, names := range versions {
			sort.Strings(names)
			lines = append(lines, fmt.Sprintf("%s: %s", v, strings.Join(names, " ")))
		}
		sort.Strings(lines)
		r.bad("commands disagree on the Kronk version (%s)", strings.Join(lines, "; "))
		fmt.Println("        they share ~/.kronk for native libraries; rebuild them together:")
		fmt.Println("        gup update ai-hear ai-ask ai-sum ai-tr ai-stack")
	}
}

// kronkVersion pulls the Kronk line out of a sibling's -version output.
func kronkVersion(out string) string {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "kronk" {
			return fields[1]
		}
	}
	return ""
}

// selfKronkVersion is this binary's own Kronk version, read the same way the
// siblings report theirs.
func selfKronkVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return moduleVersion(bi, "github.com/ardanlabs/kronk")
}

// checkTools verifies the external programs the commands shell out to. ffmpeg
// is not optional: ai-hear decodes anything that is not a plain WAV through it.
func checkTools(r *report) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if path, err := exec.LookPath(tool); err != nil {
			r.bad("%s: not in PATH (ai-hear needs it for anything but plain WAV)", tool)
		} else {
			r.ok("%s at %s", tool, path)
		}
	}
}

// checkLibs verifies both native bundles are installed. A missing whisper
// bundle is the quiet failure worth catching: transcription simply does not
// work, and nothing says why until you try.
func checkLibs(ctx context.Context, r *report) {
	if lib, err := libs.New(libs.WithDetect(ctx, silent)); err != nil {
		r.bad("llama.cpp: %v", err)
	} else if tags := toTags(lib.List()); len(tags) == 0 {
		r.bad("llama.cpp: no bundle installed (run ai-stack install)")
	} else {
		r.ok("llama.cpp %s at %s", tags[0].version, lib.LibsPath())
	}

	if blib, err := buckylibs.New(buckylibs.WithDetect(ctx, silent)); err != nil {
		r.bad("whisper.cpp: %v", err)
	} else if tags := toTags(blib.List()); len(tags) == 0 {
		r.bad("whisper.cpp: no bundle installed (run ai-stack install)")
	} else {
		r.ok("whisper.cpp %s at %s", tags[0].version, blib.LibsPath())
	}
}

func checkConfig(r *report) {
	if path := deployedConfig(); path != "" {
		r.ok("reading %s", path)
	} else {
		r.warn("no ~/.config/ai/config; every role falls back to its built-in default")
	}

	repo, ok := repoConfig()
	if !ok {
		r.warn("dev-env repository not found; ai-stack use will have nothing to edit")
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

	bound := boundRoles()
	if len(bound) == 0 {
		r.bad("no role resolves to a model; check the config")
		return
	}

	for _, role := range bound {
		installed, err := roleInstalled(role, llama, whisper)
		switch {
		case err != nil:
			r.bad("role %s: %v", role.name, err)
		case installed:
			r.ok("role %s -> %s", role.name, role.model())
		default:
			r.bad("role %s -> %s not installed (run ai-stack models sync)", role.name, role.model())
		}
	}
}
