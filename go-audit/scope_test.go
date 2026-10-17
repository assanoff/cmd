// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestParseDiff(t *testing.T) {
	diff := `diff --git a/x.go b/x.go
index 1111111..2222222 100644
--- a/x.go
+++ b/x.go
@@ -1,0 +2,2 @@ package m
+// a
+++ not a header
@@ -5 +7 @@ func A() {}
-x
+y
diff --git a/gone.go b/gone.go
deleted file mode 100644
--- a/gone.go
+++ /dev/null
@@ -1,3 +0,0 @@
-a
diff --git "a/q\"q.go" "b/q\"q.go"
--- "a/q\"q.go"
+++ "b/q\"q.go"
@@ -2 +2 @@
-a
+b
diff --git a/sp ace.go b/sp ace.go
--- a/sp ace.go	
+++ b/sp ace.go	
@@ -3,2 +2,0 @@
-a
-b
`
	want := map[string][]hunk{
		"x.go":      {{1, 0, 2, 2}, {5, 1, 7, 1}},
		`q"q.go`:    {{2, 1, 2, 1}},
		"sp ace.go": {{3, 2, 2, 0}},
	}
	if got := parseDiff(diff); !reflect.DeepEqual(got, want) {
		t.Errorf("parseDiff = %v, want %v", got, want)
	}
}

func TestToWorktree(t *testing.T) {
	tests := []struct {
		name  string
		lines []lineRange
		hunks []hunk
		want  []lineRange
	}{
		{"no changes since", []lineRange{{3, 5}}, nil, []lineRange{{3, 5}}},
		{"a line put on top", []lineRange{{1, 10}}, []hunk{{0, 0, 1, 1}}, []lineRange{{2, 11}}},
		{
			"added, changed and removed around",
			[]lineRange{{1, 10}},
			// Two lines after line 1, line 3 changed, lines 6 and 7 removed.
			[]hunk{{1, 0, 2, 2}, {3, 1, 5, 1}, {6, 2, 7, 0}},
			[]lineRange{{1, 1}, {4, 4}, {6, 10}},
		},
		{"changed again", []lineRange{{3, 3}}, []hunk{{3, 1, 3, 1}}, nil},
		{"after a hunk below", []lineRange{{2, 2}}, []hunk{{4, 1, 4, 3}}, []lineRange{{2, 2}}},
	}
	for _, tt := range tests {
		if got := toWorktree(tt.lines, tt.hunks); !slices.Equal(got, tt.want) {
			t.Errorf("%s: toWorktree(%v, %v) = %v, want %v",
				tt.name, tt.lines, tt.hunks, got, tt.want)
		}
	}
}

func TestScope(t *testing.T) {
	code := finding{File: "a.go", Line: 5}
	test := finding{File: "a_test.go", Line: 5}
	module := finding{Message: "go.mod is not tidy"}
	whole := finding{File: "a.go"}
	other := finding{File: "b.go", Line: 1}
	tests := []struct {
		name  string
		scope scope
		in    []finding
	}{
		{"all", scope{}, []finding{code, test, module, whole, other}},
		{"no tests", scope{noTests: true}, []finding{code, module, whole, other}},
		{"files", scope{files: map[string]bool{"a.go": true}}, []finding{code, whole}},
		{
			"changed lines",
			scope{changes: map[string][]lineRange{"a.go": {{4, 6}}, "b.go": {{9, 9}}}},
			[]finding{code, whole},
		},
		{
			"whole files",
			scope{changes: map[string][]lineRange{"b.go": {{9, 9}}}, whole: true},
			[]finding{other},
		},
	}
	all := []finding{code, test, module, whole, other}
	for _, tt := range tests {
		var in []finding
		for _, f := range all {
			if tt.scope.has(f) {
				in = append(in, f)
			}
		}
		if !slices.Equal(in, tt.in) {
			t.Errorf("%s: in scope %v, want %v", tt.name, in, tt.in)
		}
	}

	r := &result{status: statusFound, findings: []finding{test}, note: "a note"}
	s := scope{noTests: true}
	s.filter(r)
	if r.status != statusOK || len(r.findings) != 0 || r.note != "a note · 1 out of scope" {
		t.Errorf("a check with only tests found: %+v", r)
	}
	r = &result{status: statusError, findings: []finding{test, code}}
	s.filter(r)
	if r.status != statusError || len(r.findings) != 1 || r.note != "1 out of scope" {
		t.Errorf("a failed check: %+v", r)
	}
}

