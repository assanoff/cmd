// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// testModule writes a module example.com/m with the files, path → content,
// and returns its root.
func testModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.21\n")
	for name, content := range files {
		writeFile(t, filepath.Join(root, filepath.FromSlash(name)), content)
	}
	return root
}

// testAudit returns an audit of ./... in the module at root, with a store
// of its own.
func testAudit(t *testing.T, root string) *audit {
	t.Helper()
	return &audit{
		ctx:      t.Context(),
		cfg:      &config{},
		mod:      goModule{path: "example.com/m", root: root},
		packages: []string{"./..."},
		store:    t.TempDir(),
		logs:     t.TempDir(),
	}
}

func TestSubstitutions(t *testing.T) {
	root := testModule(t, map[string]string{
		"a.go":       "package m\n",
		"a_test.go":  "package m\n",
		"x_test.go":  "package m_test\n",
		"sub/b.go":   "package sub\n",
		"empty/x.md": "no Go here\n",
	})
	a := testAudit(t, root)
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go in PATH")
	}
	goBin, _ = filepath.Abs(goBin)
	toolDir, err := exec.Command("go", "env", "GOTOOLDIR").Output()
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(root)
	files := func(names ...string) []string {
		var paths []string
		for _, n := range names {
			paths = append(paths, filepath.Join(real, filepath.FromSlash(n)))
		}
		return paths
	}

	tests := []struct {
		cmd  []string
		want []string // the command, nil when skipped
		skip string
	}{
		{[]string{"go", "vet", "{packages}"}, []string{"go", "vet", "./..."}, ""},
		{[]string{"go", "{files}"}, append([]string{"go"},
			files("a.go", "a_test.go", "sub/b.go", "x_test.go")...), ""},
		{[]string{"go", "-include-pkgs={module}"}, []string{"go", "-include-pkgs=example.com/m"}, ""},
		{
			[]string{"go", "-vettool={gotooldir}/fix"},
			[]string{"go", "-vettool=" + strings.TrimSpace(string(toolDir)) + "/fix"},
			"",
		},
		{[]string{"go", "-vettool={bin:go}"}, []string{"go", "-vettool=" + goBin}, ""},
		{[]string{"go", "-vettool={bin:no-such-tool}"}, nil, "not installed: go install example.com/tool@latest"},
		{[]string{"no-such-tool", "{packages}"}, nil, "not installed: go install example.com/tool@latest"},
		{[]string{"go", "{file:capslock.json}"}, nil, "no capslock.json"},
		{[]string{"go", "{file:empty}"}, nil, "no empty"}, // a directory is no file
		{
			[]string{"go", "{golangci-config}"},
			[]string{"go", "--config=" + filepath.Join(a.logs, golangciName)},
			"",
		},
	}
	for _, tt := range tests {
		ch := &check{Name: "x", Cmd: tt.cmd, Install: "example.com/tool@latest"}
		argv, skip, err := a.command(ch)
		if err != nil {
			t.Errorf("command %q: %v", tt.cmd, err)
			continue
		}
		if tt.skip != "" && skip == "" {
			skip = "(not skipped)"
		}
		if skip != tt.skip {
			t.Errorf("command %q: skipped %q, want %q", tt.cmd, skip, tt.skip)
		}
		// Paths from go list may come through a link; compare them resolved.
		for i, arg := range argv {
			if r, err := filepath.EvalSymlinks(arg); err == nil && filepath.IsAbs(arg) && arg != goBin {
				argv[i] = r
			}
		}
		if !slices.Equal(argv, tt.want) {
			t.Errorf("command %q = %q, want %q", tt.cmd, argv, tt.want)
		}
	}

	// Without a golangci.yml in the store, the built-in one is in the logs.
	if data, err := os.ReadFile(filepath.Join(a.logs, golangciName)); err != nil ||
		!bytes.Equal(data, builtinGolangci) {
		t.Errorf("the built-in golangci.yml in the logs: %v\n%s", err, data)
	}

	// Without install, the program is named.
	if _, skip, _ := a.command(&check{Name: "x", Cmd: []string{"no-such-tool"}}); skip != "no-such-tool is not in PATH" {
		t.Errorf("a missing program without install: skipped %q", skip)
	}

	// The values that come from files.
	writeFile(t, filepath.Join(root, "capslock.json"), "{}\n")
	writeFile(t, filepath.Join(a.store, golangciName), "version: \"2\"\n")
	for _, tt := range []struct{ arg, want string }{
		{"{file:capslock.json}", filepath.Join(root, "capslock.json")},
		{"{golangci-config}", "--config=" + filepath.Join(a.store, golangciName)},
	} {
		argv, skip, err := a.command(&check{Name: "x", Cmd: []string{"go", tt.arg}})
		if err != nil || skip != "" || len(argv) != 2 || argv[1] != tt.want {
			t.Errorf("command with %s = %q, %q, %v; want %s", tt.arg, argv, skip, err, tt.want)
		}
	}
}

func TestNoGoFiles(t *testing.T) {
	root := testModule(t, map[string]string{"a.go": "package m\n", "empty/x.md": "no Go\n"})
	a := testAudit(t, root)
	a.packages = []string{"./empty/..."}
	_, skip, err := a.command(&check{Name: "gofmt", Cmd: []string{"go", "{files}"}})
	if err != nil || skip != "no Go files in ./empty/..." {
		t.Errorf("{files} of packages with no Go files: skipped %q, %v", skip, err)
	}
}

func TestGoVersionOfCheck(t *testing.T) {
	a := testAudit(t, testModule(t, nil))
	_, skip, err := a.command(&check{Name: "fix", Cmd: []string{"go"}, Go: "99.0"})
	if err != nil || !strings.HasPrefix(skip, "needs Go 99.0, the module builds with go1.") {
		t.Errorf("a check for Go 99.0: skipped %q, %v", skip, err)
	}
	if _, skip, err := a.command(&check{Name: "fix", Cmd: []string{"go"}, Go: "1.21"}); skip != "" || err != nil {
		t.Errorf("a check for Go 1.21: skipped %q, %v", skip, err)
	}
}

func TestProjectGolangci(t *testing.T) {
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		t.Skip("no golangci-lint in PATH")
	}
	// golangci-lint looks for a config in the home directory too.
	t.Setenv("HOME", t.TempDir())
	root := testModule(t, map[string]string{"a.go": "package m\n"})
	a := testAudit(t, root)
	ch := &check{Name: "golangci-project", Cmd: []string{"golangci-lint", "run", "{project-golangci}"}}
	if _, skip, err := a.command(ch); err != nil || skip != "no .golangci.yml" {
		t.Errorf("{project-golangci} with no config: skipped %q, %v", skip, err)
	}
	writeFile(t, filepath.Join(root, ".golangci.yml"), "version: \"2\"\n")
	argv, skip, err := a.command(ch)
	if err != nil || skip != "" {
		t.Fatalf("{project-golangci} with a config: skipped %q, %v", skip, err)
	}
	if got, want := argv[2], "--config="+filepath.Join(root, ".golangci.yml"); got != want {
		t.Errorf("{project-golangci} = %q, want %q", got, want)
	}
}

func TestInterrupt(t *testing.T) {
	// A process that is gone already cannot be asked to stop.
	cmd := exec.Command("go", "version")
	if err := cmd.Run(); err != nil {
		t.Skip(err)
	}
	if err := interrupt(cmd.Process); err == nil {
		t.Error("interrupt of a process that is done: no error")
	}
}
