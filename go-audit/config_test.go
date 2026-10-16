// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestBuiltinConfig(t *testing.T) {
	c, err := parseConfig(builtinConfig, "")
	if err != nil {
		t.Fatal(err)
	}
	var on, off []string
	for _, ch := range c.Checks {
		if ch.Off {
			off = append(off, ch.Name)
		} else {
			on = append(on, ch.Name)
		}
		if ch.line == 0 {
			t.Errorf("check %s has no line", ch.Name)
		}
	}
	want := []string{
		"vet", "fix", "staticcheck", "golangci-lint", "golangci-project", "exhaustruct", "gosec",
		"nilaway", "deadcode", "govulncheck", "go-licenses", "gostyle", "gopls", "tidy", "gofmt",
	}
	if !slices.Equal(on, want) {
		t.Errorf("checks that are on: %v, want %v", on, want)
	}
	if want := []string{"shadow", "capslock"}; !slices.Equal(off, want) {
		t.Errorf("checks that are off: %v, want %v", off, want)
	}
	if c.source() != "built-in" || c.Editor != "" {
		t.Errorf("built-in config: source %q, editor %q", c.source(), c.Editor)
	}
}

func TestOwnConfig(t *testing.T) {
	store := t.TempDir()
	if c, err := loadConfig(store); err != nil || c.file != "" {
		t.Fatalf("loadConfig of a store with no config = %v, %v; want the built-in one", c, err)
	}
	own := `editor: "zed://file/{path}:{line}:{col}"
checks:
  - name: vet
    cmd: [go, vet, -json, "{packages}"]
    format: vet-json
  - name: mine
    off: true
    cmd: [my-analyzer, -json, "{packages}"]
    format: vet-json
`
	writeFile(t, filepath.Join(store, configName), own)
	c, err := loadConfig(store)
	if err != nil {
		t.Fatal(err)
	}
	// One's own config replaces the built-in one as a whole.
	if len(c.Checks) != 2 || c.Checks[1].Name != "mine" || !c.Checks[1].Off || c.Checks[1].line != 6 {
		t.Errorf("own config: %d checks, the second %+v", len(c.Checks), c.Checks[1])
	}
	if c.Editor != "zed://file/{path}:{line}:{col}" || c.source() != filepath.Join(store, configName) {
		t.Errorf("own config: editor %q, source %q", c.Editor, c.source())
	}
}

func TestConfigErrors(t *testing.T) {
	const vet = "  - name: vet\n    cmd: [go, vet, -json, \"{packages}\"]\n    format: vet-json\n"
	tests := []struct {
		yaml string
		want string // the error, after "go-audit.yml:"
	}{
		{"", " empty"},
		{"editor: x\n", " no checks"},
		{"checks:\n  - name: vet\n    format: vet-json\n", "2: check vet: no cmd"},
		{"checks:\n  - cmd: [go]\n    format: lines\n", "2: a check with no name"},
		{"checks:\n  - name: Vet\n    cmd: [go]\n    format: lines\n", "2: check Vet: a name takes"},
		{"checks:\n  - name: vet\n    cmd: [go]\n", "2: check vet: no format"},
		{
			"checks:\n  - name: vet\n    cmd: [go]\n    format: json\n",
			`2: check vet: unknown format "json" (formats: deadcode, diff, go-licenses, ` +
				`golangci, gosec, govulncheck, lines, staticcheck, vet-json)`,
		},
		{"checks:\n" + vet + vet, "5: check vet is described twice, first at line 2"},
		{
			"checks:\n" + vet + "  - name: x\n    cmd: [x, \"{pkgs}\"]\n    format: lines\n",
			"5: check x: unknown substitution {pkgs}",
		},
		{
			"checks:\n  - name: x\n    cmd: [x, \"-p={packages}\"]\n    format: lines\n",
			"2: check x: {packages} stands for several arguments",
		},
		{
			"checks:\n  - name: x\n    cmd: [\"{bin}\"]\n    format: lines\n",
			"2: check x: {bin} needs a name: {bin:NAME}",
		},
		{
			"checks:\n  - name: x\n    cmd: [x, \"{module:y}\"]\n    format: lines\n",
			"2: check x: {module:y} takes no name",
		},
		{
			"checks:\n  - name: x\n    cmd: [x, \"{file:../go.mod}\"]\n    format: lines\n",
			"2: check x: {file:../go.mod}: the file is to be in the module root",
		},
		{
			"checks:\n  - name: x\n    cmd: [x]\n    format: lines\n    go: \"one\"\n",
			`2: check x: go: "one" is no Go version`,
		},
		{
			"checks:\n  - name: x\n    cmd: [x]\n    format: lines\n    of: true\n",
			"5: field of not found in a check",
		},
		{"editor: \"x://{file}\"\nchecks:\n" + vet, " editor: unknown {file}"},
		{"checks: [\n", "1: did not find expected node content"},
	}
	for _, tt := range tests {
		_, err := parseConfig([]byte(tt.yaml), "go-audit.yml")
		if err == nil || !strings.HasPrefix(err.Error(), "go-audit.yml:"+tt.want) {
			t.Errorf("parseConfig(%q):\n  err  %v\n  want go-audit.yml:%s…", tt.yaml, err, tt.want)
		}
	}

	// Braces that are no substitution stay as they are.
	ok := "checks:\n  - name: list\n    cmd: [go, list, -f, \"{{.Dir}}\", \"{packages}\"]\n    format: lines\n"
	if _, err := parseConfig([]byte(ok), "go-audit.yml"); err != nil {
		t.Errorf("a go list template: %v", err)
	}
}

