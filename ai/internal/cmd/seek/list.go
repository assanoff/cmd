// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package seek

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/index"
)

type listCommand struct {
	seek *Command
}

func (c *listCommand) Execute(args []string) error {
	if len(args) > 0 {
		return cli.Usagef("ls takes no arguments")
	}
	idx, err := c.seek.open()
	if err != nil {
		return err
	}
	defer func() { _ = idx.Close() }()

	ms, err := idx.List(c.seek.ctx)
	if err != nil {
		return err
	}
	if c.seek.Format == "json" {
		return listJSON(os.Stdout, ms)
	}
	return listText(os.Stdout, ms)
}

// listText prints a file per line, and marks the ones gone from disk: the
// index still finds them, but nothing can play them.
func listText(w io.Writer, ms []index.Media) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, m := range ms {
		path := m.Path
		if missing(m.Path) {
			path += "  (missing)"
		}
		_, err := fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", clock(m.Duration), m.Chunks, m.Indexed.Format("2006-01-02"), path)
		if err != nil {
			return err
		}
	}
	return tw.Flush()
}

type jsonMedia struct {
	Path       string `json:"path"`
	Title      string `json:"title"`
	DurationMs int64  `json:"duration_ms"`
	Passages   int    `json:"passages"`
	Lang       string `json:"lang"`
	Indexed    string `json:"indexed"`
	Missing    bool   `json:"missing"`
}

func listJSON(w io.Writer, ms []index.Media) error {
	out := make([]jsonMedia, 0, len(ms))
	for _, m := range ms {
		out = append(out, jsonMedia{
			Path:       m.Path,
			Title:      m.Title,
			DurationMs: m.Duration.Milliseconds(),
			Passages:   m.Chunks,
			Lang:       m.Lang,
			Indexed:    m.Indexed.UTC().Format("2006-01-02T15:04:05Z"),
			Missing:    missing(m.Path),
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func missing(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

type removeCommand struct {
	seek *Command

	Args struct {
		Files []string `positional-arg-name:"file" required:"1" description:"indexed files, as ls lists them or relative to here"`
	} `positional-args:"yes"`
}

// Execute drops each named file. The file itself is not touched, and need not
// exist any more: dropping what was moved or deleted is what this is for.
func (c *removeCommand) Execute([]string) error {
	p := cli.NewPrinter(Name, c.seek.Quiet)
	idx, err := c.seek.open()
	if err != nil {
		return err
	}
	defer func() { _ = idx.Close() }()

	var failed bool
	for _, f := range c.Args.Files {
		path, err := filepath.Abs(f)
		if err != nil {
			return err
		}
		gone, err := idx.Remove(c.seek.ctx, path)
		if err != nil {
			return err
		}
		if !gone {
			p.Printf("%s: not indexed", f)
			failed = true
		}
	}
	if failed {
		return errors.New("one or more files were not in the index")
	}
	return nil
}
