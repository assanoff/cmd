// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A scope is what of the findings an audit reports: all of them, or those
// in the files named, in the lines that changed in git, or in all files but
// the tests. The checks run over the packages all the same, as most tools
// take nothing smaller than a package and some need the whole program; the
// scope only leaves findings out.
type scope struct {
	files   map[string]bool        // the files named, module-relative; nil: all
	changes map[string][]lineRange // the changed lines by file; nil: all lines
	whole   bool                   // all lines of a changed file, not only those changed
	noTests bool                   // the _test.go files left out
}

// A lineRange is the lines first to last of a file, both included.
type lineRange struct{ first, last int }

// allLines stands for every line of a file git does not track yet.
var allLines = lineRange{1, math.MaxInt}

// narrow reports whether the scope keeps only some files, which leaves out
// the findings about no file in particular as well, such as a go.mod that is
// not tidy.
func (s *scope) narrow() bool {
	return s.files != nil || s.changes != nil
}

// hasFile reports whether the file at the module-relative path is in scope.
func (s *scope) hasFile(path string) bool {
	switch {
	case s.noTests && strings.HasSuffix(path, "_test.go"):
		return false
	case s.files != nil && !s.files[path]:
		return false
	case s.changes != nil:
		_, ok := s.changes[path]
		return ok
	}
	return true
}

// has reports whether a finding is in scope. A finding about a whole file,
// with no line, is in when its file is.
func (s *scope) has(f finding) bool {
	switch {
	case f.File == "":
		return !s.narrow()
	case !s.hasFile(f.File):
		return false
	case s.changes == nil || s.whole || f.Line == 0:
		return true
	}
	for _, r := range s.changes[f.File] {
		if r.first <= f.Line && f.Line <= r.last {
			return true
		}
	}
	return false
}

// filter leaves the findings out of scope out of r and says how many in its
// note; a check that found nothing else is ok.
func (s *scope) filter(r *result) {
	n := len(r.findings)
	r.findings = slices.DeleteFunc(r.findings, func(f finding) bool {
		return !s.has(f)
	})
	out := n - len(r.findings)
	if out == 0 {
		return
	}
	r.note = strings.TrimPrefix(fmt.Sprintf("%s · %d out of scope", r.note, out), " · ")
	if r.status == statusFound && len(r.findings) == 0 {
		r.status = statusOK
	}
}

// gitChanges returns the lines of the Go files of the module that changed in
// git, by module-relative path: with rev, those changed since the commit
// where HEAD and rev part ways, committed or not; with staged, those in the
// index; with unstaged, those in the working tree that are not staged. Both
// rev and unstaged take in the files git does not track yet, whole. The
// lines of the index are mapped onto the working tree, where the checks read
// the files: a staged line changed again since is no longer there.
func (a *audit) gitChanges(rev string, staged, unstaged bool) (map[string][]lineRange, error) {
	if _, err := a.git("rev-parse", "--is-inside-work-tree"); err != nil {
		return nil, err
	}
	changes := map[string][]lineRange{}
	add := func(path string, lines []lineRange) {
		if len(lines) > 0 {
			changes[path] = append(changes[path], lines...)
		}
	}
	if rev != "" {
		if _, err := a.git("rev-parse", "--verify", "--quiet", rev+"^{commit}"); err != nil {
			return nil, usagef("-diff: no commit %s in git", rev)
		}
		base := rev
		if mb, err := a.git("merge-base", "HEAD", rev); err == nil {
			base = strings.TrimSpace(mb)
		}
		diff, err := a.gitDiff(base)
		if err != nil {
			return nil, err
		}
		for path, hunks := range diff {
			add(path, newLines(hunks))
		}
	}
	if staged || unstaged {
		worktree, err := a.gitDiff()
		if err != nil {
			return nil, err
		}
		if staged {
			index, err := a.gitDiff("--cached")
			if err != nil {
				return nil, err
			}
			for path, hunks := range index {
				add(path, toWorktree(newLines(hunks), worktree[path]))
			}
		}
		if unstaged {
			for path, hunks := range worktree {
				add(path, newLines(hunks))
			}
		}
	}
	if rev != "" || unstaged {
		out, err := a.git("ls-files", "--others", "--exclude-standard", "-z", "--", "*.go")
		if err != nil {
			return nil, err
		}
		for path := range strings.SplitSeq(out, "\x00") {
			if path != "" {
				changes[path] = []lineRange{allLines}
			}
		}
	}
	return changes, nil
}

