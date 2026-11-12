// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// output is the -n, -f and -o of a report.
type output struct {
	n      int
	format string
	file   string
}

func (o *output) flags(f *flag.FlagSet) {
	f.IntVar(&o.n, "n", 10, "list the first `count`; 0 lists all")
	f.StringVar(&o.format, "f", "",
		"the `format`: text, csv or json; by default that the extension of -o names, else text")
	f.StringVar(&o.file, "o", "", "write to this `file` instead of standard output")
}

// check settles the format before any work is done for it.
func (o *output) check() error {
	if o.format == "" {
		switch strings.ToLower(filepath.Ext(o.file)) {
		case ".csv":
			o.format = "csv"
		case ".json":
			o.format = "json"
		default:
			o.format = "text"
		}
	}
	switch o.format {
	case "text", "csv", "json":
	default:
		return usagef("-f %s: text, csv or json", o.format)
	}
	if o.n < 0 {
		return usagef("-n %d: 0 or more", o.n)
	}
	return nil
}

// writer is where a report goes, in the format it is to be written in.
type writer struct {
	io.Writer
	format string
	total  int // the items there are; a report shows the first n
}

// write writes the first -n of total items with fn, to -o or standard output.
func (o *output) write(total int, fn func(w *writer, n int) error) (err error) {
	if err := o.check(); err != nil {
		return err
	}
	n := total
	if o.n > 0 && o.n < n {
		n = o.n
	}
	out := stdout
	if o.file != "" {
		f, err := os.Create(o.file)
		if err != nil {
			return err
		}
		defer func() {
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}()
		out = f
	}
	bw := bufio.NewWriter(out)
	if err := fn(&writer{Writer: bw, format: o.format, total: total}, n); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if o.file != "" {
		fmt.Fprintf(os.Stderr, "wrote %d of %d to %s\n", n, total, o.file)
	}
	return nil
}

// more ends a text report that shows less than there is.
func (w *writer) more(n int) {
	switch {
	case w.total == 0:
		_, _ = fmt.Fprintln(w, "no duplicates")
	case n < w.total:
		_, _ = fmt.Fprintf(w, "%d of %d shown; -n 0 lists them all\n", n, w.total)
	}
}

