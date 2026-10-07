// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/assanoff/cmd/ai/internal/walk"
)

// Jobs resolves the batch request and turns walk's errors into this program's.
//
// walk deliberately has no opinion about exit codes, and every subcommand
// holds the same opinion: a terminal with nothing piped into it and a batch
// asked for with -o are both caller mistakes. Stating it once here is what
// keeps the four subcommands from drifting apart on the exit status of the
// same mistake.
func Jobs(r walk.Request) ([]walk.Job, error) {
	jobs, err := walk.Jobs(r)

	var be *walk.BatchError
	switch {
	case err == nil:
		return jobs, nil
	case errors.Is(err, walk.ErrTerminalInput), errors.As(err, &be):
		return nil, Usagef("%v", err)
	default:
		return nil, err
	}
}

// Pending drops the jobs whose output already exists, which is what makes
// re-running a batch after an interruption do only what is left.
//
// It runs before the model is loaded, not inside the run loop: a resumed batch
// with nothing left to do should not pay for several gigabytes of model to
// discover that.
func Pending(jobs []walk.Job, force bool, p *Printer) ([]walk.Job, error) {
	if force {
		return jobs, nil
	}

	pending := make([]walk.Job, 0, len(jobs))
	for _, j := range jobs {
		skip, err := skipExisting(j.Out, force, p, j.In)
		if err != nil {
			return nil, err
		}
		if !skip {
			pending = append(pending, j)
		}
	}
	return pending, nil
}

// Each runs do over every job and applies the batch failure policy: one bad
// input among many is reported and the run carries on, an interrupted run
// stops at once, and the whole batch fails at the end if anything did.
//
// The name of each file written goes to standard output as it is finished, so
// the batch composes with xargs.
func Each(ctx context.Context, p *Printer, jobs []walk.Job, do func(walk.Job) error) error {
	var failed bool
	for _, j := range jobs {
		if err := do(j); err != nil {
			if ctx.Err() != nil {
				return err
			}
			p.Printf("%s: %v", j.In, err)
			failed = true
			continue
		}
		if j.Out != "" {
			fmt.Println(j.Out)
		}
	}
	if failed {
		return errors.New("one or more inputs failed")
	}
	return nil
}

// PrintPlan writes the input-to-output table of a --dry-run. It is the payload
// of -n, so it goes to standard output; each subcommand prints its own
// settings above it.
func PrintPlan(jobs []walk.Job) {
	for _, j := range jobs {
		out := j.Out
		if out == "" {
			out = "(standard output)"
		}
		fmt.Printf("%s -> %s\n", DescribeInput(j.In), out)
	}
	fmt.Printf("%d input(s)\n", len(jobs))
}

// FormatExt is the file extension that goes with a --format value. Every
// format names its own extension; only "text" is spelled differently from the
// flag that asks for it.
func FormatExt(format string) string {
	if format == "text" {
		return ".txt"
	}
	return "." + format
}
