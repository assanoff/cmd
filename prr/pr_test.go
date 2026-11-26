// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRef(t *testing.T) {
	tests := []struct {
		in               string
		kind, host, repo string
		n                int
	}{
		{"https://git.chocodev.kz/ryadom/workspace/-/merge_requests/77", gitlab, "git.chocodev.kz", "ryadom/workspace", 77},
		{"https://gitlab.com/a/b/c/-/merge_requests/5/diffs?view=x", gitlab, "gitlab.com", "a/b/c", 5},
		{"https://github.com/rsc/cmd/pull/4", github, "github.com", "rsc/cmd", 4},
		{"https://github.example.com/o/r/pull/12/files", github, "github.example.com", "o/r", 12},
	}
	for _, tt := range tests {
		p, err := parseRef(context.Background(), tt.in)
		if err != nil {
			t.Errorf("parseRef(%q): %v", tt.in, err)
			continue
		}
		if p.Kind != tt.kind || p.Host != tt.host || p.Repo != tt.repo || p.N != tt.n {
			t.Errorf("parseRef(%q) = %s %s %s %d", tt.in, p.Kind, p.Host, p.Repo, p.N)
		}
	}
	for _, bad := range []string{"https://github.com/rsc/cmd", "nonsense"} {
		if _, err := parseRef(context.Background(), bad); err == nil {
			t.Errorf("parseRef(%q) did not fail", bad)
		}
	}
}

func TestParseRemote(t *testing.T) {
	for in, want := range map[string]string{
		"git@git.chocodev.kz:ryadom/workspace.git":  "git.chocodev.kz ryadom/workspace",
		"ssh://git@gitlab.com:2222/a/b/c.git":       "gitlab.com a/b/c",
		"https://github.com/rsc/cmd.git":            "github.com rsc/cmd",
		"https://github.com/rsc/cmd":                "github.com rsc/cmd",
		"git@github.com:assanoff/cmd":               "github.com assanoff/cmd",
		"https://user@git.example.com/g/p.git":      "git.example.com g/p",
		"ssh://git@git.chocodev.kz/ryadom/tip.git":  "git.chocodev.kz ryadom/tip",
		"git@git.chocodev.kz:ryadom/ryadom-web.git": "git.chocodev.kz ryadom/ryadom-web",
	} {
		host, repo, err := parseRemote(in)
		if err != nil || host+" "+repo != want {
			t.Errorf("parseRemote(%q) = %q %q, %v; want %q", in, host, repo, err, want)
		}
	}
}

func me(body string) comment {
	return comment{Author: "me", Body: body}
}

func agent(body string) comment {
	return comment{Author: "claude", FromAgent: true, Body: body}
}

func TestThreads(t *testing.T) {
	tests := []struct {
		name     string
		comments []comment
		waiting  bool
		note     string
	}{
		{"remark", []comment{me("rename it")}, false, "rename it"},
		{"remark twice", []comment{me("rename it"), me("and the test")}, false, "rename it\n\nand the test"},
		{"question", []comment{me("@c why two queues?")}, true, ""},
		{"answered", []comment{me("@c why?"), agent("because")}, false, ""},
		{"follow-up", []comment{me("@claude why?"), agent("because"), me("and?")}, true, ""},
		{"agent finding", []comment{agent("this drops an error")}, false, ""},
		{"reply to finding", []comment{agent("drops an error"), me("where?")}, true, ""},
		{"mail address", []comment{me("write to a@company.kz")}, false, "write to a@company.kz"},
		{"remark answered by prr", []comment{me("typo"), {Author: "prr", FromAgent: true}}, false, "typo"},
		{"asking about a remark", []comment{me("typo here"), me("@c is it?")}, true, "typo here"},
		{
			"remark after asking",
			[]comment{me("typo"), me("@c is it?"), agent("yes"), me("and in the title")},
			false, "typo\n\nand in the title",
		},
	}
	for _, tt := range tests {
		th := &thread{Comments: tt.comments}
		if got := th.waiting(); got != tt.waiting {
			t.Errorf("%s: waiting() = %v", tt.name, got)
		}
		if got := th.note(); got != tt.note {
			t.Errorf("%s: note() = %q, want %q", tt.name, got, tt.note)
		}
	}
}

