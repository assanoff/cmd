// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/google/subcommands"
)

type filesCmd struct {
	db   string
	by   string
	sort string
	ext  string
	min  string
	out  output
}

func (*filesCmd) Name() string     { return "files" }
func (*filesCmd) Synopsis() string { return "list duplicate files" }

func (*filesCmd) Usage() string {
	return `files [-by hash|name|namesize] [-sort waste|size|count] [-ext list] [-min size]
      [-n count] [-f text|csv|json] [-o file] [path...]

Lists the groups of files that are copies of one another, the most space to
reclaim first: what deleting all but one of each group would free.

-by hash (the default) groups files of the same content, read and compared;
-by name groups files of the same name, -by namesize those of the same name
and size. Names are compared without case, and neither name criterion reads
a file: they find what may be copies, hash finds what are.

-ext and -min narrow the report as they narrow a scan, so a database of a
whole scan answers "the videos over a gigabyte" too. Given paths, only the
groups with a file in one of them: what in E:\old is also somewhere else.

`
}

func (c *filesCmd) SetFlags(f *flag.FlagSet) {
	dbFlag(f, &c.db)
	f.StringVar(&c.by, "by", "hash", "what makes files copies: `hash`, name or namesize")
	f.StringVar(&c.sort, "sort", "waste", "the `order`: waste, size or count")
	f.StringVar(&c.ext, "ext", "", "only these `extensions`, comma-separated")
	f.StringVar(&c.min, "min", "0", "only files of at least this `size`")
	c.out.flags(f)
}

func (c *filesCmd) Execute(ctx context.Context, f *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	return exit(c.run(ctx, f.Args()))
}

// criterion is what the files of a group have in common: the columns of
// files they share.
type criterion struct {
	cols  []string
	where string
}

var criteria = map[string]criterion{
	"hash":     {cols: []string{"hash"}, where: "hash is not null"},
	"name":     {cols: []string{"lname"}, where: "1"},
	"namesize": {cols: []string{"lname", "size"}, where: "1"},
}

// The orders of -sort. The waste of a group is what deleting all its files
// but the biggest would free, which for copies is the size times the
// copies less one.
var orders = map[string]string{
	"waste": "sum(size) - max(size) desc",
	"size":  "max(size) desc",
	"count": "count(*) desc, sum(size) desc",
}

type fileGroup struct {
	Key    string     `json:"key"`
	Count  int        `json:"count"`
	Size   int64      `json:"size"` // of the biggest file
	Wasted int64      `json:"wasted"`
	Files  []fileInfo `json:"files"`
}

type fileInfo struct {
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

func (c *filesCmd) run(ctx context.Context, args []string) error {
	if err := c.out.check(); err != nil {
		return err
	}
	flt, err := parseFilter(c.ext, c.min)
	if err != nil {
		return err
	}
	scope, err := canonicalAll(args)
	if err != nil {
		return err
	}
	q, err := newFileQuery(c.by, c.sort, flt, scope)
	if err != nil {
		return err
	}
	db, err := openDB(c.db, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	total, err := q.count(ctx, db)
	if err != nil {
		return err
	}
	return c.out.write(total, func(w *writer, n int) error {
		groups, err := q.groups(ctx, db, 0, n)
		if err != nil {
			return err
		}
		return writeFileGroups(w, groups)
	})
}

// fileQuery asks the files table for groups of copies: by a criterion, in
// an order, among the files a filter lets through, and only the groups with
// a file in the scope when it is not empty. The files command and the web
// page of files both ask it.
type fileQuery struct {
	crit    criterion
	order   string
	where   string // the condition on a file
	wargs   []any
	grouped string // the groups, unordered
	gargs   []any
}

func newFileQuery(by, sort string, flt filter, scope []string) (*fileQuery, error) {
	crit, ok := criteria[by]
	if !ok {
		return nil, usagef("-by %s: hash, name or namesize", by)
	}
	order, ok := orders[sort]
	if !ok {
		return nil, usagef("-sort %s: waste, size or count", sort)
	}
	q := &fileQuery{crit: crit, order: order}
	cond, args := flt.where()
	q.where = crit.where + " and " + cond
	q.wargs = args

	cols := strings.Join(crit.cols, ", ")
	having := "count(*) > 1"
	var hargs []any
	if len(scope) > 0 {
		var ins []string
		for _, d := range scope {
			lo, hi := under(d)
			ins = append(ins, "(path >= ? and path < ?)")
			hargs = append(hargs, lo, hi)
		}
		having += " and sum(" + strings.Join(ins, " or ") + ") > 0"
	}
	q.grouped = fmt.Sprintf(`select %s, count(*), sum(size), max(size) from files
		where %s group by %s having %s`, cols, q.where, cols, having)
	q.gargs = append(append([]any{}, q.wargs...), hargs...)
	return q, nil
}

// count returns the number of groups.
func (q *fileQuery) count(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `select count(*) from (`+q.grouped+`)`, q.gargs...).Scan(&n)
	return n, err
}

// groups returns at most limit groups from offset in the order of the query,
// each with its files.
func (q *fileQuery) groups(ctx context.Context, db *sql.DB, offset, limit int) ([]fileGroup, error) {
	rows, err := db.QueryContext(ctx, q.grouped+" order by "+q.order+" limit ? offset ?",
		append(append([]any{}, q.gargs...), limit, offset)...)
	if err != nil {
		return nil, err
	}
	var groups []fileGroup
	var keys [][]any
	for rows.Next() {
		key := make([]any, len(q.crit.cols))
		dest := make([]any, 0, len(key)+3)
		for i := range key {
			dest = append(dest, &key[i])
		}
		var g fileGroup
		var sum int64
		dest = append(dest, &g.Count, &sum, &g.Size)
		if err := rows.Scan(dest...); err != nil {
			_ = rows.Close()
			return nil, err
		}
		g.Wasted = sum - g.Size
		g.Key = keyString(key)
		groups = append(groups, g)
		keys = append(keys, key)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var match []string
	for _, col := range q.crit.cols {
		match = append(match, col+" = ?")
	}
	fq := `select path, size, mtime from files where ` + q.where + " and " +
		strings.Join(match, " and ") + ` order by path`
	for i := range groups {
		files, err := listFiles(ctx, db, fq, append(append([]any{}, q.wargs...), keys[i]...)...)
		if err != nil {
			return nil, err
		}
		groups[i].Files = files
	}
	return groups, nil
}

// listFiles runs a query of path, size and mtime.
func listFiles(ctx context.Context, db *sql.DB, query string, args ...any) ([]fileInfo, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var files []fileInfo
	for rows.Next() {
		var f fileInfo
		var mtime int64
		if err := rows.Scan(&f.Path, &f.Size, &mtime); err != nil {
			return nil, err
		}
		f.Modified = time.Unix(0, mtime)
		files = append(files, f)
	}
	return files, rows.Err()
}

// keyString shows what the files of a group share: the hash in hex, or the
// name.
func keyString(key []any) string {
	switch k := key[0].(type) {
	case []byte:
		return hex.EncodeToString(k)
	case string:
		return k
	}
	return fmt.Sprint(key[0])
}