// gitModule makes a module in the directory mod of a repository of its own,
// with the files committed and outside.go beside mod, and keeps the config
// of git on this machine out of the test. It returns the module root.
func gitModule(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git in PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "go-audit")
	t.Setenv("GIT_AUTHOR_EMAIL", "go-audit@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "go-audit")
	t.Setenv("GIT_COMMITTER_EMAIL", "go-audit@example.com")
	repo := t.TempDir()
	root := filepath.Join(repo, "mod")
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.21\n")
	for name, content := range files {
		writeFile(t, filepath.Join(root, filepath.FromSlash(name)), content)
	}
	writeFile(t, filepath.Join(repo, "outside.go"), "package outside\n")
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-q", "-m", "start")
	return root
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestGitChanges(t *testing.T) {
	root := gitModule(t, map[string]string{
		"a.go": "package m\n\nfunc A() {}\n\nfunc B() {}\n",
		"b.go": "package m\n\nfunc X() {}\n",
	})
	// Staged: line 5 of a.go becomes lines 5 and 6; line 3 of b.go changes.
	writeFile(t, filepath.Join(root, "a.go"),
		"package m\n\nfunc A() {}\n\nfunc B() int { return 1 }\nfunc C() {}\n")
	writeFile(t, filepath.Join(root, "b.go"), "package m\n\nfunc X() int { return 0 }\n")
	gitRun(t, root, "add", "a.go", "b.go")
	// Not staged: two lines after line 1 of a.go and line 3 changed, which
	// moves the staged lines to 7 and 8; line 3 of b.go changed again; a new
	// file.
	writeFile(t, filepath.Join(root, "a.go"),
		"package m\n// x\n// y\n\nfunc A() { _ = 0 }\n\nfunc B() int { return 1 }\nfunc C() {}\n")
	writeFile(t, filepath.Join(root, "b.go"), "package m\n\nfunc X() int { return 1 }\n")
	writeFile(t, filepath.Join(root, "new.go"), "package m\n")
	writeFile(t, filepath.Join(root, "notes.txt"), "not Go\n")
	// Changes outside the module, staged, not staged and new.
	writeFile(t, filepath.Join(root, "..", "outside.go"), "package outside\n\n// x\n")
	gitRun(t, root, "add", "../outside.go")
	writeFile(t, filepath.Join(root, "..", "outside.go"), "package outside\n\n// y\n")
	writeFile(t, filepath.Join(root, "..", "new.go"), "package outside\n")

	a := testAudit(t, root)
	tests := []struct {
		rev              string
		staged, unstaged bool
		want             map[string][]lineRange
	}{
		{"", true, false, map[string][]lineRange{"a.go": {{7, 8}}}},
		{"", false, true, map[string][]lineRange{
			"a.go": {{2, 3}, {5, 5}}, "b.go": {{3, 3}}, "new.go": {allLines},
		}},
		{"", true, true, map[string][]lineRange{
			"a.go": {{2, 3}, {5, 5}, {7, 8}}, "b.go": {{3, 3}}, "new.go": {allLines},
		}},
		{"HEAD", false, false, map[string][]lineRange{
			"a.go": {{2, 3}, {5, 5}, {7, 8}}, "b.go": {{3, 3}}, "new.go": {allLines},
		}},
	}
	// Settings of one's own that would change what git diff prints, and so
	// what go-audit reads, if it did not set the options itself; the first
	// two each change the prefixes of the paths, in ways of their own.
	for _, settings := range [][]string{
		{"diff.mnemonicPrefix", "true"},
		{"diff.noprefix", "true"},
		{"color.ui", "always", "diff.context", "5", "diff.relative", "true"},
	} {
		t.Run(settings[0], func(t *testing.T) {
			t.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(len(settings)/2))
			for i := 0; i < len(settings); i += 2 {
				t.Setenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", i/2), settings[i])
				t.Setenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", i/2), settings[i+1])
			}
			for _, tt := range tests {
				got, err := a.gitChanges(tt.rev, tt.staged, tt.unstaged)
				for _, rs := range got {
					slices.SortFunc(rs, func(a, b lineRange) int {
						return cmp.Compare(a.first, b.first)
					})
				}
				if err != nil || !maps.EqualFunc(got, tt.want, slices.Equal) {
					t.Errorf("gitChanges(%q, %v, %v) = %v, %v; want %v",
						tt.rev, tt.staged, tt.unstaged, got, err, tt.want)
				}
			}
		})
	}

	if _, err := a.gitChanges("no-such-branch", false, false); !isUsage(err) {
		t.Errorf("an unknown revision: err = %v, want a usage error", err)
	}
	outside := testAudit(t, t.TempDir())
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(outside.mod.root))
	if _, err := outside.gitChanges("", true, false); err == nil {
		t.Error("changes outside a git repository: no error")
	}
}

