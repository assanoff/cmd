// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package cli

// Batch holds the flags every file-processing subcommand accepts. go-flags
// folds an embedded struct into the command's own option list, so these read
// as ordinary flags of each subcommand rather than as a group of their own.
//
// --suffix and --ext are deliberately absent: their defaults differ per
// subcommand (sum appends "-sum", tr appends the target language, hear accepts
// media extensions rather than text ones), and a default that has to be
// patched after parsing is worse than a field declared where it belongs.
type Batch struct {
	Recurse bool   `short:"r" long:"recurse" description:"descend into subdirectories of directory arguments"`
	Out     string `short:"o" long:"out" value-name:"FILE" description:"write the result to this file instead of standard output"`
	OutDir  string `short:"O" long:"out-dir" value-name:"DIR" description:"write results below this directory, preserving the input layout"`
	Force   bool   `long:"force" description:"overwrite existing output files instead of skipping them"`
	DryRun  bool   `short:"n" long:"dry-run" description:"print the plan and exit without asking the model anything"`
	Quiet   bool   `short:"q" long:"quiet" description:"suppress progress on standard error"`
}

// Check reports the combinations that cannot both be honoured.
func (b Batch) Check() error {
	if b.Out != "" && b.OutDir != "" {
		return Usagef("-o and -O are mutually exclusive")
	}
	return nil
}
