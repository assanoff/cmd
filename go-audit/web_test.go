// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	auditName = "20261016-174935-audit"
	styleName = "20261016-180000-style-all"
)

// webStore makes a module and a store with an audit report and a go-style
// report of it, each with findings in its JSON, and returns them.
func webStore(t *testing.T) (root, store string) {
	t.Helper()
	root = testModule(t, map[string]string{
		"a.go":     "package m\n\nfunc F() {\n\treturn\n}\n",
		"sub/b.go": "package sub\n",
	})
	store = t.TempDir()
	dir, err := moduleDir(store, "reports", "example.com/m")
	if err != nil {
		t.Fatal(err)
	}
	twin := func(name, kind string, findings []map[string]any) {
		t.Helper()
		data, err := json.Marshal(map[string]any{
			"format": 1, "kind": kind, "module": "example.com/m", "root": root,
			"time": stampTime(name), "label": "./...", "total": len(findings),
			"summary": kind + " summary", "findings": findings,
		})
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, name+".json"), string(data))
	}
	twin(auditName, "audit", []map[string]any{
		{"check": "vet", "file": "a.go", "line": 4, "col": 2, "rule": "unreachable", "message": "redundant return"},
		{"check": "gofmt", "file": "a.go", "line": 0, "message": ""},
		{"check": "vet", "file": "a.go", "line": 99, "rule": "x", "message": "past the end now"},
		{"check": "vet", "file": "sub/b.go", "line": 1, "rule": "y", "message": "in another file"},
	})
	writeFile(t, filepath.Join(dir, auditName+".md"),
		"# go-audit: example.com/m\n\n- `a.go:4:2` redundant return (unreachable)\n- see <b>sub/b.go:1</b>\n")
	twin(styleName, "style", []map[string]any{
		{
			"tool": "godot", "severity": "should", "file": "a.go", "line": 3, "rule": "comment-sentences",
			"message": "Comment should end in a period", "verified": false, "also_lines": []int{5},
		},
	})
	writeFile(t, filepath.Join(dir, styleName+".md"), "# Go style review\n\n1. `a.go:3` — a comment\n")
	// The files are as old as the reports.
	for _, f := range []string{"a.go", "sub/b.go"} {
		at := stampTime(auditName)
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(f)), at, at); err != nil {
			t.Fatal(err)
		}
	}
	return root, store
}