func isUsage(err error) bool {
	_, ok := errors.AsType[usageError](err)
	return ok
}

func TestTargets(t *testing.T) {
	root := testModule(t, map[string]string{
		"a.go":                         "package m\n",
		"internal/store/store.go":      "package store\n",
		"internal/store/store_test.go": "package store\n",
	})
	internal := filepath.Join(root, "internal")
	tests := []struct {
		dir   string
		args  []string
		pkgs  []string
		files []string
	}{
		{root, nil, []string{"./..."}, nil},
		{
			root, []string{"internal/store/store_test.go", "a.go", "./a.go"},
			[]string{".", "./internal/store"},
			[]string{"a.go", "internal/store/store_test.go"},
		},
		{internal, []string{"store/store.go"}, []string{"./internal/store"},
			[]string{"internal/store/store.go"}},
		{root, []string{"internal/store"}, []string{"./internal/store"}, nil},
		{root, []string{"internal/..."}, []string{"./internal/..."}, nil},
		{internal, []string{"store"}, []string{"./internal/store"}, nil},
		{root, []string{"example.com/m/x"}, []string{"example.com/m/x"}, nil},
	}
	for _, tt := range tests {
		pkgs, files, err := targets(tt.dir, root, tt.args)
		if err != nil || !slices.Equal(pkgs, tt.pkgs) || !slices.Equal(files, tt.files) {
			t.Errorf("targets(%s, %q) = %q, %q, %v; want %q, %q",
				tt.dir, tt.args, pkgs, files, err, tt.pkgs, tt.files)
		}
	}

	elsewhere := filepath.Join(t.TempDir(), "x.go")
	writeFile(t, elsewhere, "package x\n")
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"nope.go"}, "nope.go: no such file"},
		{[]string{"a.go", "./..."}, "files and packages do not mix"},
		{[]string{elsewhere}, "is outside the module"},
	} {
		_, _, err := targets(root, root, tt.args)
		if !isUsage(err) || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("targets(%q): err = %v, want a usage error with %q", tt.args, err, tt.want)
		}
	}
}

