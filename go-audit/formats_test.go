// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The samples in testdata/formats are what the tools printed, on Go 1.27.2,
// for a module with a problem for each and for one that does not build:
// <name>.stdout, <name>.stderr and <name>.code, the exit code. $ROOT stands
// for the module root, /elsewhere for a directory outside it.

// sample reads a sample with root in place of $ROOT.
func sample(t *testing.T, name, root string) *output {
	t.Helper()
	read := func(ext string) []byte {
		data, err := os.ReadFile(filepath.Join("testdata", "formats", name+"."+ext))
		if err != nil {
			t.Fatal(err)
		}
		return []byte(strings.ReplaceAll(string(data), "$ROOT", root))
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(read("code"))))
	if err != nil {
		t.Fatal(err)
	}
	return newOutput(read("stdout"), read("stderr"), code, root)
}

// brief is a finding as the tests compare it: position, rule and message.
func brief(f finding) string {
	return strings.TrimSpace(fmt.Sprintf("%s %s: %s", position(f), f.Rule, f.Message))
}

func TestFormats(t *testing.T) {
	tests := []struct {
		sample, format string
		status         string
		findings       []string // prefixes of brief(finding), in order
		note           string   // part of the note
	}{
		{
			"vet", "vet-json", statusFound,
			[]string{`main.go:37:14 printf: fmt.Printf format %d has arg "x" of wrong type string`},
			"",
		},
		{
			"vet-broken", "vet-json", statusError, nil,
			"vet: ./b.go:3:23: undefined: undefinedThing",
		},
		{
			"fix", "vet-json", statusFound,
			[]string{"main.go:10:20 any: interface{} can be replaced by any"},
			"",
		},
		{
			"fix-broken", "vet-json", statusError, nil,
			"fix: ./b.go:3:23: undefined: undefinedThing",
		},
		{
			"nilaway", "vet-json", statusFound,
			[]string{"main.go:31:10 nilaway: Potential nil panic detected."},
			"",
		},
		// The real error is on stderr; the JSON only says that the analyzers
		// could not run.
		{"nilaway-broken", "vet-json", statusError, nil, "b.go:3:23: undefined: undefinedThing"},
		{"gostyle", "vet-json", statusFound, []string{
			"main.go:10:18 useany: [gostyle.useany]",
			"main.go:34:1 getters: [gostyle.getters]",
			"main.go:38:10 handlerrors: [gostyle.handlerrors]",
		}, ""},
		{"gopls", "lines", statusFound, []string{
			`main.go:19:7 : bufio.Scanner "s" is used in Scan loop`,
			`main.go:37:14 : fmt.Printf format %d has arg "x" of wrong type string`,
		}, ""},
		{
			"gopls-broken", "lines", statusFound,
			[]string{"b.go:3:23 : undefined: undefinedThing"},
			"",
		},
		{"gopls-missing", "lines", statusError, nil, "no such file or directory"},
		{"gofmt", "lines", statusFound, []string{"inner/inner.go :"}, ""},
		// gofmt lists the files it read and fails on the one it could not.
		{
			"gofmt-syntax", "lines", statusError,
			[]string{"inner/inner.go :"},
			"/elsewhere/bad.go:2:6: expected 'IDENT', found '{'",
		},
		{"tidy", "diff", statusFound, []string{": diff current/go.mod tidy/go.mod"}, ""},
		{"tidy-clean", "diff", statusOK, nil, ""},

		{"staticcheck", "staticcheck", statusFound, []string{
			"a.go:17:47 SA5009: Printf format %s reads arg #2, but call has only 1 args",
			"cmd/app/main.go:8:6 U1000: func unused is unused",
		}, ""},
		{"staticcheck-broken", "staticcheck", statusError, nil, "./a.go:3:23: cannot use"},
		{"staticcheck-badpkg", "staticcheck", statusError, nil, "pattern ./nosuch/...: lstat"},

		{"golangci", "golangci", statusFound, []string{
			"a.go:17:51 govet: printf: fmt.Sprintf format %s reads arg #2",
			"a.go:23:25 errcheck: Error return value of `os.Remove` is not checked",
			"cmd/app/main.go:8:6 unused: func unused is unused",
		}, ""},
		{"golangci-broken", "golangci", statusError, nil, "./a.go:3:23: cannot use"},
		{"golangci-badconfig", "golangci", statusError, nil, "Error: unknown linters: 'nosuchlinter'"},

		// The issues of gosec, with the errors of an old gosec taken out.
		{"gosec", "gosec", statusFound, []string{
			"a.go:5:2 G501: Blocklisted import crypto/md5",
			"a.go:20:38 G401: Use of weak cryptographic primitive",
		}, ""},
		// A gosec built with an older Go than the module's.
		{"gosec-old", "gosec", statusError, nil, "cannot read go1.27.2 packages: update gosec"},
		{"gosec-broken", "gosec", statusError, nil, "./a.go:3:23: cannot use"},
		{"gosec-nopkg", "gosec", statusError, nil, "No packages found"},

		{"deadcode", "deadcode", statusFound, []string{
			"a.go:14:17 : unreachable func: Store.GetCount",
			"a.go:17:6 : unreachable func: Label",
			"a.go:23:6 : unreachable func: Clean",
			"a.go:26:6 : unreachable func: Deref",
			"cmd/app/main.go:8:6 : unreachable func: unused",
		}, ""},
		{"deadcode-nomain", "deadcode", statusSkipped, nil, "no main package and no tests"},
		{"deadcode-broken", "deadcode", statusError, nil, "a.go:3:23: cannot use"},

		// One vulnerability is called, three more are in the module the
		// code requires.
		{"govulncheck", "govulncheck", statusFound, []string{
			"a.go:7:65 GO-2021-0113: Out-of-bounds read in golang.org/x/text/language: " +
				"golang.org/x/text/language.Parse is called; fixed in golang.org/x/text@v0.3.7",
		}, "3 more in dependencies, not called"},
		{
			"govulncheck-offline", "govulncheck", statusError, nil,
			"vuln.go.dev unreachable (offline? -skip govulncheck)",
		},
		{"govulncheck-broken", "govulncheck", statusError, nil, "a.go:3:23: cannot use"},

		// The libraries of ai with no license file go-licenses knows, among
		// its warnings.
		{"go-licenses", "go-licenses", statusFound, []string{
			"unknown: github.com/hybridgroup/yzma/exp/speculative: no license found",
			"unknown: github.com/hybridgroup/yzma/pkg/download: no license found",
			"unknown: github.com/hybridgroup/yzma/pkg/llama: no license found",
			"unknown: github.com/hybridgroup/yzma/pkg/loader: no license found",
			"unknown: github.com/hybridgroup/yzma/pkg/mtmd: no license found",
			"unknown: github.com/hybridgroup/yzma/pkg/utils: no license found",
		}, ""},
		// The other two lines of go-licenses check, as its check.go prints
		// them: no module at hand has such a license.
		{"go-licenses-types", "go-licenses", statusFound, []string{
			"AGPL-3.0: example.com/agpl: license AGPL-3.0 of the type Forbidden is not allowed",
			"GPL-3.0: example.com/gpl: license GPL-3.0 is not allowed",
		}, ""},
		{
			"go-licenses-bad", "go-licenses", statusError, nil,
			"./nosuch/...: -: pattern ./nosuch/...: lstat",
		},
		// Warnings alone, with the exit code of a library not allowed.
		{"go-licenses-noise", "go-licenses", statusError, nil, "exit status 1"},
	}
	root := t.TempDir()
	for _, tt := range tests {
		t.Run(tt.sample, func(t *testing.T) {
			o := sample(t, tt.sample, root)
			// As in the run of govulncheck over example.com/vuln.
			o.module, o.goVersion = "example.com/vuln", "go1.27.2"
			fs, note, ferr := formats[tt.format](o)
			sortFindings(fs)
			status, note := judge(o, fs, note, ferr, fmt.Errorf("exit status %d", o.code))
			if status != tt.status {
				t.Errorf("status = %s (%s), want %s", status, note, tt.status)
			}
			if !strings.Contains(note, tt.note) {
				t.Errorf("note = %q, want it to contain %q", note, tt.note)
			}
			if len(fs) != len(tt.findings) {
				t.Fatalf("%d findings, want %d: %+v", len(fs), len(tt.findings), fs)
			}
			for i, want := range tt.findings {
				if got := brief(fs[i]); !strings.HasPrefix(got, want) {
					t.Errorf("finding %d = %q, want it to start with %q", i, got, want)
				}
			}
		})
	}
}

