// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import "os"

// reveal shows path in the file manager: a directory opened, a file
// selected in its directory where the file manager can do that, and its
// directory opened where it cannot. Nothing is ever opened with whatever
// opens it, which for some files is running them. A variable, so that tests
// open nothing.
var reveal = func(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	cmd := revealCmd(path, fi.IsDir())
	if err := cmd.Start(); err != nil {
		return err
	}
	// Explorer exits with 1 when all went well; what matters is that it
	// started.
	go func() { _ = cmd.Wait() }()
	return nil
}
