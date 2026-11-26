// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// skillText is the skill that teaches an agent to use prr. It is built
// into the binary, so the skill an agent reads is always the one of the
// prr it runs.
//
//go:embed SKILL.md
var skillText string

// cmdSkill prints the skill, or installs it for Claude Code, replacing the
// copy of an earlier version.
func cmdSkill(install bool) error {
	if !install {
		fmt.Print(skillText)
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".claude", "skills", "prr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(file, []byte(skillText), 0o644); err != nil {
		return err
	}
	fmt.Printf("installed %s\n", file)
	return nil
}
