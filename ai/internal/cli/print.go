// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Printer writes progress to standard error, never to standard output: the
// result on standard output has to stay clean for the next command in the
// pipeline.
//
// Every subcommand builds one from its own name and its -q flag, so a message
// says which subcommand produced it even when several are chained together.
type Printer struct {
	prefix string
	quiet  bool
}

// NewPrinter returns a printer labelled with name, such as "ai sum".
func NewPrinter(name string, quiet bool) *Printer {
	return &Printer{prefix: name + ": ", quiet: quiet}
}

// Quiet reports whether progress is suppressed. Callers that would do real
// work to produce a message should ask first.
func (p *Printer) Quiet() bool { return p.quiet }

// Printf writes one labelled line.
//
// The write errors are dropped throughout this type on purpose: this is
// progress on standard error, and a caller that cannot report progress has
// nothing useful to do about it — least of all abandon the answer it was in
// the middle of producing.
func (p *Printer) Printf(format string, args ...any) {
	if p.quiet {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, p.prefix+format+"\n", args...)
}

// Open writes a labelled line without ending it, so raw text can follow. It is
// what makes streamed reasoning read as one paragraph rather than as a column
// of prefixes.
func (p *Printer) Open(format string, args ...any) {
	if p.quiet {
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, p.prefix+format, args...)
}

// Raw writes text with no label and no newline.
func (p *Printer) Raw(s string) {
	if p.quiet {
		return
	}
	_, _ = fmt.Fprint(os.Stderr, s)
}

// Close ends a line opened by Open.
func (p *Printer) Close() {
	if p.quiet {
		return
	}
	_, _ = fmt.Fprintln(os.Stderr)
}

// Logger adapts the printer to what the Kronk and Bucky SDKs want for their
// own progress. The SDKs ship FmtLogger, but it writes to standard output,
// which would corrupt every pipeline.
func (p *Printer) Logger() func(ctx context.Context, msg string, args ...any) {
	if p.quiet {
		return func(context.Context, string, ...any) {}
	}
	return func(_ context.Context, msg string, args ...any) {
		if len(args) == 0 {
			p.Printf("%s", msg)
			return
		}
		var b strings.Builder
		b.WriteString(msg)
		for i := 0; i+1 < len(args); i += 2 {
			fmt.Fprintf(&b, " %v=%v", args[i], args[i+1])
		}
		if len(args)%2 == 1 {
			fmt.Fprintf(&b, " %v", args[len(args)-1])
		}
		p.Printf("%s", b.String())
	}
}

// Discard drops SDK chatter. Platform detection reports which runtime it
// picked every time it runs, which is worth seeing during an install and pure
// noise in the middle of a status table.
func Discard(context.Context, string, ...any) {}
