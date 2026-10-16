// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// auditUsage is the usage line of go-audit running its checks.
const auditUsage = "[-c N] [-only a,b] [-skip a,b] [packages]"

func auditCommand() *command {
	fs := flag.NewFlagSet("go-audit", flag.ContinueOnError)
	c := fs.Int("c", 1, "run up to `N` checks at a time")
	only := fs.String("only", "", "run only the checks `a,b`, those that are off as well")
	skip := fs.String("skip", "", "run all checks but `a,b`")
	return &command{
		usage: auditUsage,
		short: "Run the checks over the packages, ./... by default, and write a report",
		flags: fs,
		run: func(ctx context.Context, args []string) error {
			return runAudit(ctx, *c, *only, *skip, args)
		},
	}
}

// exitStatus is an error that sets the exit code alone: what it stands for
// is printed already.
type exitStatus int

func (e exitStatus) Error() string {
	return fmt.Sprintf("exit status %d", int(e))
}

// runAudit runs the checks over the packages in args and writes the report.
// It returns exitStatus 1 when a check found something and exitFailure when
// one could not do its job or the run was interrupted.
func runAudit(ctx context.Context, concurrency int, only, skip string, args []string) error {
	if concurrency < 1 {
		return usagef("-c is how many checks run at a time: 1 or more, not %d", concurrency)
	}
	store, err := storeDir()
	if err != nil {
		return err
	}
	cfg, err := loadConfig(store)
	if err != nil {
		return err
	}
	checks, err := selectChecks(cfg, only, skip)
	if err != nil {
		return err
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	mod, err := findModule(ctx, wd)
	if err != nil {
		return err
	}
	pkgs, err := rebase(wd, mod.root, args)
	if err != nil {
		return err
	}

	a := &audit{
		ctx:         ctx,
		cfg:         cfg,
		mod:         mod,
		packages:    pkgs,
		store:       store,
		time:        time.Now().Truncate(time.Second),
		concurrency: concurrency,
	}
	if err := a.makeRun(); err != nil {
		return err
	}

	p := newPrinter(stdout)
	p.header(a)
	start := time.Now()
	results := a.runChecks(p, checks)
	wall := time.Since(start)
	if ctx.Err() != nil {
		p.dim("logs:   %s\n", a.logs)
		return exitStatus(exitFailure)
	}
	md, err := a.writeReport(results, wall)
	if err != nil {
		return err
	}
	p.dim("wall %s with -c %d\n", duration(wall), concurrency)
	p.dim("report: %s\n", md)
	p.dim("logs:   %s\n", a.logs)

	code := 0
	for _, r := range results {
		switch r.status {
		case statusError:
			code = exitFailure
		case statusFound:
			code = max(code, 1)
		}
	}
	if code != 0 {
		return exitStatus(code)
	}
	return nil
}

// selectChecks returns the checks to run, in the order of the config:
// those named by only, off or not, or else all that are on; less those named
// by skip.
func selectChecks(cfg *config, only, skip string) ([]*check, error) {
	known := map[string]bool{}
	var names []string
	for _, ch := range cfg.Checks {
		known[ch.Name] = true
		names = append(names, ch.Name)
	}
	list := func(flag, s string) (map[string]bool, error) {
		set := map[string]bool{}
		for name := range strings.SplitSeq(s, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if !known[name] {
				return nil, usagef("-%s: no check %s in the %s config (checks: %s)",
					flag, name, cfg.source(), strings.Join(names, ", "))
			}
			set[name] = true
		}
		return set, nil
	}
	onlySet, err := list("only", only)
	if err != nil {
		return nil, err
	}
	skipSet, err := list("skip", skip)
	if err != nil {
		return nil, err
	}
	var checks []*check
	for _, ch := range cfg.Checks {
		switch {
		case skipSet[ch.Name]:
		case len(onlySet) > 0:
			if onlySet[ch.Name] {
				checks = append(checks, ch)
			}
		case !ch.Off:
			checks = append(checks, ch)
		}
	}
	if len(checks) == 0 {
		return nil, usagef("no check is left to run")
	}
	return checks, nil
}

// rebase returns the package patterns in args, which are relative to the
// directory dir, as patterns relative to the module root, where the checks
// run. Import paths stay as they are; no args stands for ./... in dir, as
// with the go command.
func rebase(dir, root string, args []string) ([]string, error) {
	if len(args) == 0 {
		args = []string{"./..."}
	}
	var pkgs []string
	for _, arg := range args {
		if !isLocalPattern(arg) {
			pkgs = append(pkgs, arg)
			continue
		}
		path, dots := arg, ""
		if p, ok := strings.CutSuffix(arg, "/..."); ok {
			path, dots = p, "/..."
		} else if p, ok := strings.CutSuffix(arg, "..."); ok {
			path, dots = p, "..." // ./cmd... is cmd, cmdline and the like
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel != "." && !filepath.IsLocal(rel) {
			return nil, usagef("%s is outside the module in %s", arg, root)
		}
		if rel == "." {
			pkgs = append(pkgs, "."+dots)
			continue
		}
		pkgs = append(pkgs, "./"+filepath.ToSlash(rel)+dots)
	}
	return pkgs, nil
}

// isLocalPattern reports whether a package pattern is a directory rather
// than an import path, as the go command tells them apart.
func isLocalPattern(arg string) bool {
	return arg == "." || arg == ".." || filepath.IsAbs(arg) ||
		strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") ||
		strings.HasPrefix(arg, `.\`) || strings.HasPrefix(arg, `..\`)
}

// makeRun names the run and makes the directory for the output of its
// checks: runs/<module>/<time>-audit in the store, with -2, -3… after a run
// of the same second, so that the report of the same name is new as well.
func (a *audit) makeRun() error {
	runs, err := moduleDir(a.store, "runs", a.mod.path)
	if err != nil {
		return err
	}
	reports, err := moduleDir(a.store, "reports", a.mod.path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(runs, 0o755); err != nil {
		return err
	}
	base := a.time.Format(stampLayout) + "-audit"
	for n := 1; ; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		if _, err := os.Stat(filepath.Join(reports, name+".md")); err == nil {
			continue
		}
		err := os.Mkdir(filepath.Join(runs, name), 0o755)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		a.name, a.logs = name, filepath.Join(runs, name)
		return nil
	}
}

// runChecks runs the checks, up to a.concurrency at a time, and prints a
// line for each as soon as it and all those before it are done, so that the
// lines are in the order of the config whatever the order of finishing.
// After an interrupt it prints "interrupted" and waits for the running
// checks to stop.
func (a *audit) runChecks(p *printer, checks []*check) []*result {
	results := make([]*result, len(checks))
	done := make([]chan struct{}, len(checks))
	slots := make(chan struct{}, a.concurrency)
	var wg sync.WaitGroup
	for i, ch := range checks {
		r := &result{check: ch}
		results[i] = r
		done[i] = make(chan struct{})
		// The commands are made here, one after another, as the values of the
		// substitutions are found on first use.
		argv, skip, err := a.command(ch)
		switch {
		case err != nil:
			r.status, r.note = statusError, err.Error()
			close(done[i])
			continue
		case skip != "":
			r.status, r.note = statusSkipped, skip
			close(done[i])
			continue
		}
		wg.Go(func() {
			defer close(done[i])
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-a.ctx.Done():
				r.status = statusInterrupted
				return
			}
			if a.ctx.Err() != nil {
				r.status = statusInterrupted
				return
			}
			a.run(r, argv)
		})
	}

	width := 0
	for _, ch := range checks {
		width = max(width, len(ch.Name))
	}
	for i, r := range results {
		p.name(width, r.check.Name)
		<-done[i]
		if r.status == statusInterrupted || a.ctx.Err() != nil {
			p.interrupted()
			break
		}
		p.row(r)
	}
	wg.Wait()
	return results
}

// A printer writes the summary, in color on a terminal.
type printer struct {
	w     io.Writer
	color bool
}

func newPrinter(w io.Writer) *printer {
	p := &printer{w: w}
	if f, ok := w.(*os.File); ok && os.Getenv("NO_COLOR") == "" {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			p.color = true
		}
	}
	return p
}

const (
	bold   = "\033[1m"
	dim    = "\033[2m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	reset  = "\033[0m"
)

func (p *printer) paint(color, s string) string {
	if !p.color || s == "" {
		return s
	}
	return color + s + reset
}

func (p *printer) header(a *audit) {
	_, _ = fmt.Fprintf(p.w, "%s %s\n", p.paint(bold, a.mod.path),
		p.paint(dim, "· "+strings.Join(a.packages, " ")+" · "+a.mod.root))
}

func (p *printer) name(width int, name string) {
	_, _ = fmt.Fprintf(p.w, "  %-*s ", width, name)
}

func (p *printer) row(r *result) {
	color := map[string]string{
		statusOK: green, statusFound: red, statusError: red, statusSkipped: yellow,
	}[r.status]
	count, took := "-", "-"
	if r.ran {
		took = duration(r.took)
	}
	if r.status == statusOK || len(r.findings) > 0 {
		count = fmt.Sprint(len(r.findings))
	}
	note := r.note
	if r.status != statusError {
		note = p.paint(dim, note)
	}
	line := fmt.Sprintf("%s %5s %6s", p.paint(color, fmt.Sprintf("%-7s", r.status)), count, took)
	if note != "" {
		line += "  " + note
	}
	_, _ = fmt.Fprintln(p.w, line)
}

func (p *printer) interrupted() {
	_, _ = fmt.Fprintln(p.w, p.paint(red, statusInterrupted))
}

func (p *printer) dim(format string, args ...any) {
	_, _ = fmt.Fprint(p.w, p.paint(dim, fmt.Sprintf(format, args...)))
}

// duration prints how long a check took to the tenth of a second, and to
// the second from ten seconds on.
func duration(d time.Duration) string {
	if d < 10*time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}
