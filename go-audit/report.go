// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// An auditReport is the JSON twin of a go-audit report: the fields every
// report has, then those of an audit.
type auditReport struct {
	report

	Packages    []string      `json:"packages"`
	Go          string        `json:"go"`
	Config      string        `json:"config"`
	Concurrency int           `json:"concurrency"`
	Wall        float64       `json:"wall"`
	Logs        string        `json:"logs"`
	Checks      []checkReport `json:"checks"`
	Findings    []finding     `json:"findings"`
}

// A checkReport is how one check went.
type checkReport struct {
	Name    string  `json:"name"`
	Status  string  `json:"status"`
	Count   int     `json:"count"`
	Seconds float64 `json:"seconds"`
	Note    string  `json:"note,omitempty"`
	Out     string  `json:"out,omitempty"` // its stdout, in the logs
	Log     string  `json:"log,omitempty"` // its stderr, in the logs
}

// writeReport writes the report of the audit into the store, its .json twin
// first, so that a list never finds the one without the other, and returns
// the path of the .md.
func (a *audit) writeReport(results []*result, wall time.Duration) (string, error) {
	dir, err := moduleDir(a.store, "reports", a.mod.path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	addSource(a.mod.root, results)
	rep := a.report(results, wall)
	data, err := json.MarshalIndent(rep, "", "\t")
	if err != nil {
		return "", err
	}
	md := filepath.Join(dir, a.name+".md")
	if err := os.WriteFile(strings.TrimSuffix(md, ".md")+".json", data, 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(md, []byte(markdown(rep)), 0o644); err != nil {
		return "", err
	}
	return md, nil
}

// report gathers what the report says.
func (a *audit) report(results []*result, wall time.Duration) *auditReport {
	goVersion, _ := a.goEnv("GOVERSION")
	rep := &auditReport{
		report: report{
			Format: reportFormat,
			Kind:   "audit",
			Module: a.mod.path,
			Root:   a.mod.root,
			Time:   a.time,
			Label:  strings.Join(a.packages, " "),
		},
		Packages:    a.packages,
		Go:          goVersion,
		Config:      a.cfg.source(),
		Concurrency: a.concurrency,
		Wall:        wall.Round(time.Millisecond).Seconds(),
		Logs:        a.logs,
		Findings:    []finding{},
	}
	for _, r := range results {
		c := checkReport{
			Name:    r.check.Name,
			Status:  r.status,
			Count:   len(r.findings),
			Seconds: r.took.Round(time.Millisecond).Seconds(),
			Note:    r.note,
		}
		if r.ran {
			c.Out, c.Log = r.check.Name+".out", r.check.Name+".log"
		}
		rep.Checks = append(rep.Checks, c)
		rep.Findings = append(rep.Findings, r.findings...)
	}
	rep.Total = len(rep.Findings)
	rep.Summary = summary(rep.Checks)
	return rep
}

// summary is the line of the report in go-audit reports: the checks that
// found something or failed, then how many were skipped.
func summary(checks []checkReport) string {
	var parts []string
	skipped := 0
	for _, c := range checks {
		switch c.Status {
		case statusFound:
			parts = append(parts, fmt.Sprintf("%s %d", c.Name, c.Count))
		case statusError:
			parts = append(parts, c.Name+" error")
		case statusSkipped:
			skipped++
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "nothing found")
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", skipped))
	}
	return strings.Join(parts, " · ")
}

// addSource puts into each finding the line of code it is about, as the
// file is now, at the end of the run, unless its check gave the line.
func addSource(root string, results []*result) {
	files := map[string][]string{}
	for _, r := range results {
		for i := range r.findings {
			f := &r.findings[i]
			if f.Source != "" || f.File == "" || f.Line < 1 || filepath.IsAbs(f.File) {
				continue
			}
			lines, ok := files[f.File]
			if !ok {
				lines = readLines(filepath.Join(root, filepath.FromSlash(f.File)))
				files[f.File] = lines
			}
			if f.Line <= len(lines) {
				f.Source = lines[f.Line-1]
			}
		}
	}
}

// readLines returns the lines of a file, none if it cannot be read.
func readLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var lines []string
	s := bufio.NewScanner(f)
	s.Buffer(nil, 1<<20)
	for s.Scan() {
		lines = append(lines, s.Text())
	}
	return lines // a line too long ends them early: the rest have no source
}

// markdown renders the report for people: a header, a table of the checks,
// and a section with the findings of each check that has some, each with
// its line of code.
func markdown(rep *auditReport) string {
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	p("# go-audit: %s\n\n", rep.Module)
	p("- **Packages:** `%s` in `%s`\n", rep.Label, rep.Root)
	p("- **Time:** %s · **Go:** %s · **Wall:** %s with -c %d\n",
		rep.Time.Format("2006-01-02 15:04:05 -07:00"), orDash(rep.Go),
		duration(time.Duration(rep.Wall*float64(time.Second))), rep.Concurrency)
	p("- **Config:** %s · **Logs:** `%s`\n\n", rep.Config, rep.Logs)

	p("| Check | Status | Findings | Time | Note |\n|---|---|---:|---:|---|\n")
	for _, c := range rep.Checks {
		count, took := "–", "–"
		if c.Status == statusOK || c.Count > 0 {
			count = fmt.Sprint(c.Count)
		}
		if c.Out != "" {
			took = duration(time.Duration(c.Seconds * float64(time.Second)))
		}
		p("| %s | %s | %s | %s | %s |\n", c.Name, c.Status, count, took, cell(c.Note))
	}

	for _, c := range rep.Checks {
		if c.Count == 0 {
			continue
		}
		p("\n## %s: %d %s\n\n", c.Name, c.Count, plural(c.Count, "finding"))
		if c.Note != "" {
			p("%s\n\n", inline(c.Note))
		}
		for _, f := range rep.Findings {
			if f.Check == c.Name {
				writeFinding(&b, f)
			}
		}
	}
	return blankLines.ReplaceAllString(b.String(), "\n\n")
}

// blankLines matches more than one blank line.
var blankLines = regexp.MustCompile(`\n{3,}`)

// writeFinding writes one finding as a list item: where, what and the rule,
// with the line of code under it.
func writeFinding(b *strings.Builder, f finding) {
	var item []string
	if pos := position(f); pos != "" {
		item = append(item, "`"+pos+"`")
	}
	// A message of several lines, as nilaway prints its nil flows, goes
	// into one; the .json keeps it as it was.
	if msg := strings.Join(strings.Fields(f.Message), " "); msg != "" {
		item = append(item, inline(msg))
	}
	if f.Rule != "" {
		item = append(item, "("+inline(f.Rule)+")")
	}
	fmt.Fprintf(b, "- %s\n", strings.Join(item, " "))
	if src := strings.TrimSpace(f.Source); src != "" {
		fence := "```"
		for strings.Contains(src, fence) {
			fence += "`"
		}
		lang := ""
		if strings.HasSuffix(f.File, ".go") {
			lang = "go"
		}
		fmt.Fprintf(b, "\n  %s%s\n  %s\n  %s\n\n", fence, lang, src, fence)
	}
}

// position returns file:line:col, file:line or file of a finding.
func position(f finding) string {
	switch {
	case f.File == "":
		return ""
	case f.Line == 0:
		return f.File
	case f.Col == 0:
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return fmt.Sprintf("%s:%d:%d", f.File, f.Line, f.Col)
}

// inlineEscaper escapes what Markdown would take for emphasis or HTML in a
// line of text from a tool. Backquotes stay: tools put code in them, as
// Markdown does.
var inlineEscaper = strings.NewReplacer(`*`, `\*`, `<`, `\<`)

func inline(s string) string { return inlineEscaper.Replace(s) }

// cell escapes text for a cell of a Markdown table.
func cell(s string) string { return strings.ReplaceAll(inline(s), "|", `\|`) }

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
