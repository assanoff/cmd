// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The tests here run the built-in checks with the real tools over modules
// made for them, as tests/go-audit.sh of dev-env did for the bash go-audit.
// A check whose tool is not in PATH is to be skipped, with what to install.

// foundFiles is a module with a problem for each tool; the comments name
// the tools.
var foundFiles = map[string]string{
	"a.go": `// Package found has one problem for every tool.
package found

import (
	"crypto/md5"
	"fmt"
	"os"
)

// Store keeps a count.
type Store struct{ n int }

// GetCount returns the count. (gostyle: getters)
func (s *Store) GetCount() int { return s.n }

// Label formats x. (vet: printf, staticcheck: SA5009)
func Label(x int) string { return fmt.Sprintf("%d %s", x) }

// Sum hashes b with MD5. (gosec: G401, G501)
func Sum(b []byte) [16]byte { return md5.Sum(b) }

// Clean removes a file and drops the error. (golangci-lint: errcheck)
func Clean() { os.Remove("x") }

// Deref reads through a pointer that is always nil. (nilaway)
func Deref() int {
	var p *int
	return *p
}
`,
	"cmd/app/main.go": `package main

import "example.com/m"

func main() { _ = found.Sum(nil) }

// unused is never called. (deadcode, staticcheck: U1000)
func unused() {}
`,
}

// oneFile is a package with nothing to report.
const oneFile = `// Package clean has nothing to report.
package clean

// One returns one.
func One() int {
	return 1
}
`

// toolAudit makes a module of the files, with a store of its own, and goes
// into it. The go command works offline, from the module cache.
func toolAudit(t *testing.T, files map[string]string) (root, store string) {
	t.Helper()
	if testing.Short() {
		t.Skip("runs the tools")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go in PATH")
	}
	root = testModule(t, files)
	store = t.TempDir()
	t.Setenv("GO_STYLE_HOME", store)
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "")
	t.Chdir(root)
	return root, store
}

