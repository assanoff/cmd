// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package cli

import (
	"io"
	"os"
	"path/filepath"
)

// DescribeInput names an input for a progress line.
func DescribeInput(in string) string {
	if in == "-" {
		return "standard input"
	}
	return in
}

// ReadText reads a whole input, where "-" is standard input.
func ReadText(in string) (string, error) {
	if in == "-" {
		b, err := io.ReadAll(os.Stdin)
		return string(b), err
	}
	b, err := os.ReadFile(in)
	return string(b), err
}

// OpenInput returns a reader over an input, where "-" is standard input. It is
// for the commands that stream rather than read the whole thing: a two-hour
// recording does not belong in memory.
func OpenInput(in string) (io.Reader, func(), error) {
	if in == "-" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(in)
	if err != nil {
		return nil, nil, err
	}
	// The close error on a file opened only for reading says nothing a caller
	// could act on, and the result was already produced.
	return f, func() { _ = f.Close() }, nil
}

// OpenOutput returns the destination writer and a closer that reports a write
// error. An empty out means standard output, which is never closed.
func OpenOutput(out string) (io.Writer, func() error, error) {
	if out == "" {
		return os.Stdout, func() error { return nil }, nil
	}
	if dir := filepath.Dir(out); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, err
		}
	}
	f, err := os.Create(out)
	if err != nil {
		return nil, nil, err
	}
	return f, f.Close, nil
}

// skipExisting honours the default of leaving finished work alone, so
// re-running a batch after an interruption only does what is left.
func skipExisting(out string, force bool, p *Printer, in string) (bool, error) {
	if out == "" || force {
		return false, nil
	}
	switch _, err := os.Stat(out); {
	case err == nil:
		p.Printf("skip %s: %s exists (use --force)", in, out)
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, err
	}
}
