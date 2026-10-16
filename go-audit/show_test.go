// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reportStore makes a store with reports of example.com/m, the module that
// testModule writes, and of example.com/b, and returns it.
func reportStore(t *testing.T) string {
	t.Helper()
	store := t.TempDir()
	t.Setenv("GO_STYLE_HOME", store)
	for _, r := range []struct{ mod, name string }{
		{"example.com/m", "20261016-100000-audit"},
		{"example.com/m", "20261016-110000-audit"},
		{"example.com/b", "20261016-120000-style-all"},
		{"example.com/b", "20261015-090000-audit"},
	} {
		at := stampTime(r.name)
		addReport(t, store, r.mod, r.name, v1(at, "audit", r.mod, "./...", 0, "nothing found"))
	}
	return store
}

func TestFindReport(t *testing.T) {
	store := reportStore(t)
	t.Chdir(testModule(t, nil))
	twin := filepath.Join(store, "reports", "example.com", "b", "20261015-090000-audit")
	tests := []struct {
		arg  string
		want string // the name of the report, or the start of the error
		err  bool
	}{
		{"", "20261016-110000-audit", false}, // the newest of the module in the current directory
		{"20261016-100000-audit", "20261016-100000-audit", false},
		{"20261016-11", "20261016-110000-audit", false},
		{"STYLE", "20261016-120000-style-all", false},
		{"b/20261015", "20261015-090000-audit", false},
		{twin + ".md", "20261015-090000-audit", false},
		{twin + ".json", "20261015-090000-audit", false},
		{"20261016", "3 reports have names starting with \"20261016\":\n\t20261016-120000-style-all  example.com/b", true},
		{"example.com/b", "2 reports match \"example.com/b\":", true},
		{"nothing", "no report in " + filepath.Join(store, "reports") + " matches \"nothing\"", true},
	}
	for _, tt := range tests {
		r, err := findReport(t.Context(), store, tt.arg)
		switch {
		case tt.err:
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("findReport(%q): err = %v, want %q…", tt.arg, err, tt.want)
			}
		case err != nil:
			t.Errorf("findReport(%q): %v", tt.arg, err)
		case r.name() != tt.want:
			t.Errorf("findReport(%q) = %s, want %s", tt.arg, r.name(), tt.want)
		}
	}
}

func TestFindReportOutsideModule(t *testing.T) {
	store := reportStore(t)
	t.Chdir(t.TempDir())
	r, err := findReport(t.Context(), store, "")
	if err != nil || r.name() != "20261016-120000-style-all" {
		t.Errorf("findReport outside a module = %v, %v; want the newest of all", r, err)
	}

	empty := t.TempDir()
	if _, err := findReport(t.Context(), empty, ""); err == nil || !strings.HasPrefix(err.Error(), "no reports in ") {
		t.Errorf("findReport in an empty store: err = %v", err)
	}
}

