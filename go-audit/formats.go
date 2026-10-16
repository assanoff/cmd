// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A finding is one problem a check reports.
type finding struct {
	Check string `json:"check"`
	// File is relative to the module root, with slashes; a file outside the
	// module keeps its absolute path. A finding about no file in particular,
	// such as a go.mod that is not tidy, has none.
	File    string `json:"file"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
	// Source is the line of code at the time of the run.
	Source string `json:"source,omitempty"`
}

// output is what a check printed, for its format to read.
type output struct {
	stdout, stderr []byte
	code           int    // the exit code
	root           string // the module root, cut from paths

	realRoot string // root with symbolic links resolved, if that differs
}

func newOutput(stdout, stderr []byte, code int, root string) *output {
	o := &output{stdout: stdout, stderr: stderr, code: code, root: root}
	// Tools print paths as the operating system gives them, and a temporary
	// directory on macOS is reached through a link (/var → /private/var).
	if real, err := filepath.EvalSymlinks(root); err == nil && real != root {
		o.realRoot = real
	}
	return o
}

// A format turns the output of a check into findings and an optional note.
// An error means the check did not do its job: the output does not read, or
// it says the tool failed. Whatever the format, a check that exits non-zero
// and finds nothing failed as well (see judge).
type format func(o *output) ([]finding, string, error)

var formats = map[string]format{
	"vet-json": vetJSON,
	"lines":    lineFindings,
	"diff":     diffFinding,
}

func formatNames() string {
	return strings.Join(slices.Sorted(maps.Keys(formats)), ", ")
}

// vetJSON reads the JSON that go vet -json prints, and analysis drivers such
// as nilaway with -json: {"package": {"analyzer": [diagnostic…]}}, one such
// object after another, among lines of text. An analyzer that failed has
// {"error": "…"} in place of its diagnostics; so does a package that did
// not build, while go vet itself prints text for it and exits 1.
func vetJSON(o *output) ([]finding, string, error) {
	var (
		fs   []finding
		errs []string
		text bytes.Buffer // the lines that are not JSON
	)
	for _, data := range [][]byte{o.stdout, o.stderr} {
		for len(data) > 0 {
			line, rest, _ := bytes.Cut(data, []byte("\n"))
			if !bytes.HasPrefix(line, []byte("{")) {
				text.Write(line)
				text.WriteByte('\n')
				data = rest
				continue
			}
			var tree map[string]map[string]json.RawMessage
			dec := json.NewDecoder(bytes.NewReader(data))
			if err := dec.Decode(&tree); err != nil {
				return nil, "", fmt.Errorf("unreadable JSON: %v", err)
			}
			data = data[dec.InputOffset():]
			for _, analyzers := range tree {
				for name, raw := range analyzers {
					more, msg, err := vetDiagnostics(o, name, raw)
					if err != nil {
						return nil, "", err
					}
					fs = append(fs, more...)
					if msg != "" {
						errs = append(errs, msg)
					}
				}
			}
		}
	}
	if o.code == 0 && len(errs) == 0 {
		return fs, "", nil
	}
	// The text says what went wrong; the errors of the analyzers mostly say
	// that one could not run because another could not.
	msg := o.firstError(text.Bytes())
	if msg == "" && len(errs) > 0 {
		slices.SortFunc(errs, func(a, b string) int {
			return cmp.Or(cmp.Compare(consequence(a), consequence(b)), strings.Compare(a, b))
		})
		msg = o.firstError([]byte(errs[0]))
	}
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", o.code)
	}
	return fs, "", errors.New(msg)
}

// consequence reports whether an error of an analyzer only says that it did
// not run because something else failed: 1 if so, for sorting.
func consequence(msg string) int {
	if strings.HasPrefix(msg, "failed prerequisites") ||
		strings.HasPrefix(msg, "analysis skipped due to errors") {
		return 1
	}
	return 0
}

// vetDiagnostics reads what one analyzer reported: its diagnostics, or the
// message of its error.
func vetDiagnostics(o *output, analyzer string, raw json.RawMessage) ([]finding, string, error) {
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		var failed struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(raw, &failed); err != nil {
			return nil, "", fmt.Errorf("unreadable JSON of %s: %v", analyzer, err)
		}
		return nil, failed.Error, nil
	}
	var diags []struct {
		Posn    string `json:"posn"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &diags); err != nil {
		return nil, "", fmt.Errorf("unreadable JSON of %s: %v", analyzer, err)
	}
	fs := make([]finding, 0, len(diags))
	for _, d := range diags {
		file, line, col := splitPos(d.Posn)
		fs = append(fs, finding{
			File: o.rel(file), Line: line, Col: col,
			Rule: analyzer, Message: d.Message,
		})
	}
	return fs, "", nil
}