func TestPosition(t *testing.T) {
	tests := []struct {
		th    thread
		where string
	}{
		{thread{File: "a.md", Side: "new", Line: 3, CurrentLine: 5}, "a.md:5"},
		{thread{File: "a.md", Side: "old", Line: 3, CurrentLine: 5}, "a.md:3"},
		{thread{File: "a.md", Side: "new", Line: 3, Stale: true}, "a.md"},
		{thread{File: "a.md", Side: "new"}, "a.md"},
		{thread{File: "/COMMIT_MSG", Side: "new", Line: 7, CurrentLine: 7}, "description"},
	}
	for _, tt := range tests {
		if got := tt.th.where(); got != tt.where {
			t.Errorf("%+v: where() = %q, want %q", tt.th, got, tt.where)
		}
	}
}

func TestCommonDir(t *testing.T) {
	tests := []struct {
		paths []string
		want  string
	}{
		{nil, ""},
		{[]string{"a/b/c.md"}, "a/b/"},
		{[]string{"a/b/c.md", "a/b/d/e.md"}, "a/b/"},
		{[]string{"a/b/c.md", "a/x.md"}, "a/"},
		{[]string{"a/b.md", "c.md"}, ""},
		{[]string{"ab/c.md", "a/c.md"}, ""},
	}
	for _, tt := range tests {
		if got := commonDir(tt.paths); got != tt.want {
			t.Errorf("commonDir(%q) = %q, want %q", tt.paths, got, tt.want)
		}
	}
}