func TestVetJSONErrorOfAnalyzer(t *testing.T) {
	// An analyzer that fails on a package that builds is an error too, with
	// the exit code 0 and nothing on stderr.
	o := newOutput([]byte(`{"example.com/m": {"printf": [], "shadow": {"error": "boom"}}}`),
		nil, 0, "/src/m")
	_, _, err := vetJSON(o)
	if err == nil || err.Error() != "boom" {
		t.Errorf("vetJSON with a failed analyzer: err = %v, want boom", err)
	}

	o = newOutput([]byte("{\n\t\"example.com/m\": [\n"), nil, 0, "/src/m")
	if _, _, err := vetJSON(o); err == nil || !strings.Contains(err.Error(), "unreadable JSON") {
		t.Errorf("vetJSON of broken JSON: err = %v, want unreadable JSON", err)
	}
}

func TestDiffWithoutOutput(t *testing.T) {
	// capslock -output=compare without its baseline: exit 2, nothing on
	// stdout, the reason on stderr.
	o := newOutput(nil, []byte("2026/10/16 16:56:36 Comparison file should include output\n"),
		2, "/src/m")
	fs, note, ferr := diffFinding(o)
	status, note := judge(o, fs, note, ferr, errors.New("exit status 2"))
	if status != statusError || !strings.Contains(note, "Comparison file") {
		t.Errorf("diff with no output and exit 2 = %s %q, want an error with the reason", status, note)
	}
}