// git runs the git command cmd in the module root and returns what it
// printed. Paths come out as they are, not quoted, unless they hold quotes or
// control characters.
func (a *audit) git(cmd string, args ...string) (string, error) {
	out, err := a.output("git", slices.Concat([]string{"-c", "core.quotePath=false", cmd},
		args)...)
	if err != nil {
		return "", fmt.Errorf("git %s: %v", cmd, err)
	}
	return string(out), nil
}

// gitDiff returns the hunks of the Go files in git diff with args, by path
// relative to the module root, the files outside it left out. The prefixes
// are set, as diff.noprefix and diff.mnemonicPrefix would change them.
func (a *audit) gitDiff(args ...string) (map[string][]hunk, error) {
	out, err := a.git("diff", slices.Concat([]string{"--no-color", "--no-ext-diff",
		"--no-textconv", "--relative", "--unified=0", "--src-prefix=a/", "--dst-prefix=b/"},
		args, []string{"--", "*.go"})...)
	if err != nil {
		return nil, err
	}
	return parseDiff(out), nil
}

// A hunk is a part of a diff: the lines oldStart on of the old side, oldLen
// of them, became newLen lines from newStart on of the new side. A hunk with
// no old lines puts its lines after the line oldStart; one with no new lines
// puts none.
type hunk struct{ oldStart, oldLen, newStart, newLen int }

// hunkHeader matches the line that starts a hunk: @@ -12,3 +12,4 @@.
var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// parseDiff returns the hunks of a diff by the path of their new side; a
// deleted file has none. The diff is to have no lines of context and the
// prefix b/ on the new side.
func parseDiff(diff string) map[string][]hunk {
	hunks := map[string][]hunk{}
	path, header := "", false
	for line := range strings.Lines(diff) {
		line = strings.TrimSuffix(line, "\n")
		switch {
		case strings.HasPrefix(line, "diff "):
			path, header = "", true
		case header && strings.HasPrefix(line, "+++ "):
			// The line of the new side; in a hunk, "+++ " is a line of code.
			path = diffPath(strings.TrimPrefix(line, "+++ "))
		case strings.HasPrefix(line, "@@ "):
			header = false
			if h, ok := parseHunk(line); ok && path != "" {
				hunks[path] = append(hunks[path], h)
			}
		}
	}
	return hunks
}

// diffPath returns the path in the +++ line of a diff, "" for /dev/null. Git
// puts a path in quotes when it holds a quote or a control character, and a
// tab after it when it holds a space.
func diffPath(s string) string {
	s = strings.TrimSuffix(s, "\t")
	if s == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			s = u
		}
	}
	return strings.TrimPrefix(s, "b/")
}

// parseHunk returns the hunk whose header is line, or false if it is none.
func parseHunk(line string) (hunk, bool) {
	m := hunkHeader.FindStringSubmatch(line)
	if len(m) != 5 {
		return hunk{}, false
	}
	var n [4]int
	for i, s := range m[1:] {
		if s == "" {
			n[i] = 1 // git leaves out the length of a side when it is 1
			continue
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return hunk{}, false
		}
		n[i] = v
	}
	return hunk{n[0], n[1], n[2], n[3]}, true
}

// newLines returns the lines the hunks put on their new side.
func newLines(hunks []hunk) []lineRange {
	var rs []lineRange
	for _, h := range hunks {
		if h.newLen > 0 {
			rs = append(rs, lineRange{h.newStart, h.newStart + h.newLen - 1})
		}
	}
	return rs
}

// toWorktree maps lines of the index onto the working tree through the
// hunks of the changes not staged: a line those changed is gone, and one
// after a hunk moves by the lines the hunk added less those it removed.
func toWorktree(rs []lineRange, unstaged []hunk) []lineRange {
	var out []lineRange
	for _, r := range rs {
		for line := r.first; line <= r.last; line++ {
			moved, ok := moveLine(line, unstaged)
			if !ok {
				continue
			}
			if len(out) > 0 && out[len(out)-1].last+1 == moved {
				out[len(out)-1].last = moved
				continue
			}
			out = append(out, lineRange{moved, moved})
		}
	}
	return out
}

// moveLine returns where a line of the old side of the hunks is on their
// new side, or false when they changed it. The hunks are in the order of the
// file, as git prints them.
func moveLine(line int, hunks []hunk) (int, bool) {
	shift := 0
	for _, h := range hunks {
		switch {
		case h.oldLen == 0 && line <= h.oldStart:
			return line + shift, true
		case h.oldLen > 0 && line < h.oldStart:
			return line + shift, true
		case h.oldLen > 0 && line < h.oldStart+h.oldLen:
			return 0, false
		}
		shift += h.newLen - h.oldLen
	}
	return line + shift, true
}