func TestShowConfig(t *testing.T) {
	store := t.TempDir()
	var out bytes.Buffer
	if err := showConfig(&out, store); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"config:    built-in; go-audit config -init puts a copy in " + store,
		"editor:    none",
		"golangci:  built-in; go-audit config -init puts a copy in " + store,
		"  fix               vet-json     go 1.26  go vet -vettool={gotooldir}/fix -json {packages}",
		"  golangci-project  golangci              golangci-lint run {project-golangci} ",
		"  capslock          diff         off      capslock",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("config of the built-in one:\n%s\nwant a line with %q", out.String(), want)
		}
	}
	if strings.Contains(out.String(), "yours lacks") {
		t.Errorf("the built-in config is compared with itself:\n%s", out.String())
	}

	// One's own config, with vet changed and most built-in checks missing.
	writeFile(t, filepath.Join(store, configName), `checks:
  - name: vet
    cmd: [go, vet, "{packages}"]
    format: vet-json
    note: mine
  - name: gofmt
    cmd: [gofmt, -l, -s, "{files}"]
    format: lines
    note: gofmt -s -w <file> formats a file
`)
	writeFile(t, filepath.Join(store, golangciName), "version: \"2\"\n")
	out.Reset()
	if err := showConfig(&out, store); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"config:    " + filepath.Join(store, configName),
		"golangci:  " + filepath.Join(store, golangciName) + " (the built-in one differs)",
		"has checks yours lacks: fix, staticcheck, golangci-lint, golangci-project, exhaustruct, " +
			"gosec, nilaway, deadcode, govulncheck, go-licenses, gostyle, gopls, tidy, shadow, capslock",
		"describes otherwise: vet (cmd, note)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("config of one's own:\n%s\nwant a line with %q", out.String(), want)
		}
	}
}

func TestInitConfig(t *testing.T) {
	store := filepath.Join(t.TempDir(), "not-yet")
	var out bytes.Buffer
	if err := initConfig(&out, store); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{configName: builtinConfig, golangciName: builtinGolangci}
	for name, want := range files {
		path := filepath.Join(store, name)
		if got, _ := os.ReadFile(path); !bytes.Equal(got, want) {
			t.Errorf("config -init wrote %d bytes into %s, want the built-in one", len(got), name)
		}
		if !strings.Contains(out.String(), "wrote "+path) {
			t.Errorf("config -init printed %q, no line for %s", out.String(), name)
		}
	}

	// One's own files are never overwritten; a missing one is written.
	path := filepath.Join(store, configName)
	writeFile(t, path, "mine\n")
	if err := os.Remove(filepath.Join(store, golangciName)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := initConfig(&out, store); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "mine\n" {
		t.Errorf("config -init overwrote one's own config with %d bytes", len(got))
	}
	if !strings.Contains(out.String(), "kept "+path) ||
		!strings.Contains(out.String(), "wrote "+filepath.Join(store, golangciName)) {
		t.Errorf("config -init over one's own config printed %q", out.String())
	}

	// The standard set is a config of golangci-lint v2.
	var golangci struct{ Version string }
	if err := yaml.Unmarshal(builtinGolangci, &golangci); err != nil || golangci.Version != "2" {
		t.Errorf("golangci.yml: version %q, %v; want 2", golangci.Version, err)
	}
}
