// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The store is a contract with another program: the go-style plugin writes
// into it in Python. These tests pin down the layout both sides rely on.

func TestStoreDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GO_STYLE_HOME", dir)
	if got, err := storeDir(); err != nil || got != dir {
		t.Errorf("storeDir() with GO_STYLE_HOME=%s = %q, %v", dir, got, err)
	}

	t.Chdir(dir)
	t.Setenv("GO_STYLE_HOME", "rel")
	if got, _ := storeDir(); got != filepath.Join(dir, "rel") {
		t.Errorf("a relative GO_STYLE_HOME gives %q, want it made absolute", got)
	}

	t.Setenv("GO_STYLE_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory:", err)
	}
	if got, _ := storeDir(); got != filepath.Join(home, ".go-style") {
		t.Errorf("storeDir() without GO_STYLE_HOME = %q, want ~/.go-style", got)
	}
}

func TestModuleDir(t *testing.T) {
	tests := []struct {
		mod  string
		want string // under store/reports, slash-separated
	}{
		{"github.com/assanoff/cmd/ai", "github.com/assanoff/cmd/ai"},
		{"git.chocodev.kz/ryadom/payments", "git.chocodev.kz/ryadom/payments"},
		{"github.com/BurntSushi/toml", "github.com/!burnt!sushi/toml"},
		{"example.com/v2", "example.com/v2"},
	}
	store := t.TempDir()
	reports := filepath.Join(store, "reports")
	for _, tt := range tests {
		got, err := moduleDir(store, "reports", tt.mod)
		if err != nil {
			t.Errorf("moduleDir(%q): %v", tt.mod, err)
			continue
		}
		if want := filepath.Join(reports, filepath.FromSlash(tt.want)); got != want {
			t.Errorf("moduleDir(%q) = %q, want %q", tt.mod, got, want)
		}
		if back := moduleFromDir(reports, got); back != tt.mod {
			t.Errorf("moduleFromDir(moduleDir(%q)) = %q", tt.mod, back)
		}
	}

	if _, err := moduleDir(store, "reports", "not a path"); err == nil {
		t.Error(`moduleDir("not a path") succeeded, want an error`)
	}
	if got := moduleFromDir(reports, reports); got != "" {
		t.Errorf("moduleFromDir of the reports directory itself = %q, want empty", got)
	}
	// A directory no one escaped is shown as it is rather than dropped.
	if got := moduleFromDir(reports, filepath.Join(reports, "Odd")); got != "Odd" {
		t.Errorf("moduleFromDir of an unescaped directory = %q, want %q", got, "Odd")
	}
}

func TestStampTime(t *testing.T) {
	want := time.Date(2026, 10, 16, 14, 19, 45, 0, time.Local)
	for _, name := range []string{"20261016-141945-style-all", "20261016-141945-audit", "20261016-141945"} {
		if got := stampTime(name); !got.Equal(want) {
			t.Errorf("stampTime(%q) = %v, want %v", name, got, want)
		}
	}
	for _, name := range []string{"", "notes", "2026-10-16-style"} {
		if got := stampTime(name); !got.IsZero() {
			t.Errorf("stampTime(%q) = %v, want the zero time", name, got)
		}
	}
}

