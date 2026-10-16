// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func showCommand() *command {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	out := fs.String("o", "", "write the page to `file` and open no browser; "+
		"a directory takes the default name")
	return &command{
		name:  "show",
		usage: "show [-o file] [report]",
		short: "Write a report as a page of HTML beside it and open it in a browser",
		flags: fs,
		run: func(ctx context.Context, args []string) error {
			if len(args) > 1 {
				return usagef("show takes one report, not %d", len(args))
			}
			return runShow(ctx, *out, strings.Join(args, ""))
		},
	}
}

// openBrowser opens a file in the web browser; a variable, so that tests
// open none.
var openBrowser = func(file string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", file)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", file)
	default:
		cmd = exec.Command("xdg-open", file)
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runShow writes the report that arg names as a page of HTML and opens it
// in a browser; with out, it writes the page there and opens nothing.
func runShow(ctx context.Context, out, arg string) error {
	store, err := storeDir()
	if err != nil {
		return err
	}
	r, err := findReport(ctx, store, arg)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(store)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		return err
	}
	root := ""
	if r.err == nil {
		root = r.Root
	}
	body := renderMarkdown(string(data), root, editorLinker(cfg.Editor, root))
	title := r.name()
	if r.Module != "" {
		title = r.Module + " · " + title
	}
	html, err := renderPage(title, body)
	if err != nil {
		return err
	}

	file := strings.TrimSuffix(r.path, ".md") + ".html"
	if out != "" {
		file = out
		if fi, err := os.Stat(out); err == nil && fi.IsDir() {
			file = filepath.Join(out, r.name()+".html")
		}
	}
	if err := os.WriteFile(file, html, 0o644); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "wrote %s\n", file); err != nil {
		return err
	}
	// A page written somewhere named is for keeping, not for reading now.
	if out != "" {
		return nil
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return err
	}
	return openBrowser(abs)
}

// findReport returns the report that arg names: the path of a report, its
// name or the start of it, such as its time, or a part of its module and
// name that only one report has. With no arg it is the newest report of the
// module of the current directory, or outside a module the newest of all.
func findReport(ctx context.Context, store, arg string) (*report, error) {
	if arg != "" {
		if r, ok := reportFile(arg); ok {
			return r, nil
		}
	}
	reports, err := loadReports(store)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(store, "reports")
	if arg != "" {
		return matchReport(reports, arg, dir)
	}

	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	mod, err := findModule(ctx, wd)
	switch {
	case errors.Is(err, errNoModule):
		if len(reports) == 0 {
			return nil, fmt.Errorf("no reports in %s", dir)
		}
		return reports[0], nil
	case err != nil:
		return nil, err
	}
	for _, r := range reports {
		if r.Module == mod.path {
			return r, nil
		}
	}
	return nil, fmt.Errorf("no reports of %s in %s; go-audit reports lists them all", mod.path, dir)
}

// reportFile returns the report at path, a .md file or its .json twin, if
// there is one: one in the store or one a module keeps, as go-style 0.1.0
// did.
func reportFile(path string) (*report, bool) {
	md := strings.TrimSuffix(path, ".json")
	if md != path {
		md += ".md"
	}
	if filepath.Ext(md) != ".md" {
		return nil, false
	}
	fi, err := os.Stat(md)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	return readReport(filepath.Dir(md), md), true
}

// matchReport returns the one report that arg names: by its name, by the
// start of its name, or by a part of its module and name. Several that
// match are an error that lists them.
func matchReport(reports []*report, arg, dir string) (*report, error) {
	var byStart, byPart []*report
	for _, r := range reports {
		name := r.name()
		switch {
		case name == arg:
			return r, nil
		case strings.HasPrefix(name, arg):
			byStart = append(byStart, r)
		case strings.Contains(strings.ToLower(r.Module+"/"+name), strings.ToLower(arg)):
			byPart = append(byPart, r)
		}
	}
	match, why := byStart, "have names starting with"
	if len(match) == 0 {
		match, why = byPart, "match"
	}
	switch len(match) {
	case 0:
		return nil, fmt.Errorf("no report in %s matches %q; go-audit reports lists them", dir, arg)
	case 1:
		return match[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d reports %s %q:", len(match), why, arg)
	for _, r := range match {
		fmt.Fprintf(&b, "\n\t%s  %s", r.name(), r.Module)
	}
	return nil, errors.New(b.String())
}
