// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"os/exec"
	"syscall"
)

// revealCmd runs Explorer. It reads its command line itself and wants
// /select, outside the quotes of the path, so the line is written whole
// rather than quoted by exec; a Windows path has no quotes in it.
func revealCmd(path string, dir bool) *exec.Cmd {
	arg := `/select,"` + path + `"`
	if dir {
		arg = `"` + path + `"`
	}
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "explorer.exe " + arg}
	return cmd
}