// auditReportOf reads the JSON twin of the report whose label is label and
// returns it and the path of the report.
func auditReportOf(t *testing.T, store, label string) (*auditReport, string) {
	t.Helper()
	reports, err := loadReports(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reports {
		if r.Label != label {
			continue
		}
		data, err := os.ReadFile(strings.TrimSuffix(r.path, ".md") + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var rep auditReport
		if err := json.Unmarshal(data, &rep); err != nil {
			t.Fatal(err)
		}
		return &rep, r.path
	}
	t.Fatalf("no report %q in the store", label)
	return nil, ""
}

func TestAuditScope(t *testing.T) {
	store := helperSetup(t,
		[]string{"lint", "lines", "print", "a.go:1: in code\na_test.go:1: in a test"},
		[]string{"tidy", "diff", "print", "go.mod is not tidy"},
	)
	writeFile(t, "a_test.go", "package m\n")
	writeFile(t, "b.go", "package m\n")

	_, err := auditWith(t, t.Context(), auditOptions{concurrency: 1, noTests: true})
	if !errors.Is(err, exitStatus(1)) {
		t.Errorf("-no-tests: err = %v, want exit status 1", err)
	}
	rep, md := auditReportOf(t, store, "./... -no-tests")
	want := []checkReport{
		{Name: "lint", Status: statusFound, Count: 1, Note: "1 out of scope"},
		{Name: "tidy", Status: statusFound, Count: 1},
	}
	checkReports(t, "-no-tests", rep, want)
	if text, _ := os.ReadFile(md); !strings.Contains(string(text), "- **Scope:** `-no-tests`\n") {
		t.Errorf("-no-tests: the report does not show the scope:\n%s", text)
	}

	if _, err := auditWith(t, t.Context(), auditOptions{concurrency: 1}, "b.go"); err != nil {
		t.Errorf("b.go: err = %v, want none, as the findings are in other files", err)
	}
	rep, _ = auditReportOf(t, store, "b.go")
	want = []checkReport{
		{Name: "lint", Status: statusOK, Count: 0, Note: "2 out of scope"},
		{Name: "tidy", Status: statusOK, Count: 0, Note: "1 out of scope"},
	}
	checkReports(t, "b.go", rep, want)
	if !slices.Equal(rep.Packages, []string{"."}) || !slices.Equal(rep.Files, []string{"b.go"}) {
		t.Errorf("b.go: packages %q, files %q", rep.Packages, rep.Files)
	}

	o := auditOptions{concurrency: 1, noTests: true}
	out, err := auditWith(t, t.Context(), o, "a_test.go")
	if err != nil || !strings.Contains(out, "no Go files in scope, nothing to check") {
		t.Errorf("-no-tests a_test.go = %q, %v; want nothing to check", out, err)
	}
	if reports, _ := loadReports(store); len(reports) != 2 {
		t.Errorf("a run with nothing to check wrote a report: %d reports", len(reports))
	}
}

// checkReports compares the checks of a report with want, but for the time
// and the output files.
func checkReports(t *testing.T, name string, rep *auditReport, want []checkReport) {
	t.Helper()
	var got []checkReport
	for _, c := range rep.Checks {
		got = append(got, checkReport{Name: c.Name, Status: c.Status, Count: c.Count, Note: c.Note})
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s: checks %+v, want %+v", name, got, want)
	}
}

func TestAuditScopeUsage(t *testing.T) {
	helperSetup(t, []string{"fine", "diff", "print", ""})
	for _, tt := range []struct {
		o    auditOptions
		args []string
		want string
	}{
		{auditOptions{diff: "HEAD", staged: true}, nil, "-diff goes with neither"},
		{auditOptions{wholeFiles: true}, nil, "-whole-files goes with"},
		{auditOptions{}, []string{"a.go", "./..."}, "files and packages do not mix"},
		{auditOptions{}, []string{"no.go"}, "no.go: no such file"},
	} {
		tt.o.concurrency = 1
		_, err := auditWith(t, t.Context(), tt.o, tt.args...)
		if !isUsage(err) || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%+v %q: err = %v, want a usage error with %q", tt.o, tt.args, err, tt.want)
		}
	}
}
