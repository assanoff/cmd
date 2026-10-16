// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
go-audit runs the checks of its config, go vet, nilaway, gopls and others,
over a Go module and keeps the reports in one place in the home directory:
its own and those of the go-style Claude Code plugin.

Usage:

	go-audit [-c N] [-only a,b] [-skip a,b] [packages]
	go-audit reports [-n N] [filter]
	go-audit config [-init]
	go-audit version

# Running the checks

go-audit runs the checks over the packages, ./... of the current directory
by default, one after another, and prints a line for each: its status, the
number of findings, the time it took and a note. Then it writes the report
into the store and prints where the report and the output of the checks are:

	go-audit
	go-audit ./internal/...
	go-audit -only vet,gopls
	go-audit -skip gostyle -c 4

A check is ok when it found nothing, found when it found something, skipped
when its program, a file it needs or a Go new enough is missing, and error
when it could not do its job: the code does not build, the tool crashed or
its output does not read. A skipped check says what to install. The exit
status is 0 when nothing was found, 1 when something was, and 2 when a check
failed or go-audit was called wrongly.

-only runs the checks it names, those that are off as well; -skip leaves out
the checks it names. Ctrl-C stops the checks that run; no report is written
then.

-c N runs up to N checks at a time; the lines are printed in the order of the
config all the same. It gains little: each check loads and type-checks the
packages on its own, on all the cores, so the checks share the processor
while the memory they need adds up. With a cold build cache it loses, as the
checks build the same dependencies at once, while with -c 1 the first check
fills the cache for the rest. The wall time and the peak memory of all the
processes, for the 14 checks of the built-in config over
github.com/assanoff/cmd/ai on 8 cores and Go 1.27.2:

	cache   -c 1          -c 4           -c 10
	warm    39s   2.5 GB  35s    3.0 GB  33s  3.3 GB
	cold    2m7s  2.8 GB  3m59s  3.4 GB

The checks run in the module root, with packages relative to the current
directory made relative to it. They do not change the module: go-audit writes
only into the store.

# The config

The checks are described in YAML. The built-in config is go-audit.yml in this
directory; "go-audit config -init" puts a copy of it into the store, beside
golangci.yml, the standard set of golangci-lint. The go-audit.yml in the
store then replaces the built-in one as a whole, so that checks can be
turned off, changed and added. "go-audit config" shows which
file is in use, its checks, and the checks of the built-in config that one's
own lacks or describes otherwise, so that what a newer go-audit brings is not
lost silently. A check:

	checks:
	  - name: nilaway
	    cmd: [nilaway, -json, -pretty-print=false, "-include-pkgs={module}", "{packages}"]
	    format: vet-json
	    install: go.uber.org/nilaway/cmd/nilaway@latest

has a name, for -only, -skip and the files of its output; the command, run in
the module root; the format of its output; and optionally what to go install
when the command is missing, the oldest Go it works with ("go: 1.26"), a note
for the report and "off: true", which leaves it out unless -only names it.
The command may use these substitutions:

	{packages}          the packages go-audit was given, ./... by default
	{files}             the Go files of those packages, tests included
	{module}            the module path
	{gotooldir}         go env GOTOOLDIR in the module root: the tools of the
	                    Go that GOTOOLCHAIN picks there
	{bin:NAME}          the full path of the program NAME
	{file:NAME}         the file NAME in the module root
	{golangci-config}   --config=<store>/golangci.yml, or --no-config without it
	{project-golangci}  --config= and the golangci-lint config of the module

{packages} and {files} take an argument of their own each. A substitution
with no value, such as a program or a file that is not there, skips the check
with a note. A mistake in the config stops go-audit with the file and the line
of the check.

The formats tell go-audit how to read what a check prints. Three suit many
tools, so that a new check of such a tool needs no code:

	vet-json  the JSON of go vet -json and of analyzers run with -json, such as
	          nilaway: findings with the analyzer as the rule
	lines     a finding per line, file:line[:col]: message as gopls check prints,
	          or a file alone as gofmt -l does
	diff      one finding when there is any output, as go mod tidy -diff prints

The others are those of one tool each:

	staticcheck  staticcheck -f json; a package that does not build is an error
	golangci     golangci-lint run --output.json.path=stdout, with the line of
	             code golangci-lint gives; the typecheck linter is an error
	gosec        gosec -fmt=json; a package gosec could not type-check is an
	             error, as its issues are then only what the syntax shows; a
	             gosec built with an older Go cannot read the packages at all
	deadcode     deadcode -json: unreachable functions; with no main package
	             and no test there is nothing to start from, and the check is
	             skipped
	govulncheck  govulncheck -format json: a finding for each vulnerability the
	             code calls, at the line that calls it; those in code it only
	             imports or requires are counted in the note
	go-licenses  go-licenses check: a finding with no position for each library
	             whose license is unknown or not allowed, the license as the
	             rule; the warnings around them are left out

A check that exits non-zero and finds nothing failed, whatever its format;
vet-json also takes an error of an analyzer or a non-zero exit of go vet for a
failure, and lines anything on stderr with a non-zero exit.

The built-in config runs go vet and the analyzers of go fix, staticcheck,
golangci-lint twice, gosec, nilaway, deadcode, govulncheck, go-licenses,
gostyle, gopls check, go mod tidy -diff and gofmt -l, in this order; shadow
and capslock are there, off. golangci-lint runs once with the standard set,
the same for every module, which golangci.yml in the store can change, and
once more as golangci-project with the module's own config, if it has one.
govulncheck needs the network: offline, -skip govulncheck.

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

	go-audit.yml                                one's own config, if any
	golangci.yml                                the standard set of golangci-lint, if any
	reports/<module>/<time>-style-<label>.md    a go-style report, label = guides
	reports/<module>/<time>-style-<label>.json
	reports/<module>/<time>-audit.md            a go-audit report
	reports/<module>/<time>-audit.json
	runs/<module>/<time>-audit/<check>.out      the stdout of a check
	runs/<module>/<time>-audit/<check>.log      its stderr

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
no change to the list. That of an audit has the packages, the Go version, the
config, -c, the wall time and the directory of the output; checks, each with
its name, status, count, seconds, note and output files; and findings, each
with its check, file, line, col, rule, message and the line of code at the
time of the run.
*/
package main
