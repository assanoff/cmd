// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

//go:build !windows

package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
)

// revealCmd runs Finder, which selects a file or a directory in the one it
// is in, or elsewhere xdg-open on a directory: given a file it would open
// the file.
func revealCmd(path string, dir bool) *exec.Cmd {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-R", path)
	}
	if !dir {
		path = filepath.Dir(path)
	}
	return exec.Command("xdg-open", path)
}