func TestFindReportModuleWithout(t *testing.T) {
	store := reportStore(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/none\n")
	t.Chdir(root)
	if _, err := findReport(t.Context(), store, ""); err == nil ||
		!strings.HasPrefix(err.Error(), "no reports of example.com/none in ") {
		t.Errorf("findReport in a module with no reports: err = %v", err)
	}
}

// showModule makes a module with files to link to, and a store with a
// report of it whose Markdown is md; it returns the module root, the store
// and the name of the report.
func showModule(t *testing.T, md string) (root, store, name string) {
	t.Helper()
	root = testModule(t, map[string]string{"a.go": "package m\n", "sub/b.go": "package sub\n"})
	store = t.TempDir()
	t.Setenv("GO_STYLE_HOME", store)
	name = "20261016-174935-audit"
	dir, err := moduleDir(store, "reports", "example.com/m")
	if err != nil {
		t.Fatal(err)
	}
	js := strings.Replace(v1(stampTime(name), "audit", "example.com/m", "./...", 1, "vet 1"),
		`"root":"/src/example.com/m"`, fmt.Sprintf("%q:%q", "root", root), 1)
	writeFile(t, filepath.Join(dir, name+".json"), js)
	writeFile(t, filepath.Join(dir, name+".md"), md)
	return root, store, name
}

// noBrowser makes openBrowser record what it was to open instead.
func noBrowser(t *testing.T) *[]string {
	t.Helper()
	var opened []string
	saved := openBrowser
	openBrowser = func(file string) error {
		opened = append(opened, file)
		return nil
	}
	t.Cleanup(func() { openBrowser = saved })
	return &opened
}

func TestShow(t *testing.T) {
	root, store, name := showModule(t, "# go-audit: example.com/m\n\n- `a.go:3:2` printf (printf)\n")
	opened := noBrowser(t)
	var out strings.Builder
	saved := stdout
	stdout = &out
	t.Cleanup(func() { stdout = saved })

	// By default the page goes beside the report, and into the browser.
	if err := runShow(t.Context(), "", name); err != nil {
		t.Fatal(err)
	}
	dir, _ := moduleDir(store, "reports", "example.com/m")
	page := filepath.Join(dir, name+".html")
	html, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	want := `<a href="` + fileURL(filepath.Join(root, "a.go")) + `"><code>a.go:3:2</code></a>`
	for _, s := range []string{"<title>example.com/m · " + name + "</title>", "<style>", want} {
		if !strings.Contains(string(html), s) {
			t.Errorf("the page has no %q:\n%s", s, html)
		}
	}
	if len(*opened) != 1 || (*opened)[0] != page {
		t.Errorf("show opened %q, want %s", *opened, page)
	}
	if !strings.Contains(out.String(), "wrote "+page) {
		t.Errorf("show printed %q", out.String())
	}
	// A list of the reports does not take the page for one.
	if reports, _ := loadReports(store); len(reports) != 1 {
		t.Errorf("%d reports after show, want 1", len(reports))
	}

	// -o writes where it says, a directory takes the default name, and the
	// browser stays closed.
	*opened = nil
	outDir := t.TempDir()
	file := filepath.Join(outDir, "page.html")
	for _, o := range []struct{ arg, file string }{
		{file, file},
		{outDir, filepath.Join(outDir, name+".html")},
	} {
		if err := runShow(t.Context(), o.arg, name); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(o.file); err != nil {
			t.Errorf("show -o %s: %v", o.arg, err)
		}
	}
	if len(*opened) != 0 {
		t.Errorf("show -o opened %q", *opened)
	}

	// The editor of the config makes the links.
	writeFile(t, filepath.Join(store, configName),
		"editor: \"vscode://file/{path}:{line}:{col}\"\nchecks:\n  - name: vet\n    cmd: [go, vet, \"{packages}\"]\n    format: vet-json\n")
	if err := runShow(t.Context(), file, name); err != nil {
		t.Fatal(err)
	}
	html, _ = os.ReadFile(file)
	if want := `href="vscode://file/` + escapePath(filepath.Join(root, "a.go")) + `:3:2"`; !strings.Contains(string(html), want) {
		t.Errorf("the page with an editor has no %q:\n%s", want, html)
	}
}

func TestShowUsage(t *testing.T) {
	cmd := showCommand()
	if err := cmd.run(t.Context(), []string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "one report") {
		t.Errorf("show a b: err = %v", err)
	}
}

// testLinker links a position to /src/<file>#L<line>.<col>.
func testLinker(file string, line, col int) string {
	return fmt.Sprintf("/src/%s#L%d.%d", file, line, col)
}

func TestRenderMarkdown(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"a.go", "sub/b.go", "go.mod"} {
		writeFile(t, filepath.Join(root, filepath.FromSlash(f)), "package x\n")
	}
	writeFile(t, filepath.Join(root, "sub", "dir", "c.go"), "")
	outside := filepath.Join(t.TempDir(), "secret.go")
	writeFile(t, outside, "package secret\n")
	if err := os.Symlink(outside, filepath.Join(root, "out.go")); err != nil {
		t.Log("no symbolic links:", err)
	}
	md := "# go-audit: example.com/m\n\n" +
		"| Check | Note |\n|---|---|\n| vet | a.go:3:1: oops |\n\n" +
		"## vet: 1 finding\n\n" +
		"- `a.go:12:5` a \\<script>alert(1)\\</script> or <b>bold</b> (printf)\n" +
		"- `sub/b.go:14-20` a range, `./sub/b.go` a file, `go.mod` another\n" +
		"- `missing.go:1`, `../a.go:1`, `out.go:1` and `sub/dir` lead nowhere\n" +
		"- in text: ./sub/b.go:7:2 and sub/b.go:8, not x/a.go:3, ../a.go:1 or ba.go:1\n" +
		"- [click](javascript:alert(1)), [web](https://go.dev/doc), [here](#vet-1-finding)\n\n" +
		"<div onclick=\"x()\">raw</div>\n\n" +
		"```go\n  fmt.Printf(\"%d\", `a.go:1`)\n```\n\n" +
		"--allow-parallel-runners \"quoted\" ...\n"
	html := string(renderMarkdown(md, root, testLinker))
	for _, want := range []string{
		"<h1>go-audit: example.com/m</h1>",
		"<table>", "<td>vet</td>",
		`<td><a href="/src/a.go#L3.1">a.go:3:1</a>: oops</td>`,
		`<a href="/src/a.go#L12.5"><code>a.go:12:5</code></a>`,
		"&lt;script&gt;alert(1)&lt;/script&gt;", "&lt;b&gt;bold&lt;/b&gt;",
		`<a href="/src/sub/b.go#L14.0"><code>sub/b.go:14-20</code></a>`,
		`<a href="/src/sub/b.go#L0.0"><code>./sub/b.go</code></a>`,
		`<a href="/src/go.mod#L0.0"><code>go.mod</code></a>`,
		"<code>missing.go:1</code>,", "<code>../a.go:1</code>", "<code>out.go:1</code>", "<code>sub/dir</code>",
		`<a href="/src/sub/b.go#L7.2">./sub/b.go:7:2</a> and <a href="/src/sub/b.go#L8.0">sub/b.go:8</a>`,
		"not x/a.go:3, ../a.go:1 or ba.go:1",
		"click,", `<a href="https://go.dev/doc">web</a>`, `<a href="#vet-1-finding">here</a>`,
		"&lt;div onclick=", "fmt.Printf(&quot;%d&quot;, `a.go:1`)",
		"--allow-parallel-runners &quot;quoted&quot; ...",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the HTML has no %q:\n%s", want, html)
		}
	}
	for _, bad := range []string{"<script", "<b>", "javascript:", "<div"} {
		if strings.Contains(html, bad) {
			t.Errorf("the HTML has %q:\n%s", bad, html)
		}
	}

	// Without a root there is nothing to link to.
	if html := string(renderMarkdown(md, "", testLinker)); strings.Contains(html, `href="/src/`) {
		t.Errorf("a report with no root has links to the code:\n%s", html)
	}
}

