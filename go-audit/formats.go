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
	module         string // the module path
	goVersion      string // the Go of the module root, such as go1.27.2

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
// it says the tool failed; a skipError, that the check does not apply to the
// module. Whatever the format, a check that exits non-zero and finds nothing
// failed as well (see judge).
type format func(o *output) ([]finding, string, error)

var formats = map[string]format{
	"vet-json":    vetJSON,
	"lines":       lineFindings,
	"diff":        diffFinding,
	"staticcheck": staticcheckJSON,
	"golangci":    golangciJSON,
	"gosec":       gosecJSON,
	"deadcode":    deadcodeJSON,
	"govulncheck": govulncheckJSON,
	"go-licenses": licenseLines,
}

// A skipError says why a check that ran does not apply to the module.
type skipError string

func (e skipError) Error() string { return string(e) }

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

// staticcheckJSON reads what staticcheck -f json prints: an object per
// problem. A package that does not build comes as a problem of the code
// "compile"; staticcheck exits 1 on findings and 2 or more when it failed.
func staticcheckJSON(o *output) ([]finding, string, error) {
	var fs []finding
	dec := json.NewDecoder(bytes.NewReader(o.stdout))
	for dec.More() {
		var p struct {
			Code     string `json:"code"`
			Location struct {
				File   string `json:"file"`
				Line   int    `json:"line"`
				Column int    `json:"column"`
			} `json:"location"`
			Message string `json:"message"`
		}
		if err := dec.Decode(&p); err != nil {
			return nil, "", fmt.Errorf("unreadable JSON: %v", err)
		}
		if p.Code == "compile" {
			return nil, "", errors.New(cmp.Or(o.firstError([]byte(p.Message)), p.Message))
		}
		fs = append(fs, finding{
			File: o.rel(p.Location.File), Line: p.Location.Line, Col: p.Location.Column,
			Rule: p.Code, Message: p.Message,
		})
	}
	if o.code > 1 {
		return nil, "", exitError(o, o.firstError(o.stderr))
	}
	return fs, "", nil
}

// exitError is the error of a check that failed: msg, or its exit code
// without one.
func exitError(o *output, msg string) error {
	if msg == "" {
		return fmt.Errorf("exit status %d", o.code)
	}
	return errors.New(msg)
}

// golangciJSON reads the JSON of golangci-lint run --output.json.path=stdout:
// one object with the issues, each with its lines of code. Lines that are
// not JSON, as a text output a project config adds, are left out. A package
// that does not build comes as an issue of the linter "typecheck".
func golangciJSON(o *output) ([]finding, string, error) {
	var fs []finding
	for line := range strings.Lines(string(o.stdout)) {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var out struct {
			Issues []struct {
				FromLinter  string
				Text        string
				SourceLines []string
				Pos         struct {
					Filename     string
					Line, Column int
				}
			}
		}
		if err := json.Unmarshal([]byte(line), &out); err != nil {
			return nil, "", fmt.Errorf("unreadable JSON: %v", err)
		}
		for _, is := range out.Issues {
			if is.FromLinter == "typecheck" {
				// The text is ": " and what the go command said.
				text := strings.TrimPrefix(is.Text, ": ")
				return nil, "", errors.New(cmp.Or(o.firstError([]byte(text)), text))
			}
			f := finding{
				File: o.rel(is.Pos.Filename), Line: is.Pos.Line, Col: is.Pos.Column,
				Rule: is.FromLinter, Message: is.Text,
			}
			if len(is.SourceLines) > 0 {
				f.Source = is.SourceLines[0]
			}
			fs = append(fs, f)
		}
	}
	return fs, "", nil
}

