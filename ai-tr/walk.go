// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// defaultExts is the text set worth translating. It only filters directory
// walks; a file named on the command line is read whatever it is called.
const defaultExts = "txt,md,srt,vtt"

// job is one unit of work: read from in, write to out. An empty out means
// standard output, and in of "-" means standard input.
type job struct {
	in  string
	out string
}

// source is a resolved input together with the path to reproduce below -O.
type source struct {
	path string
	rel  string
}

// plan resolves the command line into jobs. It decides both what to read and
// where each answer goes, which is the only place those two questions are
// answered.
func plan(args []string) ([]job, error) {
	// No arguments means standard input. Sitting on a terminal, though, the
	// user is far more likely to have forgotten the file than to want to type
	// a document, so show the flags instead of hanging on a read.
	if len(args) == 0 {
		if stdinIsTerminal() {
			usage()
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

		found, err := walkDir(arg)
		if err != nil {
			return nil, err
		}
		if len(found) == 0 {
			fmt.Fprintf(os.Stderr, "ai-tr: %s: no files matching -ext\n", arg)
		}
		srcs = append(srcs, found...)
	}

	if len(srcs) == 0 {
		return nil, nil
	}

	// One input is the pipeline case: straight to standard output unless the
	// caller asked for a file.
	if len(srcs) == 1 && *flagOutDir == "" {
		return []job{{in: srcs[0].path, out: *flagOut}}, nil
	}
	if *flagOut != "" {
		return nil, usagef("-o takes one input, got %d; use -O for a batch", len(srcs))
	}

	jobs := make([]job, 0, len(srcs))
	for _, src := range srcs {
		if src.path == "-" {
			return nil, usagef("standard input cannot be part of a batch")
		}
		jobs = append(jobs, job{in: src.path, out: outPath(src, formatExt(src.path))})
	}
	return jobs, nil
}

// walkDir collects the text files of a directory, in lexical order.
// filepath.WalkDir already sorts, so the order of a batch is reproducible.
func walkDir(root string) ([]source, error) {
	accept := extSet(*flagExts)

	var found []source
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root || *flagRecurse {
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

// outPath names the file an answer goes to: beside its source by default, or
// below -O with the source's own layout preserved so a recursive run does not
// flatten a tree into one directory.
func outPath(src source, ext string) string {
	base := strings.TrimSuffix(filepath.Base(src.path), filepath.Ext(src.path))
	name := base + *flagSuffix + ext

	if *flagOutDir == "" {
		return filepath.Join(filepath.Dir(src.path), name)
	}
	return filepath.Join(*flagOutDir, filepath.Dir(src.rel), name)
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

// formatExt keeps the input's own extension. Translating subtitles has to
// produce subtitles and translating markdown has to stay markdown, so unlike
// the other commands ai-tr does not choose an extension of its own.
func formatExt(in string) string {
	if ext := filepath.Ext(in); ext != "" {
		return ext
	}
	return ".txt"
}

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// describe prints what a run would do. This is the payload of -n, so it goes
// to standard output.
func describe(jobs []job, model string, to, from string, subs bool) {
	fmt.Printf("model  %s\n", model)
	fmt.Printf("into   %s\n", to)
	if from == "" {
		fmt.Println("from   (detected)")
	} else {
		fmt.Printf("from   %s\n", from)
	}
	fmt.Printf("timing %v\n", subs)
	for _, j := range jobs {
		out := j.out
		if out == "" {
			out = "(standard output)"
		}
		fmt.Printf("%s -> %s\n", describeInput(j.in), out)
	}
	fmt.Printf("%d input(s)\n", len(jobs))
}
