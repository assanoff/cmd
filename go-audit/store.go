// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

// reportFormat is the version of the fields every report's JSON twin has.
// A twin of a newer format is listed as one this go-audit cannot read.
const reportFormat = 1

// stampLayout is the time at the start of every report's file name.
const stampLayout = "20060102-150405"

// storeDir returns the store: $GO_STYLE_HOME, or .go-style in the home
// directory.
func storeDir() (string, error) {
	if dir := os.Getenv("GO_STYLE_HOME"); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home directory for the store, set GO_STYLE_HOME: %w", err)
	}
	return filepath.Join(home, ".go-style"), nil
}

// moduleDir returns the directory of module path mod in area ("reports" or
// "runs") of the store. The path is escaped as in the module cache, so that
// Foo and foo stay apart on a case-insensitive file system. Unlike the module
// cache, the store takes any path a main module may have, "myapp" among them.
func moduleDir(store, area, mod string) (string, error) {
	if err := module.CheckImportPath(mod); err != nil {
		return "", err
	}
	esc := filepath.FromSlash(escapeModule(mod))
	return filepath.Join(store, area, esc), nil
}

// escapeModule turns each capital letter of a module path into "!" and the
// lower-case letter.
func escapeModule(mod string) string {
	var b strings.Builder
	for _, r := range mod {
		if 'A' <= r && r <= 'Z' {
			b.WriteByte('!')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// unescapeModule undoes escapeModule. It reports false for a string that
// escapeModule cannot have made: one with a capital letter, or with a "!"
// not followed by a lower-case letter.
func unescapeModule(esc string) (string, bool) {
	var b strings.Builder
	bang := false
	for _, r := range esc {
		switch {
		case bang && 'a' <= r && r <= 'z':
			b.WriteRune(r - ('a' - 'A'))
			bang = false
		case bang, 'A' <= r && r <= 'Z':
			return "", false
		case r == '!':
			bang = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), !bang
}

// goModule is the module that holds a directory.
type goModule struct {
	path string // the module path from go.mod
	root string // the directory of go.mod
}

// findModule returns the module holding dir, as the go command sees it.
func findModule(ctx context.Context, dir string) (goModule, error) {
	cmd := exec.CommandContext(ctx, "go", "env", "GOMOD")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return goModule{}, fmt.Errorf("go env GOMOD: %s", msg)
		}
		return goModule{}, fmt.Errorf("go env GOMOD: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return goModule{}, fmt.Errorf("not inside a Go module (no go.mod in %s or above)", dir)
	}
	data, err := os.ReadFile(gomod)
	if err != nil {
		return goModule{}, err
	}
	path := modfile.ModulePath(data)
	if path == "" {
		return goModule{}, fmt.Errorf("%s: no module line", gomod)
	}
	return goModule{path: path, root: filepath.Dir(gomod)}, nil
}

// A report is one report in the store, as the list sees it. The exported
// fields come from its JSON twin and are the ones every kind of report has.
// When the twin is missing or cannot be read, err says why, and the time and
// the module come from the file name and the directory instead.
type report struct {
	Format  int       `json:"format"`
	Kind    string    `json:"kind"`
	Module  string    `json:"module"`
	Root    string    `json:"root"`
	Time    time.Time `json:"time"`
	Label   string    `json:"label"`
	Total   int       `json:"total"`
	Summary string    `json:"summary"`

	path string // the .md file
	err  error
}

// name returns the report's file name without the extension.
func (r *report) name() string {
	return strings.TrimSuffix(filepath.Base(r.path), ".md")
}

// seq returns the number of a report among those of its module written in
// the same second: 1 for the first, n for the one whose name ends in -n.
func (r *report) seq() int {
	name := r.name()
	i := strings.LastIndexByte(name, '-')
	if i <= len(stampLayout) {
		return 1
	}
	if n, err := strconv.Atoi(name[i+1:]); err == nil && n >= 2 {
		return n
	}
	return 1
}

// loadReports returns the reports in the store, newest first; of those of
// one module in the same second, the last written first. A store with no
// reports directory yet has no reports.
func loadReports(store string) ([]*report, error) {
	dir := filepath.Join(store, "reports")
	var reports []*report
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".md" {
			reports = append(reports, readReport(dir, path))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(reports, func(a, b *report) int {
		return cmp.Or(
			b.Time.Compare(a.Time),
			strings.Compare(filepath.Dir(b.path), filepath.Dir(a.path)),
			cmp.Compare(b.seq(), a.seq()),
			strings.Compare(b.path, a.path),
		)
	})
	return reports, nil
}

// readReport reads the report at md under the reports directory dir.
func readReport(dir, md string) *report {
	r := &report{path: md}
	data, err := os.ReadFile(strings.TrimSuffix(md, ".md") + ".json")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		err = errors.New("no .json twin")
	case err != nil:
	default:
		err = json.Unmarshal(data, r)
		switch {
		case err != nil:
			err = fmt.Errorf("unreadable .json twin: %v", err)
		case r.Format == 0:
			err = errors.New(`.json twin of an older format: no "format" field`)
		case r.Format > reportFormat:
			err = fmt.Errorf(".json twin of format %d, newer than this go-audit reads (%d)",
				r.Format, reportFormat)
		}
	}
	if err != nil {
		// A failed decoding may have filled some of the fields; none of them
		// is to be trusted.
		r = &report{path: md, err: err}
		r.Time = stampTime(r.name())
		r.Module = moduleFromDir(dir, filepath.Dir(md))
	}
	return r
}

// stampTime returns the time at the start of a report's file name, or the
// zero time if the name does not start with one.
func stampTime(name string) time.Time {
	if len(name) < len(stampLayout) {
		return time.Time{}
	}
	t, err := time.ParseInLocation(stampLayout, name[:len(stampLayout)], time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// moduleFromDir returns the module path that the directory dir of the
// reports directory reports stands for, undoing moduleDir's escaping. A
// directory that is no escaped module path is returned as it is.
func moduleFromDir(reports, dir string) string {
	rel, err := filepath.Rel(reports, dir)
	if err != nil || rel == "." {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if path, ok := unescapeModule(rel); ok {
		return path
	}
	return rel
}
