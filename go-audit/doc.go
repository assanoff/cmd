// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
go-audit keeps the reports of Go code checks in one place in the home
directory: its own and those of the go-style Claude Code plugin.

Usage:

	go-audit reports [-n N] [filter]
	go-audit version

Running the checks themselves is not in this version yet.

# Listing reports

go-audit reports lists the reports in the store, newest first, ten of them
unless -n says otherwise; -n 0 lists all. A filter keeps the reports of the
modules whose path contains it, and "." keeps those of the module holding the
current directory:

	go-audit reports
	go-audit reports -n 0 payments
	go-audit reports .

Each line gives the time of the report, the module, the kind and label of the
report, the number of findings and a one-line summary. A report whose JSON
twin is missing, unreadable or of an older format is listed all the same, with
the reason in place of the summary.

# The store

The store is $GO_STYLE_HOME, or .go-style in the home directory. It holds
plain files and no database:

	reports/<module>/<time>-style-<label>.md    a go-style report, label = guides
	reports/<module>/<time>-style-<label>.json
	reports/<module>/<time>-audit.md            a go-audit report
	reports/<module>/<time>-audit.json

<module> is the module path from go.mod, escaped as in the module cache:
each capital letter becomes "!" and the lower-case letter, so that Foo and foo
stay apart on a case-insensitive file system. Any path a main module may have
is taken, "myapp" as well as "github.com/BurntSushi/toml"; it is checked as
golang.org/x/mod/module.CheckImportPath checks an import path. <time> is
YYYYMMDD-HHMMSS in local time, first in the name, so that sorting by name is
sorting by time. A second report of a module in the same second gets -2 at
the end of its name, a third -3.

The .md file is the report for people. Its .json twin is for programs; the
list reads only these fields of it, which every kind of report has:

	format   1, the version of these fields
	kind     "style" or "audit"
	module   the module path
	root     the directory the module was in when it was checked
	time     when the report was written, RFC 3339 with a UTC offset
	label    the guides of a style report, the packages of an audit
	total    the number of findings (of a style report: the verified ones)
	summary  one line for the list, such as "must 58 · should 336 · consider 65"

Everything else in the JSON belongs to its kind, so a new kind of report needs
no change to the list.
*/
package main
