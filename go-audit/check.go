// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/version"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// golangciName is the file of the golangci-lint config of {golangci-config}:
// one's own in the store, or the built-in one in the output of a run.
const golangciName = "golangci.yml"

// waitDelay is how long a check may take to stop after an interrupt before
// it is killed.
const waitDelay = 5 * time.Second

// The statuses of a check.
const (
	statusOK          = "ok"          // ran and found nothing
	statusFound       = "found"       // ran and found something
	statusSkipped     = "skipped"     // not run: a program or a file is missing
	statusError       = "error"       // could not do its job
	statusInterrupted = "interrupted" // stopped by Ctrl-C
)

// An audit is one run of go-audit over a module: what it checks, where its
// output goes, and what the substitutions of the checks stand for there.
type audit struct {
	ctx         context.Context
	cfg         *config
	mod         goModule
	packages    []string // patterns as the checks get them in the module root
	store       string
	name        string // <time>-audit, the name of the run and of its report
	logs        string // the directory for the output of the checks
	time        time.Time
	concurrency int
	goVersion   string // go env GOVERSION in the module root, for the formats

	// Found on first use, as not every check needs them; only the goroutine
	// that prepares the commands touches them.
	goenv     map[string]string
	goenvErr  error
	files     []string
	filesErr  error
	filesDone bool
	golangci  string // the built-in golangci.yml in the logs, once written
}

// A result is what one check came to.
type result struct {
	check    *check
	status   string
	findings []finding
	note     string
	took     time.Duration
	ran      bool // whether the command ran, leaving its output in the logs
}

// command returns the command of a check with the substitutions made, or why
// the check is to be skipped.
func (a *audit) command(ch *check) (argv []string, skip string, err error) {
	if !substPart.MatchString(ch.Cmd[0]) {
		if _, err := exec.LookPath(ch.Cmd[0]); err != nil {
			return nil, missing(ch, ch.Cmd[0]), nil
		}
	}
	if ch.Go != "" {
		have, err := a.goEnv("GOVERSION")
		if err != nil {
			return nil, "", err
		}
		if version.IsValid(have) && version.Compare(have, "go"+ch.Go) < 0 {
			return nil, fmt.Sprintf("needs Go %s, the module builds with %s", ch.Go, have), nil
		}
	}
	for _, arg := range ch.Cmd {
		vals, skip, err := a.expand(ch, arg)
		if err != nil || skip != "" {
			return nil, skip, err
		}
		argv = append(argv, vals...)
	}
	return argv, "", nil
}

// missing says how to get a program a check needs.
func missing(ch *check, prog string) string {
	if ch.Install != "" {
		return "not installed: go install " + ch.Install
	}
	return prog + " is not in PATH"
}

// expand returns what arg of a check stands for: one argument, or several
// for {packages} and {files}, which validate keeps to arguments of their own.
func (a *audit) expand(ch *check, arg string) ([]string, string, error) {
	var b strings.Builder
	last := 0
	for _, m := range substPart.FindAllStringSubmatchIndex(arg, -1) {
		name, param := arg[m[2]:m[3]], ""
		if m[4] >= 0 {
			param = arg[m[4]:m[5]]
		}
		switch name {
		case "packages":
			return a.packages, "", nil
		case "files":
			files, err := a.goFiles()
			if err != nil {
				return nil, "", err
			}
			if len(files) == 0 {
				return nil, "no Go files in " + strings.Join(a.packages, " "), nil
			}
			return files, "", nil
		}
		v, skip, err := a.value(ch, name, param)
		if err != nil || skip != "" {
			return nil, skip, err
		}
		b.WriteString(arg[last:m[0]])
		b.WriteString(v)
		last = m[1]
	}
	b.WriteString(arg[last:])
	return []string{b.String()}, "", nil
}

// value returns what a substitution of one argument stands for, or why the
// check is to be skipped without it.
func (a *audit) value(ch *check, name, param string) (string, string, error) {
	switch name {
	case "module":
		return a.mod.path, "", nil
	case "gotooldir":
		dir, err := a.goEnv("GOTOOLDIR")
		return dir, "", err
	case "bin":
		path, err := exec.LookPath(param)
		if err != nil {
			return "", missing(ch, param), nil
		}
		path, err = filepath.Abs(path)
		return path, "", err
	case "file":
		path := filepath.Join(a.mod.root, param)
		if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
			return "", "no " + param, nil
		}
		return path, "", nil
	case "golangci-config":
		path := filepath.Join(a.store, golangciName)
		if _, err := os.Stat(path); err != nil {
			path, err = a.builtinGolangci()
			if err != nil {
				return "", "", err
			}
		}
		return "--config=" + path, "", nil
	case "project-golangci":
		path, err := a.projectGolangci()
		if err != nil || path == "" {
			return "", "no .golangci.yml", err
		}
		return "--config=" + path, "", nil
	}
	// validate lets no other name through.
	return "", "", fmt.Errorf("unknown substitution {%s}", name)
}

// goEnv returns the go env variable key in the module root, where
// GOTOOLCHAIN may pick another Go than the one in PATH.
func (a *audit) goEnv(key string) (string, error) {
	if a.goenv == nil && a.goenvErr == nil {
		out, err := a.output("go", "env", "-json", "GOTOOLDIR", "GOVERSION")
		if err == nil {
			err = json.Unmarshal(out, &a.goenv)
		}
		if err != nil {
			a.goenvErr = fmt.Errorf("go env: %v", err)
		}
	}
	return a.goenv[key], a.goenvErr
}