// gosecJSON reads what gosec -fmt=json prints. gosec exits 1 both on issues
// and on packages it could not type-check, which it lists under "Golang
// errors"; the issues are then only what the syntax shows, so that is an
// error, not a quiet partial result. A gosec built with an older Go cannot
// read the export data of a newer one.
func gosecJSON(o *output) ([]finding, string, error) {
	if len(bytes.TrimSpace(o.stdout)) == 0 {
		if o.code == 0 {
			return nil, "", nil
		}
		return nil, "", exitError(o, gosecReason(o))
	}
	var out struct {
		Errors map[string][]struct {
			Line  int    `json:"line"`
			Error string `json:"error"`
		} `json:"Golang errors"`
		Issues []struct {
			RuleID  string `json:"rule_id"`
			Details string `json:"details"`
			File    string `json:"file"`
			Line    string `json:"line"`   // a number or a range: 12-14
			Column  string `json:"column"` // a number
		} `json:"Issues"`
	}
	if err := json.Unmarshal(o.stdout, &out); err != nil {
		return nil, "", fmt.Errorf("unreadable JSON: %v", err)
	}
	var errs []string
	// The errors of the go command come under "", which sorts first.
	for _, file := range slices.Sorted(maps.Keys(out.Errors)) {
		for _, e := range out.Errors[file] {
			if strings.Contains(e.Error, "export data version") {
				return nil, "", fmt.Errorf("cannot read %s packages: update gosec",
					cmp.Or(o.goVersion, "this Go's"))
			}
			if file == "" {
				errs = append(errs, e.Error)
			} else {
				errs = append(errs, fmt.Sprintf("%s:%d: %s", o.rel(file), e.Line, e.Error))
			}
		}
	}
	if len(errs) > 0 {
		return nil, "", errors.New(cmp.Or(o.firstError([]byte(errs[0])), errs[0]))
	}
	fs := make([]finding, 0, len(out.Issues))
	for _, is := range out.Issues {
		first, _, _ := strings.Cut(is.Line, "-")
		line, _ := strconv.Atoi(first)
		col, _ := strconv.Atoi(is.Column)
		fs = append(fs, finding{
			File: o.rel(is.File), Line: line, Col: col,
			Rule: is.RuleID, Message: is.Details,
		})
	}
	return fs, "", nil
}

// gosecLog matches the start of a line gosec logs: [gosec] and the time.
var gosecLog = regexp.MustCompile(`^\[gosec\] \d{4}/\d\d/\d\d \d\d:\d\d:\d\d `)

// gosecReason returns why gosec printed no report: the last line it logged,
// as the lines before it say what it was going to do.
func gosecReason(o *output) string {
	last := ""
	for line := range strings.Lines(string(o.stderr)) {
		if m := gosecLog.FindString(line); m != "" {
			last = line[len(m):]
		}
	}
	if last == "" {
		return o.firstError(o.stderr)
	}
	return o.firstError([]byte(last))
}

// deadcodeJSON reads what deadcode -json prints: the packages with the
// functions that no main package and no test reaches. With neither there is
// nothing to start from, and the check does not apply.
func deadcodeJSON(o *output) ([]finding, string, error) {
	if o.code != 0 && bytes.Contains(o.stderr, []byte("no main packages")) {
		return nil, "", skipError("no main package and no tests")
	}
	if len(bytes.TrimSpace(o.stdout)) == 0 {
		return nil, "", nil
	}
	var pkgs []struct {
		Funcs []struct {
			Name     string
			Position struct {
				File      string
				Line, Col int
			}
		}
	}
	if err := json.Unmarshal(o.stdout, &pkgs); err != nil {
		return nil, "", fmt.Errorf("unreadable JSON: %v", err)
	}
	var fs []finding
	for _, p := range pkgs {
		for _, fn := range p.Funcs {
			fs = append(fs, finding{
				File: o.rel(fn.Position.File), Line: fn.Position.Line, Col: fn.Position.Col,
				Message: "unreachable func: " + fn.Name,
			})
		}
	}
	return fs, "", nil
}

// A vulnFrame is one step of the trace of a govulncheck finding: the
// vulnerable symbol first, the code of the module that reaches it last.
type vulnFrame struct {
	Module   string `json:"module"`
	Package  string `json:"package"`
	Function string `json:"function"`
	Receiver string `json:"receiver"`
	Position *struct {
		Filename string `json:"filename"`
		Line     int    `json:"line"`
		Column   int    `json:"column"`
	} `json:"position"`
}

