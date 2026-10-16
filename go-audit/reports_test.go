// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// listStore fills a store with reports of three modules and points
// GO_STYLE_HOME at it.
func listStore(t *testing.T) string {
	t.Helper()
	store := t.TempDir()
	t.Setenv("GO_STYLE_HOME", store)
	at := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)
	addReport(t, store, "github.com/assanoff/cmd/ai", "20261016-150000-audit",
		v1(at, "audit", "github.com/assanoff/cmd/ai", "./...", 12, "vet 1 · staticcheck 6"))
	addReport(t, store, "git.chocodev.kz/ryadom/payments", "20261016-140000-style-all",
		v1(at.Add(-time.Hour), "style", "git.chocodev.kz/ryadom/payments", "all", 459, "must 58 · should 336"))
	addReport(t, store, "git.chocodev.kz/ryadom/payments", "20261016-130000-audit",
		v1(at.Add(-2*time.Hour), "audit", "git.chocodev.kz/ryadom/payments", "./...", 3, "gosec error"))
	addReport(t, store, "github.com/BurntSushi/toml", "20261016-120000-audit", "")
	return store
}

// list runs go-audit reports with args and returns the lines it printed.
func list(t *testing.T, args ...string) ([]string, error) {
	t.Helper()
	cmd := reportsCommand()
	if err := cmd.flags.Parse(args); err != nil {
		t.Fatalf("parsing %q: %v", args, err)
	}
	var out bytes.Buffer
	saved := stdout
	stdout = &out
	defer func() { stdout = saved }()
	err := cmd.run(t.Context(), cmd.flags.Args())
	return strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n"), err
}

func TestListReports(t *testing.T) {
	store := listStore(t)
	dir := filepath.Join(store, "reports")
	const (
		ai   = "github.com/assanoff/cmd/ai"
		pay  = "git.chocodev.kz/ryadom/payments"
		toml = "github.com/BurntSushi/toml"
	)

	tests := []struct {
		args    []string
		modules []string // the module of each line, in order
		footer  string
	}{
		{nil, []string{ai, pay, pay, toml}, "4 of 4 · " + dir},
		{[]string{"-n", "2"}, []string{ai, pay}, "2 of 4 · " + dir},
		{[]string{"-n", "0"}, []string{ai, pay, pay, toml}, "4 of 4 · " + dir},
		{[]string{"payments"}, []string{pay, pay}, "2 of 2 · " + dir},
		{[]string{"-n", "1", "payments"}, []string{pay}, "1 of 2 · " + dir},
		{[]string{"BurntSushi"}, []string{toml}, "1 of 1 · " + dir},
	}
	for _, tt := range tests {
		lines, err := list(t, tt.args...)
		if err != nil {
			t.Errorf("reports %q: %v", tt.args, err)
			continue
		}
		if len(lines) != len(tt.modules)+1 {
			t.Errorf("reports %q printed %d lines, want %d:\n%s",
				tt.args, len(lines), len(tt.modules)+1, strings.Join(lines, "\n"))
			continue
		}
		for i, mod := range tt.modules {
			if f := strings.Fields(lines[i]); len(f) < 3 || f[2] != mod {
				t.Errorf("reports %q line %d = %q, want module %s", tt.args, i, lines[i], mod)
			}
		}
		if got := lines[len(lines)-1]; got != tt.footer {
			t.Errorf("reports %q footer = %q, want %q", tt.args, got, tt.footer)
		}
	}
}

func TestListReportsLine(t *testing.T) {
	listStore(t)
	lines, err := list(t)
	if err != nil {
		t.Fatal(err)
	}
	// The time is shown in the local zone, whatever zone the twin has.
	when := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC).Local().Format("2006-01-02 15:04")
	want := when + " github.com/assanoff/cmd/ai audit ./... 12 vet 1 · staticcheck 6"
	if got := strings.Join(strings.Fields(lines[0]), " "); got != want {
		t.Errorf("first line = %q, want the fields %q", lines[0], want)
	}
	if !strings.HasPrefix(lines[0], when+"  ") {
		t.Errorf("first line = %q, want it to start with the local time %s", lines[0], when)
	}
	// A report without its twin: file name for the label, the reason for the
	// summary, and no number of findings.
	last := lines[len(lines)-2]
	for _, part := range []string{"github.com/BurntSushi/toml", " ? ", "20261016-120000-audit", " - ", "no .json twin"} {
		if !strings.Contains(last, part) {
			t.Errorf("line of a report without a twin = %q, want it to contain %q", last, part)
		}
	}
}

func TestListReportsCurrentModule(t *testing.T) {
	listStore(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module git.chocodev.kz/ryadom/payments\n\ngo 1.21\n")
	t.Chdir(root)
	lines, err := list(t, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 || !strings.HasPrefix(lines[2], "2 of 2 ") {
		t.Errorf("reports . in payments printed:\n%s\nwant its two reports", strings.Join(lines, "\n"))
	}

	t.Chdir(t.TempDir())
	if _, err := list(t, "."); err == nil || !strings.Contains(err.Error(), "not inside a Go module") {
		t.Errorf("reports . outside a module: err = %v, want \"not inside a Go module\"", err)
	}

	// Even with nothing in the store, "." outside a module is a mistake
	// rather than "no reports yet".
	t.Setenv("GO_STYLE_HOME", t.TempDir())
	if _, err := list(t, "."); err == nil || !strings.Contains(err.Error(), "not inside a Go module") {
		t.Errorf("reports . outside a module, empty store: err = %v, want \"not inside a Go module\"", err)
	}
}

func TestListReportsEmpty(t *testing.T) {
	store := t.TempDir()
	t.Setenv("GO_STYLE_HOME", store)
	lines, err := list(t)
	if err != nil || len(lines) != 1 || !strings.HasPrefix(lines[0], "no reports yet in ") {
		t.Errorf("reports of an empty store = %q, %v; want \"no reports yet\"", lines, err)
	}

	listStore(t)
	lines, err = list(t, "nothing-like-this")
	if err != nil || len(lines) != 1 || !strings.HasPrefix(lines[0], `no reports for "nothing-like-this"`) {
		t.Errorf("reports with a filter that matches nothing = %q, %v", lines, err)
	}
}

func TestListReportsUsage(t *testing.T) {
	listStore(t)
	for _, args := range [][]string{{"-n", "-1"}, {"a", "b"}} {
		_, err := list(t, args...)
		if _, ok := errors.AsType[usageError](err); !ok {
			t.Errorf("reports %q: err = %v, want a usage error", args, err)
		}
	}
	if err := reportsCommand().flags.Parse([]string{"-n", "x"}); err == nil {
		t.Error(`reports -n x parsed, want an error`)
	}
}
