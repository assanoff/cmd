// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
)

// defaultListed is how many reports go-audit reports lists without -n.
const defaultListed = 10

func reportsCommand() *command {
	fs := flag.NewFlagSet("reports", flag.ContinueOnError)
	n := fs.Int("n", defaultListed, "list the newest `N` reports; 0 lists all")
	return &command{
		name:  "reports",
		usage: "reports [-n N] [filter]",
		short: "List the reports in the store, newest first",
		flags: fs,
		run: func(ctx context.Context, args []string) error {
			return listReports(ctx, *n, args)
		},
	}
}

// listReports lists the newest n reports, all of them if n is 0, of the
// modules that match the filter in args.
func listReports(ctx context.Context, n int, args []string) error {
	if n < 0 {
		return usagef("reports: -n is a count of reports, not %d", n)
	}
	if len(args) > 1 {
		return usagef("reports takes one filter at most")
	}
	// The filter first: "." outside a module is a mistake whatever the store
	// holds.
	var match func(string) bool
	if len(args) == 1 {
		var err error
		if match, err = moduleFilter(ctx, args[0]); err != nil {
			return err
		}
	}
	store, err := storeDir()
	if err != nil {
		return err
	}
	reports, err := loadReports(store)
	if err != nil {
		return err
	}
	dir := filepath.Join(store, "reports")
	if len(reports) == 0 {
		_, err := fmt.Fprintf(stdout, "no reports yet in %s\n", dir)
		return err
	}

	if match != nil {
		var kept []*report
		for _, r := range reports {
			if match(r.Module) {
				kept = append(kept, r)
			}
		}
		if len(kept) == 0 {
			_, err := fmt.Fprintf(stdout, "no reports for %q in %s\n", args[0], dir)
			return err
		}
		reports = kept
	}

	total := len(reports)
	if n > 0 && n < total {
		reports = reports[:n]
	}
	if err := printReports(stdout, reports); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%d of %d · %s\n", len(reports), total, dir)
	return err
}

// moduleFilter returns what keeps the reports of a module: "." stands for
// the module of the current directory, anything else is part of a module
// path.
func moduleFilter(ctx context.Context, filter string) (func(string) bool, error) {
	if filter != "." {
		return func(path string) bool {
			return strings.Contains(path, filter)
		}, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	m, err := findModule(ctx, wd)
	if err != nil {
		return nil, err
	}
	return func(path string) bool { return path == m.path }, nil
}

// printReports writes one line per report: time, module, kind, label,
// findings and summary. A report that could not be read gets its file name in
// place of the label and the reason in place of the summary.
func printReports(w io.Writer, reports []*report) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range reports {
		when := "-"
		if !r.Time.IsZero() {
			when = r.Time.Local().Format("2006-01-02 15:04")
		}
		kind, label, total, summary := r.Kind, r.Label, strconv.Itoa(r.Total), r.Summary
		if r.err != nil {
			kind, label, total, summary = "?", r.name(), "-", r.err.Error()
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			when, orDash(r.Module), kind, label, total, summary)
	}
	return tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