// govulncheckJSON reads what govulncheck -format json prints: a stream of
// messages, among them the vulnerabilities (osv) and the findings. A finding
// whose trace starts at a function is a vulnerability the code calls, and a
// finding of the check, once per vulnerability; the others are in packages
// or modules it only imports or requires, and go into the note. govulncheck
// exits 0 with findings.
func govulncheckJSON(o *output) ([]finding, string, error) {
	db := "vuln.go.dev"
	summaries := map[string]string{}
	all := map[string]bool{}    // the vulnerabilities with any finding
	called := map[string]bool{} // those the code calls
	var fs []finding
	dec := json.NewDecoder(bytes.NewReader(o.stdout))
	for dec.More() {
		var msg struct {
			Config *struct {
				DB string `json:"db"`
			} `json:"config"`
			OSV *struct {
				ID      string `json:"id"`
				Summary string `json:"summary"`
			} `json:"osv"`
			Finding *struct {
				OSV   string      `json:"osv"`
				Fixed string      `json:"fixed_version"`
				Trace []vulnFrame `json:"trace"`
			} `json:"finding"`
		}
		if err := dec.Decode(&msg); err != nil {
			return nil, "", fmt.Errorf("unreadable JSON: %v", err)
		}
		switch {
		case msg.Config != nil && msg.Config.DB != "":
			db = strings.TrimPrefix(strings.TrimPrefix(msg.Config.DB, "https://"), "http://")
		case msg.OSV != nil:
			summaries[msg.OSV.ID] = msg.OSV.Summary
		case msg.Finding != nil:
			f := msg.Finding
			all[f.OSV] = true
			if len(f.Trace) > 0 && f.Trace[0].Function != "" && !called[f.OSV] {
				called[f.OSV] = true
				fs = append(fs, vulnFinding(o, f.OSV, f.Fixed, f.Trace))
			}
		}
	}
	if o.code != 0 && bytes.Contains(o.stderr, []byte("fetching vulnerabilities")) {
		return nil, "", fmt.Errorf("%s unreachable (offline? -skip govulncheck)", db)
	}
	// The vulnerabilities may come after their findings.
	for i := range fs {
		fs[i].Message = cmp.Or(summaries[fs[i].Rule], fs[i].Rule) + fs[i].Message
	}
	note := ""
	if n := len(all) - len(fs); n > 0 {
		note = fmt.Sprintf("%d more in dependencies, not called", n)
	}
	return fs, note, nil
}

// vulnFinding makes a finding of a vulnerability the code calls, at the
// last step of its trace in the module. Its message is what follows the
// summary of the vulnerability.
func vulnFinding(o *output, id, fixed string, trace []vulnFrame) finding {
	sym := trace[0]
	name := sym.Package + "." + sym.Function
	if sym.Receiver != "" {
		name = sym.Package + "." + strings.TrimPrefix(sym.Receiver, "*") + "." + sym.Function
	}
	fix := "no fixed version"
	if fixed != "" {
		fix = "fixed in " + sym.Module + "@" + fixed
	}
	f := finding{Rule: id, Message: fmt.Sprintf(": %s is called; %s", name, fix)}
	for _, fr := range slices.Backward(trace) {
		if fr.Module == o.module && fr.Position != nil {
			f.File, f.Line, f.Col = o.rel(fr.Position.Filename), fr.Position.Line, fr.Position.Column
			break
		}
	}
	return f
}

// The lines of go-licenses check about a library, on stderr.
var (
	licenseUnknown = regexp.MustCompile(`^Did not find license for library '(.+)'\.$`)
	licenseName    = regexp.MustCompile(`^Not allowed license '(.+)' found for library '(.+)'\.$`)
	licenseType    = regexp.MustCompile(
		`^License '(.+)' of not allowed license type '(.+)' found for library '(.+)'\.$`)
)

// klogHeader matches the start of a line go-licenses logs:
// W1016 16:55:11.251504    6764 library.go:141] …
var klogHeader = regexp.MustCompile(`^[IWEF]\d{4} \d\d:\d\d:\d\d\.\d+ +\d+ [^\]]+\] `)

// licenseLines reads what go-licenses check prints on stderr: a line per
// library whose license is unknown or not allowed, among logged warnings and
// the files they name. It exits 1 on such a library; without one, that is
// a failure, logged with E or F.
func licenseLines(o *output) ([]finding, string, error) {
	var fs []finding
	for line := range strings.Lines(string(o.stderr)) {
		line = strings.TrimRight(line, "\r\n")
		if m := licenseUnknown.FindStringSubmatch(line); m != nil {
			fs = append(fs, finding{Rule: "unknown", Message: m[1] + ": no license found"})
		} else if m := licenseName.FindStringSubmatch(line); m != nil {
			fs = append(fs, finding{Rule: m[1], Message: m[2] + ": license " + m[1] + " is not allowed"})
		} else if m := licenseType.FindStringSubmatch(line); m != nil {
			fs = append(fs, finding{
				Rule:    m[1],
				Message: fmt.Sprintf("%s: license %s of the type %s is not allowed", m[3], m[1], m[2]),
			})
		}
	}
	if o.code != 0 && len(fs) == 0 {
		return nil, "", exitError(o, licenseError(o))
	}
	return fs, "", nil
}

// licenseError returns why go-licenses failed: the first line worth showing
// of what it logged as an error, E or F, and the lines that follow it.
func licenseError(o *output) string {
	var b strings.Builder
	on := false
	for line := range strings.Lines(string(o.stderr)) {
		if m := klogHeader.FindString(line); m != "" {
			on = line[0] == 'E' || line[0] == 'F'
			line = line[len(m):]
		}
		if on {
			b.WriteString(line)
		}
	}
	return o.firstError([]byte(b.String()))
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