func TestRenderGoStyle(t *testing.T) {
	// A go-style report, its Markdown as the plugin writes it.
	root := t.TempDir()
	for _, f := range []string{"pkg/server/http/http.go", "app/providers/pg/initialize.go", "internal/auth/tests/helpers_test.go"} {
		writeFile(t, filepath.Join(root, filepath.FromSlash(f)), "package x\n")
	}
	md := "### Ошибки поведения\n\n" +
		"1. `pkg/server/http/http.go:55` — проверка `port >= 1 || port <= 65535` истинна. — [Google Guide › Clarity](https://google.github.io/styleguide/go/guide#clarity)\n" +
		"2. `internal/auth/tests/helpers_test.go:84` и `verifier_test.go:20` — `identity` хранит `t`.\n\n" +
		"- `app/providers/pg/initialize.go:14-20` — сообщения gocritic `captLocal: 'DBHost' should not be capitalized`.\n"
	html := string(renderMarkdown(md, root, testLinker))
	for _, want := range []string{
		`<a href="/src/pkg/server/http/http.go#L55.0"><code>pkg/server/http/http.go:55</code></a>`,
		`<a href="/src/internal/auth/tests/helpers_test.go#L84.0">`,
		"<code>verifier_test.go:20</code>", // relative to a directory the report names
		`<a href="/src/app/providers/pg/initialize.go#L14.0">`,
		"<code>port &gt;= 1 || port &lt;= 65535</code>",
		`<a href="https://google.github.io/styleguide/go/guide#clarity">`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the HTML of a go-style report has no %q:\n%s", want, html)
		}
	}
}

func TestEditorLinker(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "src", "my mod")
	tests := []struct {
		editor    string
		line, col int
		want      string
	}{
		{"", 3, 2, "file:///src/my%20mod/a%23b.go"},
		{"vscode://file/{path}:{line}:{col}", 3, 2, "vscode://file//src/my%20mod/a%23b.go:3:2"},
		{"idea://open?file={path}&line={line}", 0, 0, "idea://open?file=/src/my%20mod/a%23b.go&line=1"},
	}
	for _, tt := range tests {
		if got := editorLinker(tt.editor, root)("a#b.go", tt.line, tt.col); got != tt.want {
			t.Errorf("editor %q: link %q, want %q", tt.editor, got, tt.want)
		}
	}
}

func TestPage(t *testing.T) {
	page, err := renderPage("<x> & y", "<p>body</p>")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<!DOCTYPE html>", "<title>&lt;x&gt; &amp; y</title>", "<p>body</p>", "prefers-color-scheme: dark"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("the page has no %q:\n%s", want, page)
		}
	}
}