func TestSplitPos(t *testing.T) {
	tests := []struct {
		pos       string
		file      string
		line, col int
	}{
		{"/src/m/a.go:10:2", "/src/m/a.go", 10, 2},
		{"a.go:10", "a.go", 10, 0},
		{`C:\src\m\a.go:7:1`, `C:\src\m\a.go`, 7, 1},
		{"a.go", "a.go", 0, 0},
		{"", "", 0, 0},
	}
	for _, tt := range tests {
		file, line, col := splitPos(tt.pos)
		if file != tt.file || line != tt.line || col != tt.col {
			t.Errorf("splitPos(%q) = %q, %d, %d; want %q, %d, %d",
				tt.pos, file, line, col, tt.file, tt.line, tt.col)
		}
	}
}

func TestOutputPaths(t *testing.T) {
	// The root through a link, as a temporary directory on macOS is.
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symbolic links:", err)
	}
	o := newOutput(nil, nil, 0, link)
	real, _ = filepath.EvalSymlinks(real)
	for _, tt := range []struct{ path, want string }{
		{filepath.Join(link, "a", "b.go"), "a/b.go"},
		{filepath.Join(real, "a", "b.go"), "a/b.go"},
		{"./a/b.go", "a/b.go"},
		{filepath.Join(filepath.Dir(link), "elsewhere.go"), filepath.Join(filepath.Dir(link), "elsewhere.go")},
	} {
		if got := o.rel(tt.path); got != tt.want {
			t.Errorf("rel(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
	text := "# example.com/m\nvet: lead-in:\n\n  " + filepath.Join(real, "a.go") + ":3: oops\nmore\n"
	if got := o.firstError([]byte(text)); got != "a.go:3: oops" {
		t.Errorf("firstError = %q, want the line after the header and the lead-in, root cut", got)
	}
	if got := o.firstError([]byte("# only a header\n")); got != "" {
		t.Errorf("firstError of a header alone = %q, want none", got)
	}
}

func TestShorten(t *testing.T) {
	if got := shorten("привет, мир", 7); got != "привет…" {
		t.Errorf("shorten by runes = %q", got)
	}
	if got := shorten("short", 7); got != "short" {
		t.Errorf("shorten of a short string = %q", got)
	}
}

func TestGolangciSource(t *testing.T) {
	// The line of code comes from golangci-lint, and addSource keeps it
	// when the file says otherwise by now.
	root := t.TempDir()
	fs, _, err := golangciJSON(sample(t, "golangci", root))
	if err != nil || len(fs) != 3 {
		t.Fatalf("golangciJSON = %d findings, %v", len(fs), err)
	}
	writeFile(t, filepath.Join(root, "a.go"), "package found\n")
	results := []*result{{findings: fs}}
	addSource(root, results)
	for _, f := range results[0].findings {
		if f.Line == 23 && f.Source != `func Clean() { os.Remove("x") }` {
			t.Errorf("the source of %s = %q, want that of golangci-lint", brief(f), f.Source)
		}
	}
}

func TestUnreadableJSON(t *testing.T) {
	for _, name := range []string{"staticcheck", "golangci", "gosec", "deadcode", "govulncheck"} {
		o := newOutput([]byte("{\"x\": [\n"), nil, 1, "/src/m")
		if _, _, err := formats[name](o); err == nil || !strings.Contains(err.Error(), "unreadable JSON") {
			t.Errorf("%s of broken JSON: err = %v, want unreadable JSON", name, err)
		}
	}
}

func TestGosecOfUnknownGo(t *testing.T) {
	o := sample(t, "gosec-old", t.TempDir())
	_, _, err := gosecJSON(o)
	if err == nil || err.Error() != "cannot read this Go's packages: update gosec" {
		t.Errorf("gosec with no Go version known: err = %v", err)
	}
}

func TestVulnFinding(t *testing.T) {
	o := newOutput(nil, nil, 0, "/src/m")
	o.module = "example.com/m"
	trace := []vulnFrame{{Module: "example.com/dep", Package: "example.com/dep/x", Function: "Get", Receiver: "*Client"}}
	f := vulnFinding(o, "GO-2099-0001", "", trace)
	want := finding{Rule: "GO-2099-0001", Message: ": example.com/dep/x.Client.Get is called; no fixed version"}
	if f != want {
		t.Errorf("a method with no fix, reached from no line of the module:\n got %+v\nwant %+v", f, want)
	}
}
