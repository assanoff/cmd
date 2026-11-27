// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// at returns a finding of check and rule at line of a.go.
func at(check, rule string, line int, msg string) finding {
	return finding{Check: check, Rule: rule, File: "a.go", Line: line, Message: msg}
}

func TestAuditMarkdownGroups(t *testing.T) {
	rep := &auditReport{
		report: report{Module: "example.com/m"},
		Checks: []checkReport{
			{Name: "style", Status: statusFound, Count: 5},
			{Name: "vuln", Status: statusFound, Count: 2},
			{Name: "nil", Status: statusFound, Count: 2},
			{Name: "vet", Status: statusOK},
		},
		Findings: []finding{
			at("style", "errorstrings", 1, `[s.errorstrings] Error strings are lower case (ref: x): "Foo"`),
			at("style", "getters", 2, "no Get"),
			at("style", "errorstrings", 3, `[s.errorstrings] Error strings are lower case (ref: x): "Bar"`),
			at("style", "", 4, "a finding of no rule"),
			at("style", "getters", 5, "no  Get\n"),
			at("vuln", "GO-1", 6, "one"),
			at("vuln", "GO-2", 7, "two"),
			at("nil", "nilaway", 8, "Potential nil panic detected. Observed nil flow: a -> b"),
			at("nil", "nilaway", 9, "Potential nil panic detected. Observed nil flow: c -> d"),
		},
	}
	md := auditMarkdown(rep)
	for _, want := range []string{
		"| [style](#style-5-findings) | found | 5 |",
		"| [vuln](#vuln-2-findings) | found | 2 |",
		"| vet | ok | 0 |",
		// A table of the rules, those with the most first, no rule last.
		"## style: 5 findings\n\n| Rule | Findings |\n|---|---:|\n" +
			"| [errorstrings](#errorstrings-2-findings) | 2 |\n" +
			"| [getters](#getters-2-findings) | 2 |\n" +
			"| [other](#other-1-finding) | 1 |\n",
		// The start the messages share goes above them once, the rule not at all.
		"### errorstrings: 2 findings\n\n[s.errorstrings] Error strings are lower case (ref: x):\n\n" +
			"- `a.go:1` \"Foo\"\n- `a.go:3` \"Bar\"\n",
		"### getters: 2 findings\n\nno Get\n\n- `a.go:2`\n- `a.go:5`\n",
		"### other: 1 finding\n\n- `a.go:4` a finding of no rule\n",
		// A rule each: one list, with the rules.
		"## vuln: 2 findings\n\n- `a.go:6` one (GO-1)\n- `a.go:7` two (GO-2)\n",
		// One rule: one list, the shared start above it.
		"## nil: 2 findings\n\nPotential nil panic detected. Observed nil flow:\n\n" +
			"- `a.go:8` a -> b (nilaway)\n- `a.go:9` c -> d (nilaway)\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("the report has no\n%s\nin\n%s", want, md)
		}
	}
}

// TestAuditMarkdownLinks checks that every link within the page of a report
// leads to a heading there, when headings share their text as well.
func TestAuditMarkdownLinks(t *testing.T) {
	rep := &auditReport{
		report: report{Module: "example.com/m"},
		Checks: []checkReport{
			{Name: "a", Status: statusFound, Count: 3},
			{Name: "x", Status: statusFound, Count: 2},
			{Name: "under_score*", Status: statusFound, Count: 1},
		},
		Findings: []finding{
			at("a", "x", 1, "one"), at("a", "x", 2, "two"), at("a", "y", 3, "three"),
			at("x", "r", 4, "four"), at("x", "r", 5, "five"),
			at("under_score*", "", 6, "six"),
		},
	}
	md := auditMarkdown(rep)
	// The group x of a and the check x have the same heading.
	if !strings.Contains(md, "| [x](#x-2-findings-1) | found | 2 |") {
		t.Errorf("the check x does not link to the second heading x:\n%s", md)
	}
	html := string(renderMarkdown(md, "", nil))
	links := regexp.MustCompile(`href="#([^"]+)"`).FindAllStringSubmatch(html, -1)
	if len(links) < 6 {
		t.Fatalf("%d links within the page, want 6 and more:\n%s", len(links), html)
	}
	for _, l := range links {
		if !strings.Contains(html, `id="`+l[1]+`"`) {
			t.Errorf("a link to #%s, which is not on the page:\n%s", l[1], html)
		}
	}
}

func TestCommonStart(t *testing.T) {
	for _, tt := range []struct {
		msgs []string
		want string
	}{
		{[]string{"only one message of its own"}, ""},
		{[]string{"same", "same"}, "same"},
		{[]string{"a long start the two share: x", "a long start the two share: y"}, "a long start the two share:"},
		{[]string{"a long start the two share", "a long start the two share and more"}, "a long start the two share"},
		{[]string{"a long start the two sha-x", "a long start the two sha-y"}, "a long start the two"},
		{[]string{"short: x", "short: y"}, ""},
		{[]string{"nothing alike", "else"}, ""},
		{[]string{"ключ один и тот же: а", "ключ один и тот же: б"}, "ключ один и тот же:"},
	} {
		if got := commonStart(tt.msgs); got != tt.want {
			t.Errorf("commonStart(%q) = %q, want %q", tt.msgs, got, tt.want)
		}
	}
}

func TestReportMarkdown(t *testing.T) {
	audit, err := json.Marshal(&auditReport{
		report:   report{Kind: "audit", Module: "example.com/m"},
		Checks:   []checkReport{{Name: "vet", Status: statusFound, Count: 1}},
		Findings: []finding{at("vet", "printf", 1, "oops")},
	})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"new.md": "# as written\n", "new.json": string(audit),
		"stub.md": "# a stub\n", "stub.json": `{"kind":"audit","findings":[]}`,
		"none.md":  "# no twin\n",
		"style.md": "# go-style\n", "style.json": string(audit),
	}
	read := func(name string) ([]byte, error) {
		if s, ok := files[name]; ok {
			return []byte(s), nil
		}
		return nil, fs.ErrNotExist
	}
	for _, tt := range []struct {
		path, kind string
		want       string
	}{
		{"new.md", "audit", "| [vet](#vet-1-finding) | found | 1 |"}, // made afresh
		{"stub.md", "audit", "# a stub\n"},
		{"none.md", "audit", "# no twin\n"},
		{"style.md", "style", "# go-style\n"},
	} {
		md, err := reportMarkdown(&report{Kind: tt.kind, path: tt.path}, read)
		if err != nil || !strings.Contains(string(md), tt.want) {
			t.Errorf("%s: %v\n%s\nwant it to have %q", tt.path, err, md, tt.want)
		}
	}
	if _, err := reportMarkdown(&report{Kind: "audit", path: "gone.md"}, read); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a report gone: err = %v, want %v", err, fs.ErrNotExist)
	}
}