func TestFindModule(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/inventory\n\ngo 1.21\n")
	sub := filepath.Join(root, "internal", "store")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := findModule(t.Context(), sub)
	if err != nil {
		t.Fatal(err)
	}
	// The temporary directory may be reached through a symbolic link
	// (/var → /private/var on macOS), and go env reports the real path.
	wantRoot, _ := filepath.EvalSymlinks(root)
	gotRoot, _ := filepath.EvalSymlinks(m.root)
	if m.path != "example.com/inventory" || gotRoot != wantRoot {
		t.Errorf("findModule = %+v, want example.com/inventory in %s", m, root)
	}

	_, err = findModule(t.Context(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not inside a Go module") {
		t.Errorf("findModule outside a module: err = %v, want \"not inside a Go module\"", err)
	}
}

func TestLoadReports(t *testing.T) {
	store := t.TempDir()
	at := func(s string) time.Time {
		t.Helper()
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	addReport(t, store, "github.com/assanoff/cmd/ai", "20261016-150210-audit",
		v1(at("2026-10-16T15:02:10+05:00"), "audit", "github.com/assanoff/cmd/ai", "./...", 12, "vet 1 · staticcheck 6"))
	addReport(t, store, "github.com/BurntSushi/toml", "20261015-090000-audit",
		v1(at("2026-10-15T09:00:00Z"), "audit", "github.com/BurntSushi/toml", "./...", 0, "nothing found"))
	addReport(t, store, "github.com/assanoff/cmd/ai", "20261014-100000-audit", "") // no twin
	addReport(t, store, "github.com/assanoff/cmd/ai", "20261013-100000-audit", "{")
	addReport(t, store, "git.chocodev.kz/ryadom/payments", "20261016-120000-all",
		`{"run": "20261016-115000-all", "mode": "full", "findings": []}`) // go-style v0.1.0
	addReport(t, store, "git.chocodev.kz/ryadom/payments", "20261012-100000-style-all",
		`{"format": 2, "kind": "style", "time": "2026-10-12T10:00:00Z"}`)
	// Only .md files are reports; show writes .html beside them.
	writeFile(t, filepath.Join(store, "reports", "github.com", "assanoff", "cmd", "ai", "20261016-150210-audit.html"), "<p>")

	reports, err := loadReports(store)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name, module, err string
	}{
		{"20261016-150210-audit", "github.com/assanoff/cmd/ai", ""},
		{"20261016-120000-all", "git.chocodev.kz/ryadom/payments", "older format"},
		{"20261015-090000-audit", "github.com/BurntSushi/toml", ""},
		{"20261014-100000-audit", "github.com/assanoff/cmd/ai", "no .json twin"},
		{"20261013-100000-audit", "github.com/assanoff/cmd/ai", "unreadable .json twin"},
		{"20261012-100000-style-all", "git.chocodev.kz/ryadom/payments", "format 2, newer"},
	}
	if len(reports) != len(want) {
		t.Fatalf("loadReports found %d reports, want %d", len(reports), len(want))
	}
	for i, w := range want {
		r := reports[i]
		var gotErr string
		if r.err != nil {
			gotErr = r.err.Error()
		}
		if r.name() != w.name || r.Module != w.module ||
			(w.err == "") != (gotErr == "") || !strings.Contains(gotErr, w.err) {
			t.Errorf("report %d = %s of %s, err %q; want %s of %s, err containing %q",
				i, r.name(), r.Module, gotErr, w.name, w.module, w.err)
		}
	}

	first := reports[0]
	if first.Kind != "audit" || first.Total != 12 || first.Summary != "vet 1 · staticcheck 6" ||
		first.Label != "./..." || !first.Time.Equal(at("2026-10-16T15:02:10+05:00")) {
		t.Errorf("fields of the first report = %+v", first)
	}
	// A twin of a newer format is not to be trusted at all, not even partly.
	if last := reports[len(reports)-1]; last.Kind != "" {
		t.Errorf("an unreadable report kept kind %q from its twin", last.Kind)
	}
}

func TestLoadReportsEmptyStore(t *testing.T) {
	reports, err := loadReports(filepath.Join(t.TempDir(), "never-created"))
	if err != nil || len(reports) != 0 {
		t.Errorf("loadReports of a missing store = %d reports, %v; want none and no error", len(reports), err)
	}
}

// v1 returns the JSON twin of a report in format 1.
func v1(at time.Time, kind, mod, label string, total int, summary string) string {
	data, err := json.Marshal(map[string]any{
		"format": 1, "kind": kind, "module": mod, "root": "/src/" + mod,
		"time": at, "label": label, "total": total, "summary": summary,
		"findings": []any{}, // the part of the kind, which the list ignores
	})
	if err != nil {
		panic(err)
	}
	return string(data)
}

// addReport puts a report named name of module mod into the store, with js as
// its JSON twin; an empty js leaves the twin out.
func addReport(t *testing.T, store, mod, name, js string) {
	t.Helper()
	dir, err := moduleDir(store, "reports", mod)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, name+".md"), "# "+name+"\n")
	if js != "" {
		writeFile(t, filepath.Join(dir, name+".json"), js)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