// lastAudit returns the newest report in the store, as its JSON has it.
func lastAudit(t *testing.T, store string) *auditReport {
	t.Helper()
	reports, err := loadReports(store)
	if err != nil || len(reports) == 0 {
		t.Fatalf("%d reports in the store, %v", len(reports), err)
	}
	data, err := os.ReadFile(strings.TrimSuffix(reports[0].path, ".md") + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var rep auditReport
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	return &rep
}

// checkOf returns how a check went in the report, and its findings.
func checkOf(t *testing.T, rep *auditReport, name string) (checkReport, []finding) {
	t.Helper()
	i := slices.IndexFunc(rep.Checks, func(c checkReport) bool {
		return c.Name == name
	})
	if i < 0 {
		t.Fatalf("no check %s in the report", name)
	}
	var fs []finding
	for _, f := range rep.Findings {
		if f.Check == name {
			fs = append(fs, f)
		}
	}
	return rep.Checks[i], fs
}

// hasRule reports whether one of the findings has the rule.
func hasRule(fs []finding, rule string) bool {
	for _, f := range fs {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

// installed reports whether the program of a built-in check is in PATH.
func installed(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// projectConfig returns the golangci-lint config a module in the current
// directory would find above it, such as one in the home directory.
func projectConfig(t *testing.T) string {
	t.Helper()
	a := testAudit(t, ".")
	path, err := a.projectGolangci()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestToolsFound(t *testing.T) {
	_, store := toolAudit(t, foundFiles)
	tests := []struct {
		check, tool string
		min         int    // findings, at least
		rule        string // the rule of one of them
	}{
		{"vet", "go", 1, "printf"},
		{"staticcheck", "staticcheck", 2, "SA5009"},
		{"golangci-lint", "golangci-lint", 1, "errcheck"},
		{"gosec", "gosec", 1, "G401"},
		{"nilaway", "nilaway", 1, "nilaway"},
		{"deadcode", "deadcode", 1, ""},
		{"gostyle", "gostyle", 1, "getters"},
	}
	var only []string
	for _, tt := range tests {
		only = append(only, tt.check)
	}
	only = append(only, "golangci-project", "go-licenses")
	out, err := auditOut(t, t.Context(), 4, strings.Join(only, ","))
	if !errors.Is(err, exitStatus(1)) && !errors.Is(err, exitStatus(exitFailure)) {
		t.Fatalf("audit of a module with problems: err = %v\n%s", err, out)
	}
	rep := lastAudit(t, store)
	for _, tt := range tests {
		c, fs := checkOf(t, rep, tt.check)
		switch {
		case !installed(tt.tool):
			if c.Status != statusSkipped || !strings.Contains(c.Note, "go install") {
				t.Errorf("%s with no %s: %s %q, want skipped with what to install", tt.check, tt.tool, c.Status, c.Note)
			}
		case tt.check == "gosec" && c.Status == statusError && strings.Contains(c.Note, "update gosec"):
			t.Logf("gosec: %s", c.Note) // a gosec built with an older Go than this one
		case c.Status != statusFound || len(fs) < tt.min:
			t.Errorf("%s: %s with %d findings (%s), want found, %d or more\n%s",
				tt.check, c.Status, len(fs), c.Note, tt.min, out)
		case tt.rule != "" && !hasRule(fs, tt.rule):
			t.Errorf("%s: no finding of %s in %+v", tt.check, tt.rule, fs)
		}
	}
	if installed("golangci-lint") && projectConfig(t) == "" {
		if c, _ := checkOf(t, rep, "golangci-project"); c.Status != statusSkipped || c.Note != "no .golangci.yml" {
			t.Errorf("golangci-project with no config: %s %q", c.Status, c.Note)
		}
	}
	// The module's own packages have no license file, and it has no
	// dependencies.
	if c, _ := checkOf(t, rep, "go-licenses"); installed("go-licenses") && c.Status != statusOK {
		t.Errorf("go-licenses of a module with no dependencies: %s %q", c.Status, c.Note)
	}
	// golangci-lint gives the line of code.
	if _, fs := checkOf(t, rep, "golangci-lint"); len(fs) > 0 && fs[0].Source == "" {
		t.Errorf("a finding of golangci-lint has no line of code: %+v", fs[0])
	}
}

func TestToolsClean(t *testing.T) {
	toolAudit(t, map[string]string{"a.go": oneFile})
	store := os.Getenv("GO_STYLE_HOME")
	out, err := auditOut(t, t.Context(), 4, "")
	rep := lastAudit(t, store)
	offline := false
	for _, c := range rep.Checks {
		switch {
		case c.Name == "govulncheck" && c.Status == statusError && strings.Contains(c.Note, "unreachable"):
			offline = true // vuln.go.dev is out of reach; the rest need no network
		case c.Status == statusFound || c.Status == statusError:
			t.Errorf("%s on a clean module: %s %q\n%s", c.Name, c.Status, c.Note, out)
		}
	}
	if err != nil && !offline {
		t.Errorf("audit of a clean module: err = %v\n%s", err, out)
	}
	if c, _ := checkOf(t, rep, "deadcode"); installed("deadcode") &&
		(c.Status != statusSkipped || c.Note != "no main package and no tests") {
		t.Errorf("deadcode with no main and no tests: %s %q", c.Status, c.Note)
	}
}

func TestToolsBroken(t *testing.T) {
	toolAudit(t, map[string]string{"a.go": `package broken

func F() int {
	return "s"
}
`})
	store := os.Getenv("GO_STYLE_HOME")
	checks := []string{"vet", "fix", "staticcheck", "golangci-lint", "gosec", "nilaway", "deadcode", "govulncheck", "gostyle"}
	out, err := auditOut(t, t.Context(), 4, strings.Join(checks, ","))
	rep := lastAudit(t, store)
	failed := false
	for _, name := range checks {
		c, _ := checkOf(t, rep, name)
		switch c.Status {
		case statusSkipped:
			continue
		case statusError:
			failed = true
			if !strings.Contains(c.Note, "cannot use") {
				t.Errorf("%s on a package that does not build: %q, want the compile error", name, c.Note)
			}
		default:
			t.Errorf("%s on a package that does not build: %s %q", name, c.Status, c.Note)
		}
	}
	if failed && !errors.Is(err, exitStatus(exitFailure)) {
		t.Errorf("audit of a package that does not build: err = %v\n%s", err, out)
	}
}

func TestToolsGovulncheck(t *testing.T) {
	if !installed("govulncheck") {
		t.Skip("no govulncheck in PATH")
	}
	_, store := toolAudit(t, map[string]string{"a.go": "package clean\n"})

	// vuln.go.dev behind a proxy that refuses: offline, without having to be.
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	out, err := auditOut(t, t.Context(), 1, "govulncheck")
	c, _ := checkOf(t, lastAudit(t, store), "govulncheck")
	if !errors.Is(err, exitStatus(exitFailure)) || c.Status != statusError ||
		!strings.HasSuffix(c.Note, "unreachable (offline? -skip govulncheck)") {
		t.Errorf("govulncheck offline: err %v, %s %q\n%s", err, c.Status, c.Note, out)
	}
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("HTTP_PROXY", "")

	// golang.org/x/text v0.3.5: language.Parse has GO-2021-0113, fixed in
	// v0.3.7; three more of its vulnerabilities are in code not called.
	writeFile(t, "a.go", `// Package vuln calls a function with a known vulnerability.
package vuln

import "golang.org/x/text/language"

// Tag parses s.
func Tag(s string) (language.Tag, error) { return language.Parse(s) }
`)
	t.Setenv("GOPROXY", "")
	if out, err := exec.Command("go", "get", "golang.org/x/text@v0.3.5").CombinedOutput(); err != nil {
		t.Skipf("cannot get golang.org/x/text@v0.3.5: %v\n%s", err, out)
	}
	out, err = auditOut(t, t.Context(), 1, "govulncheck")
	c, fs := checkOf(t, lastAudit(t, store), "govulncheck")
	if c.Status == statusError && strings.Contains(c.Note, "unreachable") {
		t.Skipf("govulncheck: %s", c.Note)
	}
	if !errors.Is(err, exitStatus(1)) || len(fs) != 1 || fs[0].Rule != "GO-2021-0113" ||
		fs[0].File != "a.go" || fs[0].Line != 7 || !strings.Contains(c.Note, "more in dependencies, not called") {
		t.Errorf("govulncheck of a called vulnerability: err %v, %s %q, %+v\n%s", err, c.Status, c.Note, fs, out)
	}
}

// configuredFiles is a module with a misspelling, which only a linter out
// of the standard set finds, and an unchecked error, which errcheck of the
// standard set finds.
var configuredFiles = map[string]string{
	"a.go": `// Package configured has a word misspelled: recieve.
package configured

import "os"

// Clean removes a file and drops the error.
func Clean() { os.Remove("x") }
`,
	".golangci.yml": "version: \"2\"\nlinters:\n  enable: [misspell]\n",
}

func TestToolsGolangci(t *testing.T) {
	if !installed("golangci-lint") {
		t.Skip("no golangci-lint in PATH")
	}
	_, store := toolAudit(t, configuredFiles)
	rules := func(fs []finding) []string {
		var rs []string
		for _, f := range fs {
			rs = append(rs, f.Rule)
		}
		slices.Sort(rs)
		return slices.Compact(rs)
	}

	// Both at once, as -c 2 runs them.
	out, err := auditOut(t, t.Context(), 2, "golangci-lint,golangci-project")
	if !errors.Is(err, exitStatus(1)) {
		t.Fatalf("both golangci checks: err = %v\n%s", err, out)
	}
	rep := lastAudit(t, store)
	if c, fs := checkOf(t, rep, "golangci-project"); c.Status != statusFound ||
		!slices.Equal(rules(fs), []string{"errcheck", "misspell"}) {
		t.Errorf("golangci-project with misspell enabled: %s, rules %v\n%s", c.Status, rules(fs), out)
	}
	if c, fs := checkOf(t, rep, "golangci-lint"); c.Status != statusFound ||
		!slices.Equal(rules(fs), []string{"errcheck"}) {
		t.Errorf("golangci-lint, the standard set: %s, rules %v\n%s", c.Status, rules(fs), out)
	}

	// The golangci.yml of the store changes the set of golangci-lint.
	writeFile(t, filepath.Join(store, golangciName), "version: \"2\"\nlinters:\n  default: none\n  enable: [misspell]\n")
	out, err = auditOut(t, t.Context(), 1, "golangci-lint")
	if c, fs := checkOf(t, lastAudit(t, store), "golangci-lint"); !errors.Is(err, exitStatus(1)) ||
		!slices.Equal(rules(fs), []string{"misspell"}) {
		t.Errorf("golangci-lint with the golangci.yml of the store: %s, rules %v\n%s", c.Status, rules(fs), out)
	}

	// The example of the standard set is the standard set.
	writeFile(t, filepath.Join(store, golangciName), string(builtinGolangci))
	out, _ = auditOut(t, t.Context(), 1, "golangci-lint")
	if c, fs := checkOf(t, lastAudit(t, store), "golangci-lint"); !slices.Equal(rules(fs), []string{"errcheck"}) {
		t.Errorf("golangci-lint with golangci.yml of go-audit: %s %q, rules %v\n%s", c.Status, c.Note, rules(fs), out)
	}
}

func TestToolsCapslock(t *testing.T) {
	if !installed("capslock") {
		t.Skip("no capslock in PATH")
	}
	root, store := toolAudit(t, map[string]string{"a.go": oneFile})
	capslock := func() (checkReport, []finding) {
		t.Helper()
		out, _ := auditOut(t, t.Context(), 1, "capslock")
		c, fs := checkOf(t, lastAudit(t, store), "capslock")
		t.Logf("capslock: %s %q\n%s", c.Status, c.Note, out)
		return c, fs
	}

	if c, _ := capslock(); c.Status != statusSkipped || c.Note != "no capslock.json" {
		t.Errorf("capslock with no baseline: %s %q", c.Status, c.Note)
	}

	baseline, err := exec.Command("capslock", "-packages=./...", "-output=json").Output()
	if err != nil {
		t.Fatalf("capslock baseline: %v", err)
	}
	writeFile(t, filepath.Join(root, "capslock.json"), string(baseline))
	if c, _ := capslock(); c.Status != statusOK {
		t.Errorf("capslock against its own baseline: %s %q", c.Status, c.Note)
	}

	writeFile(t, filepath.Join(root, "a.go"), `package caps

import "os/exec"

// Run runs true.
func Run() error {
	return exec.Command("true").Run()
}
`)
	if c, fs := capslock(); c.Status != statusFound || len(fs) != 1 || !strings.Contains(fs[0].Message, "new capability EXEC") {
		t.Errorf("capslock with a new EXEC: %s %q, %+v", c.Status, c.Note, fs)
	}
}