func (w *writer) json(v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func (w *writer) csv(rows [][]string) error {
	cw := csv.NewWriter(w)
	if err := cw.WriteAll(rows); err != nil {
		return err
	}
	return cw.Error()
}

func writeFileGroups(w *writer, groups []fileGroup) error {
	switch w.format {
	case "json":
		if groups == nil {
			groups = []fileGroup{}
		}
		return w.json(groups)
	case "csv":
		rows := [][]string{{"group", "key", "count", "wasted", "path", "size", "modified"}}
		for i, g := range groups {
			for _, f := range g.Files {
				rows = append(rows, []string{
					strconv.Itoa(i + 1), g.Key, strconv.Itoa(g.Count), itoa(g.Wasted),
					f.Path, itoa(f.Size), f.Modified.Format(time.RFC3339),
				})
			}
		}
		return w.csv(rows)
	}
	for i, g := range groups {
		key := g.Key
		if len(key) == 64 {
			key = "sha256:" + key[:12]
		}
		_, _ = fmt.Fprintf(w, "%d. %s, %s to reclaim  %s\n", i+1, plural(g.Count, "file"), formatSize(g.Wasted), key)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
		for _, f := range g.Files {
			_, _ = fmt.Fprintf(tw, "\t%s\t  %s  %s\n",
				formatSize(f.Size), f.Modified.Format("2006-01-02 15:04"), f.Path)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	w.more(len(groups))
	return nil
}

func writeDirGroups(w *writer, groups []dirGroup) error {
	switch w.format {
	case "json":
		type group struct {
			Files  int64    `json:"files"`
			Bytes  int64    `json:"bytes"`
			Wasted int64    `json:"wasted"`
			Dirs   []string `json:"dirs"`
		}
		out := []group{}
		for _, g := range groups {
			out = append(out, group{g.files, g.bytes, g.waste(), g.dirs})
		}
		return w.json(out)
	case "csv":
		rows := [][]string{{"group", "files", "bytes", "wasted", "path"}}
		for i, g := range groups {
			for _, d := range g.dirs {
				rows = append(rows, []string{
					strconv.Itoa(i + 1), itoa(g.files), itoa(g.bytes), itoa(g.waste()), d,
				})
			}
		}
		return w.csv(rows)
	}
	for i, g := range groups {
		_, _ = fmt.Fprintf(w, "%d. %d copies of %s, %s; %s to reclaim\n",
			i+1, len(g.dirs), plural(g.files, "file"), formatSize(g.bytes), formatSize(g.waste()))
		for _, d := range g.dirs {
			_, _ = fmt.Fprintf(w, "     %s\n", d)
		}
	}
	w.more(len(groups))
	return nil
}

func writePairs(w *writer, pairs []dirPair) error {
	switch w.format {
	case "json":
		type dir struct {
			Path  string `json:"path"`
			Files int64  `json:"files"`
			Bytes int64  `json:"bytes"`
		}
		type pair struct {
			Name        string  `json:"name"`
			Similarity  float64 `json:"similarity"`
			CommonFiles int     `json:"common_files"`
			CommonBytes int64   `json:"common_bytes"`
			A           dir     `json:"a"`
			B           dir     `json:"b"`
		}
		out := []pair{}
		for _, p := range pairs {
			out = append(out, pair{
				Name:        p.name,
				Similarity:  p.score,
				CommonFiles: p.common,
				CommonBytes: p.commonBytes,
				A:           dir{p.a.path, p.a.files, p.a.bytes},
				B:           dir{p.b.path, p.b.files, p.b.bytes},
			})
		}
		return w.json(out)
	case "csv":
		rows := [][]string{{
			"pair", "name", "similarity", "common_files", "common_bytes",
			"path_a", "files_a", "bytes_a", "path_b", "files_b", "bytes_b",
		}}
		for i, p := range pairs {
			rows = append(rows, []string{
				strconv.Itoa(i + 1), p.name, strconv.FormatFloat(p.score, 'f', 3, 64),
				strconv.Itoa(p.common), itoa(p.commonBytes),
				p.a.path, itoa(p.a.files), itoa(p.a.bytes),
				p.b.path, itoa(p.b.files), itoa(p.b.bytes),
			})
		}
		return w.csv(rows)
	}
	for i, p := range pairs {
		_, _ = fmt.Fprintf(w, "%d. %s: %.0f%% alike, %s and %s in common\n",
			i+1, p.name, 100*p.score, plural(p.common, "file"), formatSize(p.commonBytes))
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, d := range []dirSet{p.a, p.b} {
			_, _ = fmt.Fprintf(tw, "     %s\t%s, %s\n", d.path, plural(d.files, "file"), formatSize(d.bytes))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	w.more(len(pairs))
	return nil
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// plural writes a count of things.
func plural[N int | int64](n N, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}

// parseSize reads a size: a number of bytes with an optional K, M, G or T,
// powers of 1024, and an optional B or iB after it: 100M, 1.5GB, 4KiB.
func parseSize(s string) (int64, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	t = strings.TrimSuffix(strings.TrimSuffix(t, "B"), "I")
	mult := 1.0
	if t != "" {
		if i := strings.IndexByte("KMGTP", t[len(t)-1]); i >= 0 {
			mult = float64(int64(1) << (10 * (i + 1)))
			t = t[:len(t)-1]
		}
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
	if err != nil || f < 0 {
		return 0, errors.New("not a size: write it as 0, 4K, 100M or 1.5G")
	}
	return int64(f * mult), nil
}

// formatSize writes a size the way Windows Explorer does, in powers of 1024.
func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}
