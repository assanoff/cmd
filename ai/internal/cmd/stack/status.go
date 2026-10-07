// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package stack

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	buckylibs "github.com/ardanlabs/kronk/sdk/tools/bucky/libs"
	"github.com/ardanlabs/kronk/sdk/tools/defaults"
	"github.com/ardanlabs/kronk/sdk/tools/libs"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/core/config"
)

type statusCommand struct {
	ctx context.Context
}

func (c *statusCommand) Execute(args []string) error {
	if len(args) > 0 {
		return cli.Usagef("status takes no arguments")
	}

	base := defaults.BaseDir("")
	fmt.Printf("base path  %s\n", base)
	if path := config.Path(); path != "" {
		fmt.Printf("config     %s\n", path)
	} else {
		fmt.Printf("config     (none; using built-in defaults)\n")
	}
	if path, ok := config.RepoPath(); ok {
		fmt.Printf("repo       %s\n", path)
	}

	fmt.Println("\nlibraries")
	printLibs(c.ctx)

	fmt.Println("\nroles")
	if err := printRoles(); err != nil {
		return err
	}

	fmt.Println("\ndisk")
	printDisk(base)

	return nil
}

// printLibs lists the installed bundles for both backends. A missing bundle is
// reported rather than treated as an error: reporting is this command's whole
// job, and doctor is the one that decides whether it is a problem.
func printLibs(ctx context.Context) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer func() { _ = w.Flush() }()

	if lib, err := libs.New(libs.WithDetect(ctx, cli.Discard)); err != nil {
		_, _ = fmt.Fprintf(w, "  llama.cpp\t(unavailable: %v)\n", err)
	} else {
		printInstalls(w, "llama.cpp", lib.LibsPath(), toTags(lib.List()))
	}

	if blib, err := buckylibs.New(buckylibs.WithDetect(ctx, cli.Discard)); err != nil {
		_, _ = fmt.Fprintf(w, "  whisper.cpp\t(unavailable: %v)\n", err)
	} else {
		printInstalls(w, "whisper.cpp", blib.LibsPath(), toTags(blib.List()))
	}
}

// tag is the shape both library managers report, reduced to what status shows.
type tag struct {
	version   string
	os        string
	arch      string
	processor string
}

// toTags takes List's two results directly. Both library managers alias the
// same underlying VersionTag, so one converter serves both backends. A listing
// error is folded into an empty list: status reports what it can see and lets
// doctor be the one that complains.
func toTags(tags []libs.VersionTag, err error) []tag {
	if err != nil {
		return nil
	}
	out := make([]tag, 0, len(tags))
	for _, t := range tags {
		out = append(out, tag{version: t.Version, os: t.OS, arch: t.Arch, processor: t.Processor})
	}
	return out
}

func printInstalls(w *tabwriter.Writer, name, active string, tags []tag) {
	if len(tags) == 0 {
		_, _ = fmt.Fprintf(w, "  %s\tnot installed\t(run ai stack install)\n", name)
		return
	}
	for i, t := range tags {
		label := name
		if i > 0 {
			label = ""
		}
		mark := ""
		if strings.Contains(active, filepath.Join(t.os, t.arch, t.processor)) {
			mark = "  <- active"
		}
		_, _ = fmt.Fprintf(w, "  %s\t%s\t%s/%s/%s%s\n", label, t.version, t.os, t.arch, t.processor, mark)
	}
}

// printRoles is the table that matters most: which model each role resolves to
// and whether it is actually on disk. A typo in a model id shows up here
// rather than at the moment a pipeline needs it.
func printRoles() error {
	llama, whisper, err := openStores()
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer func() { _ = w.Flush() }()

	for _, r := range config.Roles {
		name := r.Model()
		if name == "" {
			_, _ = fmt.Fprintf(w, "  %s\t%s\t(unbound)\n", r.Name, r.Backend)
			continue
		}
		installed, err := roleInstalled(r, llama, whisper)
		if err != nil {
			return err
		}
		state := "MISSING"
		if installed {
			state = "installed"
		}
		_, _ = fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", r.Name, r.Backend, name, state)
	}
	return nil
}

// printDisk reports what the stack occupies. Free space is deliberately not
// reported: reading it portably needs syscalls that differ per platform, while
// "what is this costing me" is the question actually worth answering.
func printDisk(base string) {
	dirs := []string{"libraries", "models", "bucky-libraries", "bucky-models", "catalog"}

	var total int64
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer func() { _ = w.Flush() }()

	for _, d := range dirs {
		n, err := dirSize(filepath.Join(base, d))
		if err != nil {
			continue
		}
		total += n
		_, _ = fmt.Fprintf(w, "  %s\t%s\n", d, humanSize(n))
	}
	_, _ = fmt.Fprintf(w, "  total\t%s\n", humanSize(total))
}

func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // a file that vanished mid-walk is not worth failing over
		}
		total += info.Size()
		return nil
	})
	return total, err
}
