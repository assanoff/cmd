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
	"path/filepath"
	"strings"
)

func useCommand() *command {
	fs := flag.NewFlagSet("use", flag.ContinueOnError)
	deployed := fs.Bool("deployed", false, "edit ~/.config/ai/config instead of the dev-env repository (lost on the next deploy)")

	return &command{
		name:  "use",
		usage: "use [-deployed] <role> <model>",
		short: "Point a role at a different model",
		flags: fs,
		run: func(_ context.Context, args []string) error {
			if f, ok := flagAfterArgs(args); ok {
				return usagef("%s: flags go before the arguments, as in \"ai-stack use -deployed <role> <model>\"", f)
			}
			if len(args) != 2 {
				return usagef("use takes a role and a model; roles are %s", roleNames())
			}
			name, value := args[0], args[1]

			r, ok := lookupRole(name)
			if !ok {
				return usagef("unknown role %q: want %s", name, roleNames())
			}
			return use(r, value, *deployed)
		},
	}
}

// use rebinds a role by editing the config file.
//
// By default it edits the copy inside the dev-env repository, not the deployed
// one. The deploy script copies env/.config/* over $XDG_CONFIG_HOME and does an
// rm -rf of the target directory first, so an edit to ~/.config/ai/config
// disappears the next time anything is deployed. Writing to the repository is
// what makes the change survive, at the cost of needing a deploy to take
// effect — which is the same bargain every other file in dev-env makes.
func use(r role, value string, deployed bool) error {
	path, err := configTarget(deployed)
	if err != nil {
		return err
	}

	// The "before" value has to come from the file being edited, not from the
	// resolved role: the repository copy and the deployed one differ for as
	// long as it takes to run a deploy, and reporting the deployed value while
	// editing the repository one would describe a change that is not happening.
	before, err := setKey(path, r.key, value)
	if err != nil {
		return err
	}

	fmt.Printf("%s: %s -> %s\n", r.name, orNone(before), value)
	fmt.Printf("wrote %s\n", path)

	if !deployed {
		fmt.Println("run 'dev-env deploy ai' to apply it")
	}
	fmt.Printf("then 'ai-stack models sync' to download it if needed\n")

	return nil
}

func configTarget(deployed bool) (string, error) {
	if !deployed {
		if path, ok := repoConfig(); ok {
			return path, nil
		}
		return "", fmt.Errorf("dev-env repository not found; pass -deployed to edit ~/.config/ai/config instead")
	}

	dir := configDir()
	if dir == "" {
		return "", fmt.Errorf("cannot locate the config directory")
	}
	return filepath.Join(dir, "ai", "config"), nil
}

// setKey rewrites one KEY=value line in place and reports what was there
// before. Every comment, blank line and the ordering are kept: this is a file a
// human maintains, and rewriting it wholesale would throw away its structure.
func setKey(path, key, value string) (before string, err error) {
	lines, err := readLines(path)
	if err != nil {
		return "", err
	}

	replaced := false
	for i, line := range lines {
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.TrimSpace(k) != key {
			continue
		}
		before = strings.Trim(strings.TrimSpace(v), `"'`)
		lines[i] = key + "=" + value
		replaced = true
		break
	}
	if !replaced {
		lines = append(lines, key+"="+value)
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return "", err
	}
	return before, nil
}

// readLines returns the file's lines, or an empty slice when it does not exist
// yet: rebinding a role is a reasonable way to create the config.
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	// Drop a single trailing blank so appending does not accumulate them.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, nil
}

func orNone(s string) string {
	if s == "" {
		return "(unbound)"
	}
	return s
}
