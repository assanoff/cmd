// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package cli holds the plumbing every subcommand repeats: exit codes, the
// distinction between a caller's mistake and a runtime failure, and the
// input/output helpers that make a command behave like a text filter.
//
// It deliberately knows nothing about models. Everything here would be as true
// of a command that never loaded one.
package cli

import (
	"errors"
	"fmt"
)

// Exit codes. Scripts need to tell "you called me wrong" apart from "the model
// stack is missing", so these are part of the interface and must stay stable.
const (
	ExitError    = 1
	ExitUsage    = 2
	ExitNotSetup = 3
)

// ErrNotSetup marks a failure the user can fix by installing something. It
// surfaces as exit code 3, which is what lets a wrapper script tell a missing
// model apart from a model that answered badly.
var ErrNotSetup = errors.New("model stack not installed")

// UsageError is a caller mistake rather than a runtime failure. go-flags
// reports its own parse errors; this covers the ones only the command can
// catch, such as two flags that contradict each other.
type UsageError struct{ err error }

func (e UsageError) Error() string { return e.err.Error() }
func (e UsageError) Unwrap() error { return e.err }

// Usagef reports a caller mistake.
func Usagef(format string, args ...any) error {
	return UsageError{fmt.Errorf(format, args...)}
}

// NotSetupf reports a failure the user can install their way out of. The
// format may itself carry %w verbs, which is how the SDK's own error survives
// into the message.
func NotSetupf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrNotSetup}, args...)...)
}

// ExitCode maps an error to the status the process exits with.
func ExitCode(err error) int {
	var ue UsageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ue):
		return ExitUsage
	case errors.Is(err, ErrNotSetup):
		return ExitNotSetup
	default:
		return ExitError
	}
}