// goFiles returns the Go files of the packages, tests included, as go list
// sees them in the module root.
func (a *audit) goFiles() ([]string, error) {
	if a.filesDone {
		return a.files, a.filesErr
	}
	a.filesDone = true
	args := append([]string{"list", "-e", "-json=Dir,GoFiles,CgoFiles,TestGoFiles,XTestGoFiles"},
		a.packages...)
	out, err := a.output("go", args...)
	if err != nil {
		a.filesErr = fmt.Errorf("go list: %v", err)
		return nil, a.filesErr
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var p struct {
			Dir                                          string
			GoFiles, CgoFiles, TestGoFiles, XTestGoFiles []string
		}
		if err := dec.Decode(&p); err != nil {
			a.filesErr = fmt.Errorf("go list: %v", err)
			return nil, a.filesErr
		}
		for _, f := range slices.Concat(p.GoFiles, p.CgoFiles, p.TestGoFiles, p.XTestGoFiles) {
			a.files = append(a.files, filepath.Join(p.Dir, f))
		}
	}
	slices.Sort(a.files)
	a.files = slices.Compact(a.files)
	return a.files, nil
}

// builtinGolangci writes the built-in golangci.yml beside the output of the
// checks, for {golangci-config} when the store has none of its own, and
// returns its path. golangci-lint takes the settings of its linters from a
// file only, and those of exhaustruct are to be there all the same.
func (a *audit) builtinGolangci() (string, error) {
	if a.golangci == "" {
		path := filepath.Join(a.logs, golangciName)
		if err := os.WriteFile(path, builtinGolangci, 0o644); err != nil {
			return "", err
		}
		a.golangci = path
	}
	return a.golangci, nil
}

// projectGolangci returns the golangci-lint config the module would run
// with, as golangci-lint config path finds it from the module root, or ""
// if there is none.
func (a *audit) projectGolangci() (string, error) {
	cmd := exec.CommandContext(a.ctx, "golangci-lint", "config", "path")
	cmd.Dir = a.mod.root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.ExitCode() == 6 {
		return "", nil // no config
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		return "", fmt.Errorf("golangci-lint config path: %v %s", err, msg)
	}
	// It prints the path to stderr, maybe after warnings.
	var path string
	for line := range strings.Lines(stderr.String()) {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "level=") {
			path = line
		}
	}
	if path != "" && !filepath.IsAbs(path) {
		path = filepath.Join(a.mod.root, path)
	}
	return path, nil
}

// output runs a go command or another helper in the module root and returns
// its stdout; an error carries the first line of its stderr.
func (a *audit) output(name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(a.ctx, name, args...)
	cmd.Dir = a.mod.root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		o := newOutput(nil, nil, 0, a.mod.root)
		if msg := o.firstError(stderr.Bytes()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, err
	}
	return out, nil
}

// run runs a check with its command argv and reads its output into r. The
// output stays in the logs: <check>.out for stdout, <check>.log for stderr.
func (a *audit) run(r *result, argv []string) {
	start := time.Now()
	defer func() { r.took = time.Since(start) }()

	outFile, err := os.Create(filepath.Join(a.logs, r.check.Name+".out"))
	if err != nil {
		r.status, r.note = statusError, err.Error()
		return
	}
	logFile, err := os.Create(filepath.Join(a.logs, r.check.Name+".log"))
	if err != nil {
		_ = outFile.Close()
		r.status, r.note = statusError, err.Error()
		return
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(a.ctx, argv[0], argv[1:]...)
	cmd.Dir = a.mod.root
	cmd.Stdout = io.MultiWriter(outFile, &stdout)
	cmd.Stderr = io.MultiWriter(logFile, &stderr)
	cmd.Cancel = func() error { return interrupt(cmd.Process) }
	cmd.WaitDelay = waitDelay
	err = cmd.Run()
	r.ran = true
	closeErr := errors.Join(outFile.Close(), logFile.Close())
	if a.ctx.Err() != nil {
		r.status = statusInterrupted
		return
	}
	if closeErr != nil {
		r.status, r.note = statusError, "writing its output: "+closeErr.Error()
		return
	}
	code := 0
	if err != nil {
		ee, ok := errors.AsType[*exec.ExitError](err)
		if !ok {
			r.status, r.note = statusError, err.Error()
			return
		}
		code = ee.ExitCode()
	}

	o := newOutput(stdout.Bytes(), stderr.Bytes(), code, a.mod.root)
	o.module, o.goVersion = a.mod.path, a.goVersion
	fs, note, ferr := formats[r.check.Format](o)
	for i := range fs {
		fs[i].Check = r.check.Name
	}
	sortFindings(fs)
	r.findings = fs
	r.status, r.note = judge(o, fs, note, ferr, err)
	if r.status == statusFound && r.check.Note != "" {
		r.note = strings.TrimPrefix(r.note+" · "+r.check.Note, " · ")
	}
}

// judge returns the status of a check that ran, and its note. The exit code
// alone decides nothing, as tools exit 1 on findings; a check failed when
// its format says so, or when it exits non-zero and finds nothing. It was
// skipped when its format says it does not apply.
func judge(o *output, fs []finding, note string, ferr, runErr error) (string, string) {
	if skip, ok := errors.AsType[skipError](ferr); ok {
		return statusSkipped, string(skip)
	}
	switch {
	case ferr != nil:
		return statusError, ferr.Error()
	case o.code != 0 && len(fs) == 0:
		if msg := o.firstError(o.stderr); msg != "" {
			return statusError, msg
		}
		return statusError, runErr.Error()
	case len(fs) > 0:
		return statusFound, note
	}
	return statusOK, note
}

// interrupt asks a check to stop, as Ctrl-C in a terminal would; where there
// is no such signal, as on Windows, it kills the process.
func interrupt(p *os.Process) error {
	if err := p.Signal(os.Interrupt); err != nil {
		return p.Kill()
	}
	return nil
}
