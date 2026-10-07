// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package walk turns a command line into a list of jobs.
//
// It answers both halves of the same question — what to read, and where each
// result goes — which is why they live together and nowhere else. ask, hear,
// sum and tr had four copies of this file; they differed only in the extension
// they chose for an output and in the name they printed in a warning.
package walk

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Job is one unit of work: read from In, write to Out. An empty Out means
// standard output, and In of "-" means standard input.
type Job struct {
	In  string
	Out string
}

// source is a resolved input together with the path to reproduce below -O.
type source struct {
	path string
	rel  string
}

// ErrTerminalInput is returned when there is nothing to read: no arguments and
// standard input is a terminal. The user is far more likely to have forgotten
// the file than to want to type a document, so the caller turns this into its
// own usage message rather than hanging on a read.
var ErrTerminalInput = errors.New("no input: give a file, or pipe something in")

// Request is everything the batch flags say.
type Request struct {
	Args    []string
	Recurse bool
	Exts    string
	Out     string // -o: one named output file
	OutDir  string // -O: a tree of outputs
	Suffix  string

	// OutExt is the extension each output file gets, including the dot. It is
	// only consulted for a batch. Empty keeps each input's own extension,
	// which is what tr needs: translating subtitles has to produce subtitles.
	OutExt string

	// Report receives notes about inputs that matched nothing. It may be nil.
	Report func(format string, args ...any)
}

// Jobs resolves the request. The one-input case is the pipeline case and goes
// straight to standard output unless the caller asked for a file.
func Jobs(r Request) ([]Job, error) {
	args := r.Args
	if len(args) == 0 {
		if stdinIsTerminal() {
			return nil, ErrTerminalInput
		}
		args = []string{"-"}
	}

	var srcs []source
	for _, arg := range args {
		if arg == "-" {
			srcs = append(srcs, source{path: "-", rel: "-"})
			continue
		}

		fi, err := os.Stat(arg)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			srcs = append(srcs, source{path: arg, rel: filepath.Base(arg)})
			continue
		}

		found, err := walkDir(arg, r.Exts, r.Recurse)
		if err != nil {
			return nil, err
		}
		if len(found) == 0 && r.Report != nil {
			r.Report("%s: no files matching --ext", arg)
		}
		srcs = append(srcs, found...)
	}

	if len(srcs) == 0 {
		return nil, nil
	}

	if len(srcs) == 1 && r.OutDir == "" {
		return []Job{{In: srcs[0].path, Out: r.Out}}, nil
	}
	if r.Out != "" {
		return nil, &BatchError{Reason: "-o takes one input", Count: len(srcs)}
	}

	jobs := make([]Job, 0, len(srcs))
	for _, src := range srcs {
		if src.path == "-" {
			return nil, &BatchError{Reason: "standard input cannot be part of a batch", Count: len(srcs)}
		}
		jobs = append(jobs, Job{In: src.path, Out: r.outPath(src)})
	}
	return jobs, nil
}

// BatchError is a caller mistake about how a batch was asked for. The caller
// turns it into a usage error; this package has no opinion about exit codes.
type BatchError struct {
	Reason string
	Count  int
}

func (e *BatchError) Error() string {
	if e.Count > 1 {
		return fmt.Sprintf("%s, got %d; use -O for a batch", e.Reason, e.Count)
	}
	return e.Reason
}

// walkDir collects the acceptable files of a directory, in lexical order.
// filepath.WalkDir already sorts, so the order of a batch is reproducible.
func walkDir(root, exts string, recurse bool) ([]source, error) {
	accept := extSet(exts)

	var found []source
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root || recurse {
				return nil
			}
			return fs.SkipDir
		}
		if !accept[strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))] {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		found = append(found, source{path: path, rel: rel})
		return nil
	})
	return found, err
}

// outPath names the file a result goes to: beside its source by default, or
// below -O with the source's own layout preserved so a recursive run does not
// flatten a tree into one directory.
func (r Request) outPath(src source) string {
	base := strings.TrimSuffix(filepath.Base(src.path), filepath.Ext(src.path))

	ext := r.OutExt
	if ext == "" {
		ext = filepath.Ext(src.path)
	}
	name := base + r.Suffix + ext

	if r.OutDir == "" {
		return filepath.Join(filepath.Dir(src.path), name)
	}
	return filepath.Join(r.OutDir, filepath.Dir(src.rel), name)
}

func extSet(list string) map[string]bool {
	set := make(map[string]bool)
	for _, ext := range strings.Split(list, ",") {
		ext = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ext), "."))
		if ext != "" {
			set[ext] = true
		}
	}
	return set
}

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