// TestReview runs the git side against a local remote laid out as GitLab
// lays one out: a target branch and the head of a merge request under
// refs/merge-requests.
func TestReview(t *testing.T) {
	ctx := context.Background()
	t.Setenv("PRRHOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")

	remote := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		out, err := git(ctx, dir, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	write := func(name, text string) {
		t.Helper()
		name = filepath.Join(remote, name)
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(remote, "init", "-q", "-b", "master")
	write("README.md", "# repo\n")
	write("docs/old.md", "old\n")
	run(remote, "add", ".")
	run(remote, "commit", "-q", "-m", "base")
	run(remote, "checkout", "-q", "-b", "feature")
	write("docs/new.md", "new\n")
	run(remote, "rm", "-q", "docs/old.md")
	run(remote, "add", ".")
	run(remote, "commit", "-q", "-m", "one")
	write("docs/new.md", "newer\n")
	run(remote, "commit", "-q", "-am", "two")
	head := run(remote, "rev-parse", "HEAD")
	run(remote, "update-ref", "refs/merge-requests/1/head", head)
	run(remote, "checkout", "-q", "master")
	write("README.md", "# repo, moved on\n")
	run(remote, "commit", "-q", "-am", "master moves")

	p := &PR{
		Kind: gitlab, Host: "example.com", Repo: "g/repo", N: 1,
		URL: "https://example.com/g/repo/-/merge_requests/1", Title: "Add new",
		Body: "Why.", Author: "A", Login: "a", Base: "master", Head: head,
	}
	if err := os.MkdirAll(p.gitDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	run(p.gitDir(), "init", "-q", "--bare")
	run(p.gitDir(), "remote", "add", "origin", remote)
	if err := p.fetch(ctx); err != nil {
		t.Fatal(err)
	}

	c1, err := p.reviewCommit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := p.reviewCommit(ctx)
	if err != nil || c1 != c2 {
		t.Fatalf("reviewCommit not repeatable: %s, %s, %v", c1, c2, err)
	}
	if err := p.checkout(ctx, c1); err != nil {
		t.Fatal(err)
	}
	wt := p.worktree()
	// What review takes for pending: one change, whatever the commits.
	pending := run(wt, "log", "--format=%s", "HEAD", "--not", "--remotes=origin")
	if pending != "Add new" {
		t.Errorf("pending = %q, want the one review commit", pending)
	}
	if got := trailer(ctx, wt, "PR-Head"); got != head {
		t.Errorf("PR-Head = %q, want %q", got, head)
	}
	if got := trailer(ctx, wt, "Change-Id"); got != p.changeID() {
		t.Errorf("Change-Id = %q, want %q", got, p.changeID())
	}

	b, err := build(ctx, []*PR{p})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"repo/README.md":   "# repo, moved on\n", // the target branch as it is now
		"repo/docs/new.md": "newer\n",            // with the files of the PR on top
		"repo/docs/old.md": "",                   // and without those it deletes
	} {
		data, err := os.ReadFile(filepath.Join(b.dir, name))
		if string(data) != want || (want == "") != os.IsNotExist(err) {
			t.Errorf("%s = %q, %v; want %q", name, data, err, want)
		}
	}
	var got []string
	for _, c := range b.prs[0].Files {
		got = append(got, c.Status+" "+c.Path)
	}
	if strings.Join(got, ", ") != "A docs/new.md, D docs/old.md" {
		t.Errorf("files = %q", got)
	}
}

func TestNearest(t *testing.T) {
	lines := []string{"a", "b", "", "a", "c"}
	tests := []struct {
		text string
		n    int
		want int
	}{
		{"a", 1, 1},
		{"a", 5, 4},
		{"c", 1, 5},
		{"gone", 2, 0},
		{"", 3, 0}, // a blank line could be anywhere
	}
	for _, tt := range tests {
		if got := nearest(lines, tt.text, tt.n); got != tt.want {
			t.Errorf("nearest(%q, %d) = %d, want %d", tt.text, tt.n, got, tt.want)
		}
	}
}

func TestImported(t *testing.T) {
	link := "https://git.example.com/g/p/-/merge_requests/1#note_42"
	host := comment{Author: "bob", FromAgent: true, Body: note{"bob", "why?", link}.text(gitlab)}
	tests := []struct {
		name     string
		comments []comment
		imported string
		waiting  bool
		note     string
	}{
		{"untouched", []comment{host}, link, false, ""},
		{"my reply goes back to the host", []comment{host, me("because")}, link, false, "because"},
		{"asking the agent about it", []comment{host, me("@c is bob right?")}, link, true, ""},
		{
			"replying to bob after asking",
			[]comment{host, me("@c is bob right?"), agent("partly"), me("agreed, bob")},
			link, false, "agreed, bob",
		},
		{"a link the reviewer wrote", []comment{me("see\n[GitLab](" + link + ")")}, "", false, "see\n[GitLab](" + link + ")"},
		{"the agent's own finding", []comment{agent("drops an error")}, "", false, ""},
	}
	for _, tt := range tests {
		th := &thread{Comments: tt.comments}
		if got := th.imported(); got != tt.imported {
			t.Errorf("%s: imported() = %q, want %q", tt.name, got, tt.imported)
		}
		if got := th.waiting(); got != tt.waiting {
			t.Errorf("%s: waiting() = %v", tt.name, got)
		}
		if got := th.note(); got != tt.note {
			t.Errorf("%s: note() = %q, want %q", tt.name, got, tt.note)
		}
	}
}

// TestThemeScript checks that the page carries the script exactly as the
// policy hashes it, as a browser would refuse it otherwise.
func TestThemeScript(t *testing.T) {
	b := &bundle{dir: t.TempDir()}
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "<script>"+themeJS+"</script>") {
		t.Errorf("the page does not carry themeJS as it is")
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "'sha256-"+hash(themeJS)+"'") {
		t.Errorf("the policy does not allow themeJS")
	}
}