// posTail matches the end of a position: :line or :line:col.
var posTail = regexp.MustCompile(`:(\d+)(?::(\d+))?$`)

// splitPos splits file:line:col, file:line or file into its parts. The file
// may hold colons itself, as C:\src\a.go does.
func splitPos(pos string) (file string, line, col int) {
	m := posTail.FindStringSubmatchIndex(pos)
	if m == nil {
		return pos, 0, 0
	}
	line, _ = strconv.Atoi(pos[m[2]:m[3]])
	if m[4] >= 0 {
		col, _ = strconv.Atoi(pos[m[4]:m[5]])
	}
	return pos[:m[0]], line, col
}

// linePos matches a finding in a line: file:line[:col[-end]]: message, where
// end is col or line:col, as gopls check prints them.
var linePos = regexp.MustCompile(`^(.+?):(\d+)(?::(\d+)(?:-\d+(?::\d+)?)?)?: (.*)$`)

// lineFindings reads a finding from each line of the output: file:line[:col]:
// message, as gopls check and compilers print them, or a file name alone, as
// gofmt -l does. A tool that exits non-zero with something on stderr failed,
// even when it listed findings: gofmt -l lists the files it could read, and
// says why it could not read the rest.
func lineFindings(o *output) ([]finding, string, error) {
	var fs []finding
	for line := range strings.Lines(string(o.stdout)) {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := linePos.FindStringSubmatch(line)
		if m == nil {
			fs = append(fs, finding{File: o.rel(line)})
			continue
		}
		n, _ := strconv.Atoi(m[2])
		col, _ := strconv.Atoi(m[3])
		fs = append(fs, finding{File: o.rel(m[1]), Line: n, Col: col, Message: m[4]})
	}
	if o.code != 0 && len(bytes.TrimSpace(o.stderr)) > 0 {
		return fs, "", errors.New(o.firstError(o.stderr))
	}
	return fs, "", nil
}

// diffFinding makes one finding of any output, as go mod tidy -diff prints
// when go.mod is not tidy; its first line is the message, and the whole of it
// is in the check's output file.
func diffFinding(o *output) ([]finding, string, error) {
	text := strings.TrimSpace(string(o.stdout))
	if text == "" {
		return nil, "", nil
	}
	first, _, _ := strings.Cut(text, "\n")
	return []finding{{Message: o.trim(strings.TrimSpace(first))}}, "", nil
}

// rel returns path relative to the module root, with slashes, if it is in
// the module; a path elsewhere stays as it is.
func (o *output) rel(path string) string {
	if !filepath.IsAbs(path) {
		return filepath.ToSlash(filepath.Clean(path))
	}
	for _, root := range []string{o.root, o.realRoot} {
		if root == "" {
			continue
		}
		if rel, err := filepath.Rel(root, path); err == nil && filepath.IsLocal(rel) {
			return filepath.ToSlash(rel)
		}
	}
	return path
}

// trim cuts the module root out of the paths in a line of text.
func (o *output) trim(s string) string {
	for _, root := range []string{o.root, o.realRoot} {
		if root != "" {
			s = strings.ReplaceAll(s, root+string(filepath.Separator), "")
		}
	}
	return s
}

// maxNote is how many characters of an error go into a note.
const maxNote = 160

// firstError returns the first line of text worth showing as the reason a
// check failed: not a "# package" header, not a lead-in that ends in a colon,
// with the module root cut out of its paths.
func (o *output) firstError(text []byte) string {
	for line := range strings.Lines(string(text)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasSuffix(line, ":") {
			continue
		}
		return shorten(o.trim(line), maxNote)
	}
	return ""
}

// shorten cuts s to at most n characters, marking the cut.
func shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// sortFindings puts findings in the order of the code: by file, line and
// column.
func sortFindings(fs []finding) {
	slices.SortStableFunc(fs, func(a, b finding) int {
		return cmp.Or(
			strings.Compare(a.File, b.File),
			cmp.Compare(a.Line, b.Line),
			cmp.Compare(a.Col, b.Col),
			strings.Compare(a.Rule, b.Rule),
			strings.Compare(a.Message, b.Message),
		)
	})
}