// get asks the handler for target with the host, and returns the status
// and the body.
func get(t *testing.T, h http.Handler, method, host, target string) (int, string, http.Header) {
	t.Helper()
	req := httptest.NewRequest(method, "http://localhost"+target, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header()
}

func TestWebPages(t *testing.T) {
	_, store := webStore(t)
	h := newWeb(store)
	tests := []struct {
		target string
		code   int
		want   []string
	}{
		{"/", 200, []string{
			"<title>go-audit reports</title>",
			`<a href="/example.com/m/` + styleName + `">`, `<a href="/example.com/m/` + auditName + `">`,
			`<a href="/example.com/m/">example.com/m</a>`, "audit summary", "style ./...",
		}},
		{"/example.com/m/", 200, []string{"<h1>example.com/m</h1>", "2 reports"}},
		{"/example.com/m/" + auditName, 200, []string{
			`<a href="/example.com/m/">example.com/m</a> › ` + auditName,
			`<a href="/src/example.com/m/a.go?report=` + auditName + `#L4"><code>a.go:4:2</code></a>`,
			`<a href="/src/example.com/m/sub/b.go?report=` + auditName + `#L1">sub/b.go:1</a>`,
			"&lt;b&gt;",
		}},
		{"/nope/", 404, nil},
		{"/example.com/m/20990101-000000-audit", 404, nil},
	}
	for _, tt := range tests {
		code, body, _ := get(t, h, "GET", "localhost:8781", tt.target)
		if code != tt.code {
			t.Errorf("GET %s: %d, want %d\n%s", tt.target, code, tt.code, body)
			continue
		}
		for _, want := range tt.want {
			if !strings.Contains(body, want) {
				t.Errorf("GET %s: no %q in\n%s", tt.target, want, body)
			}
		}
	}

	// A module without its slash goes to the page of the module.
	if code, _, hdr := get(t, h, "GET", "localhost", "/example.com/m"); code != 301 || hdr.Get("Location") != "/example.com/m/" {
		t.Errorf("GET /example.com/m: %d to %q, want 301 to /example.com/m/", code, hdr.Get("Location"))
	}
}

func TestWebSource(t *testing.T) {
	root, store := webStore(t)
	h := newWeb(store)
	code, body, _ := get(t, h, "GET", "localhost", "/src/example.com/m/a.go?report="+auditName)
	if code != 200 {
		t.Fatalf("the page of a.go: %d\n%s", code, body)
	}
	// Each finding is under its line, the one past the end and the one
	// about the whole file above the code.
	order := []string{
		"3 findings of the report in this file",
		`<span class="who">gofmt</span>`,
		`<span class="who">vet · x</span> past the end now`,
		`<tr id="L4">`, "\treturn",
		`<span class="who">vet · unreachable</span> redundant return`,
		`<tr id="L5">`,
	}
	last := -1
	for _, s := range order {
		i := strings.Index(body, s)
		if i < 0 || i < last {
			t.Fatalf("the page of a.go has no %q after the one before it:\n%s", s, body)
		}
		last = i
	}
	if strings.Contains(body, "in another file") || strings.Contains(body, "changed after the report") ||
		strings.Contains(body, "open in the editor") {
		t.Errorf("the page of a.go has what it should not:\n%s", body)
	}

	// The go-style report: the severity and the tool, and the finding under
	// each of its lines. With no report named, the newest of the module.
	for _, target := range []string{"/src/example.com/m/a.go?report=" + styleName, "/src/example.com/m/a.go"} {
		_, body, _ = get(t, h, "GET", "localhost", target)
		who := `<span class="who">should · godot · comment-sentences · not verified</span>`
		if strings.Count(body, who) != 2 ||
			strings.Index(body, `<tr id="L3">`) > strings.Index(body, who) ||
			strings.LastIndex(body, who) < strings.Index(body, `<tr id="L5">`) {
			t.Errorf("GET %s: the finding is not under lines 3 and 5:\n%s", target, body)
		}
	}

	// A file changed after the report; an editor in the config.
	at := stampTime(auditName).Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, "a.go"), at, at); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(store, configName),
		"editor: \"zed://file/{path}:{line}\"\nchecks:\n  - name: vet\n    cmd: [go, vet, \"{packages}\"]\n    format: vet-json\n")
	_, body, _ = get(t, h, "GET", "localhost", "/src/example.com/m/a.go?report="+auditName)
	for _, want := range []string{
		"The file changed after the report, at " + at.Local().Format("2006-01-02 15:04:05"),
		`<a href="zed://file/` + escapePath(filepath.Join(root, "a.go")) + `:1">open in the editor</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page of a changed a.go has no %q:\n%s", want, body)
		}
	}
}

func TestWebRefuses(t *testing.T) {
	root, store := webStore(t)
	outside := filepath.Join(t.TempDir(), "secret.go")
	writeFile(t, outside, "package secret\n")
	links := os.Symlink(outside, filepath.Join(root, "out.go")) == nil
	h := newWeb(store)
	for _, target := range []string{
		"/src/example.com/m/missing.go",
		"/src/example.com/m/sub",                               // a directory
		"/src/example.com/m/a.go?report=20990101-000000-audit", // a report there is not
		"/src/example.com/nope/a.go",                           // a module of no report
		"/src/example.com/m/../../etc/passwd",
		"/src/example.com/m/..%2F..%2F..%2Fetc%2Fpasswd",
		"/src/example.com/m/" + filepath.ToSlash(outside),
		"/../../etc/passwd",
	} {
		if code, body, _ := get(t, h, "GET", "localhost", target); code == 200 {
			t.Errorf("GET %s: 200, want a refusal\n%s", target, body)
		}
	}
	if code, _, _ := get(t, h, "GET", "localhost", "/src/example.com/m/out.go"); links && code != 404 {
		t.Errorf("GET of a link out of the module: %d, want 404", code)
	}

	if code, _, _ := get(t, h, "POST", "localhost", "/"); code != 405 {
		t.Errorf("POST /: %d, want 405", code)
	}
	for _, host := range []string{"evil.example", "evil.example:8781", "192.168.1.5:8781", "localhost.evil.example"} {
		if code, _, _ := get(t, h, "GET", host, "/"); code != 403 {
			t.Errorf("GET / of Host %s: %d, want 403", host, code)
		}
	}
	for _, host := range []string{"localhost", "127.0.0.1:8781", "[::1]:8781"} {
		if code, _, _ := get(t, h, "GET", host, "/"); code != 200 {
			t.Errorf("GET / of Host %s: %d, want 200", host, code)
		}
	}
}

func TestBrowseAddr(t *testing.T) {
	got := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 54321}
	for _, tt := range []struct{ addr, want string }{
		{"localhost:8781", "localhost:8781"},
		{":8781", "localhost:8781"},
		{"0.0.0.0:0", "localhost:54321"},
		{"127.0.0.1:9", "127.0.0.1:9"},
	} {
		if s := browseAddr(tt.addr, got); s != tt.want {
			t.Errorf("browseAddr(%q) = %q, want %q", tt.addr, s, tt.want)
		}
	}
}

func TestRunWeb(t *testing.T) {
	_, store := webStore(t)
	t.Setenv("GO_STYLE_HOME", store)
	opened := make(chan string, 1)
	saved := openBrowser
	openBrowser = func(u string) error {
		opened <- u
		return nil
	}
	t.Cleanup(func() { openBrowser = saved })
	var out strings.Builder
	savedOut := stdout
	stdout = &out
	t.Cleanup(func() { stdout = savedOut })

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runWeb(ctx, "localhost:0", true) }()
	var home string
	select {
	case home = <-opened:
	case err := <-done:
		t.Fatalf("runWeb: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("runWeb opened no browser")
	}
	resp, err := http.Get(home)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), auditName) {
		t.Errorf("GET %s: %d\n%s", home, resp.StatusCode, body)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("runWeb after the interrupt: %v", err)
	}
	if !strings.HasPrefix(home, "http://localhost:") || !strings.Contains(out.String(), "serving "+store+" on "+home) {
		t.Errorf("runWeb opened %q and printed %q", home, out.String())
	}

	if err := webCommand().run(t.Context(), []string{"x"}); err == nil {
		t.Error("web with an argument: no error")
	}
}
