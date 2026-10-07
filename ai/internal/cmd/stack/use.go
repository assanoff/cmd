// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package stack

import (
	"fmt"
	"path/filepath"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/config"
)

type useCommand struct {
	Deployed bool `long:"deployed" description:"edit ~/.config/ai/config instead of the dev-env repository (lost on the next deploy)"`

	Args struct {
		Role  string `positional-arg-name:"role" required:"yes" description:"the role to rebind"`
		Model string `positional-arg-name:"model" required:"yes" description:"the model it should resolve to"`
	} `positional-args:"yes" required:"yes"`
}

// Execute rebinds a role by editing the config file.
//
// By default it edits the copy inside the dev-env repository, not the deployed
// one. The deploy script copies env/.config/* over $XDG_CONFIG_HOME and does an
// rm -rf of the target directory first, so an edit to ~/.config/ai/config
// disappears the next time anything is deployed. Writing to the repository is
// what makes the change survive, at the cost of needing a deploy to take
// effect — which is the same bargain every other file in dev-env makes.
func (c *useCommand) Execute(args []string) error {
	if len(args) > 0 {
		return cli.Usagef("use takes a role and a model, nothing more")
	}

	r, ok := config.LookupRole(c.Args.Role)
	if !ok {
		return cli.Usagef("unknown role %q: want %s", c.Args.Role, config.RoleNames())
	}

	path, err := c.target()
	if err != nil {
		return err
	}

	// The "before" value has to come from the file being edited, not from the
	// resolved role: the repository copy and the deployed one differ for as
	// long as it takes to run a deploy, and reporting the deployed value while
	// editing the repository one would describe a change that is not happening.
	before, err := config.SetKey(path, r.Key, c.Args.Model)
	if err != nil {
		return err
	}

	fmt.Printf("%s: %s -> %s\n", r.Name, orNone(before), c.Args.Model)
	fmt.Printf("wrote %s\n", path)

	if !c.Deployed {
		fmt.Println("run 'dev-env deploy ai' to apply it")
	}
	fmt.Println("then 'ai stack models sync' to download it if needed")

	return nil
}

func (c *useCommand) target() (string, error) {
	if !c.Deployed {
		if path, ok := config.RepoPath(); ok {
			return path, nil
		}
		return "", fmt.Errorf("dev-env repository not found; pass --deployed to edit ~/.config/ai/config instead")
	}

	dir := config.Dir()
	if dir == "" {
		return "", fmt.Errorf("cannot locate the config directory")
	}
	return filepath.Join(dir, "ai", "config"), nil
}

func orNone(s string) string {
	if s == "" {
		return "(unbound)"
	}
	return s
}
