// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"flag"
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/subcommands"
)

// content is what a directory holds, everything below it included: the
// number of files, their bytes and the sum of their hashes. Two directories
// with the same content hold the same files, whatever they are called and
// wherever they sit inside; deleting either loses nothing the other does
// not have.
//
// The hashes are added as 256-bit numbers, so a directory's content is the
// sum of its children's and the whole tree is summed in one pass from the
// leaves up, with no directory ever holding the list of its files. A sum is
// no defence against files made to collide, but nobody fills a disk with
// those to fool a duplicate finder.
type content struct {
	files int64
	bytes int64
	sum   [4]uint64
	// open is set when some file below has no hash. Such a file has no twin,
	// so neither has the directory.
	open bool
}

func (c *content) addFile(size int64, hash []byte) {
	c.files++
	c.bytes += size
	if hash == nil {
		c.open = true
		return
	}
	var carry uint64
	for i := 3; i >= 0; i-- {
		c.sum[i], carry = bits.Add64(c.sum[i], binary.BigEndian.Uint64(hash[i*8:]), carry)
	}
}

func (c *content) add(d *content) {
	c.files += d.files
	c.bytes += d.bytes
	c.open = c.open || d.open
	var carry uint64
	for i := 3; i >= 0; i-- {
		c.sum[i], carry = bits.Add64(c.sum[i], d.sum[i], carry)
	}
}

// sig is the content as one value to group by, nil for a directory that has
// no twin.
func (c *content) sig() []byte {
	if c.open || c.files == 0 {
		return nil
	}
	var buf [48]byte
	binary.BigEndian.PutUint64(buf[0:], uint64(c.files))
	binary.BigEndian.PutUint64(buf[8:], uint64(c.bytes))
	for i, v := range c.sum {
		binary.BigEndian.PutUint64(buf[16+8*i:], v)
	}
	s := sha256.Sum256(buf[:])
	return s[:]
}

// buildDirs rebuilds the dirs table from the files: every directory of every
// root that holds a recorded file, its own or below it. The tree above a
// root is not part of it — the root of a drive scanned only in D:\Photos
// would otherwise be a twin of D:\Photos.
func buildDirs(ctx context.Context, db *sql.DB) error {
	roots, err := listRoots(ctx, db)
	if err != nil {
		return err
	}
	inScope := func(dir string) bool {
		for _, r := range roots {
			if within(dir, r.path) {
				return true
			}
		}
		return false
	}

	dirs := map[string]*content{}
	rows, err := db.QueryContext(ctx, `select dir, size, hash from files`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var dir string
		var size int64
		var hash []byte
		if err := rows.Scan(&dir, &size, &hash); err != nil {
			_ = rows.Close()
			return err
		}
		c := dirs[dir]
		if c == nil {
			c = &content{}
			dirs[dir] = c
		}
		c.addFile(size, hash)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Every directory between a file and its root gets an entry, even one
	// holding only directories, then each adds itself into its parent, the
	// deepest first.
	paths := make([]string, 0, len(dirs))
	for d := range dirs {
		paths = append(paths, d)
	}
	for _, d := range paths {
		for {
			p := filepath.Dir(d)
			if p == d || !inScope(p) {
				break
			}
			if _, ok := dirs[p]; ok {
				break
			}
			dirs[p] = &content{}
			d = p
		}
	}
	paths = paths[:0]
	for d := range dirs {
		paths = append(paths, d)
	}
	slices.SortFunc(paths, func(a, b string) int {
		return cmp.Compare(len(b), len(a))
	})
	for _, d := range paths {
		if p := filepath.Dir(d); p != d && inScope(p) {
			dirs[p].add(dirs[d])
		}
	}

	if _, err := db.ExecContext(ctx, `delete from dirs`); err != nil {
		return err
	}
	b := &batch{db: db, query: `insert into dirs (path, lname, files, bytes, sig) values (?, ?, ?, ?, ?)`}
	for _, d := range paths {
		c := dirs[d]
		if err := b.exec(d, dirName(d), c.files, c.bytes, c.sig()); err != nil {
			return err
		}
	}
	return b.commit()
}

// dirName is the name of a directory in lower case. The root of a drive is
// called by the drive, so that D:\ and E:\ are not two directories named \.
func dirName(dir string) string {
	name := filepath.Base(dir)
	if name == string(os.PathSeparator) {
		if v := filepath.VolumeName(dir); v != "" {
			name = v
		}
	}
	return strings.ToLower(name)
}

type dirsCmd struct {
	db       string
	by       string
	min      string
	similar  float64
	maxGroup int
	out      output
}

func (*dirsCmd) Name() string     { return "dirs" }
func (*dirsCmd) Synopsis() string { return "list duplicate directories" }

func (*dirsCmd) Usage() string {
	return `dirs [-by content|name] [-n count] [-f text|csv|json] [-o file] [path...]

Lists the directories that are copies of one another, the most space first.

-by content (the default) groups directories that hold the same files, all of
them, however the files inside are named or arranged: any one directory of a
group can go without losing a file. A group whose directories all sit in the
directories of a bigger group is left out, since that group says it already.

-by name pairs directories of the same name, compares what they hold and
lists the pairs that have at least -similar of their files in common: the
"Photos" of an old backup that has nearly everything the current one has.

Given paths, only the groups and pairs with a directory in one of them.

`
}

func (c *dirsCmd) SetFlags(f *flag.FlagSet) {
	dbFlag(f, &c.db)
	f.StringVar(&c.by, "by", "content", "what makes directories copies: `content` or name")
	f.StringVar(&c.min, "min", "0", "only directories of at least this `size`")
	f.Float64Var(&c.similar, "similar", 0.5, "-by name: the `share` of files in common, from 0 to 1")
	f.IntVar(&c.maxGroup, "max-group", 50,
		"-by name: skip a name more than this `many` directories have, such as src or .git")
	c.out.flags(f)
}

func (c *dirsCmd) Execute(ctx context.Context, f *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	return exit(c.run(ctx, f.Args()))
}

func (c *dirsCmd) run(ctx context.Context, args []string) error {
	minSize, err := parseSize(c.min)
	if err != nil {
		return usagef("-min %s: %v", c.min, err)
	}
	if c.by != "content" && c.by != "name" {
		return usagef("-by %s: content or name", c.by)
	}
	scope, err := canonicalAll(args)
	if err != nil {
		return err
	}
	db, err := openDB(c.db, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if c.by == "name" {
		pairs, skipped, err := namePairs(ctx, db, c.similar, c.maxGroup)
		if err != nil {
			return err
		}
		if skipped > 0 {
			fmt.Fprintf(os.Stderr, "skipped %d names more than %d directories share; -max-group changes that\n",
				skipped, c.maxGroup)
		}
		pairs = slices.DeleteFunc(pairs, func(p dirPair) bool {
			return (p.a.bytes < minSize && p.b.bytes < minSize) ||
				(!inAny(scope, p.a.path) && !inAny(scope, p.b.path))
		})
		return c.out.write(len(pairs), func(w *writer, n int) error {
			return writePairs(w, pairs[:n])
		})
	}

	groups, err := contentGroups(ctx, db)
	if err != nil {
		return err
	}
	groups = slices.DeleteFunc(groups, func(g dirGroup) bool {
		if g.bytes < minSize {
			return true
		}
		for _, d := range g.dirs {
			if inAny(scope, d) {
				return false
			}
		}
		return len(scope) > 0
	})
	return c.out.write(len(groups), func(w *writer, n int) error {
		return writeDirGroups(w, groups[:n])
	})
}

// inAny reports whether path is in one of the dirs; with none, every path is.
func inAny(dirs []string, path string) bool {
	if len(dirs) == 0 {
		return true
	}
	for _, d := range dirs {
		if within(path, d) {
			return true
		}
	}
	return false
}

// dirGroup is directories of the same content.
type dirGroup struct {
	dirs  []string
	files int64 // in each
	bytes int64 // in each
}

func (g dirGroup) waste() int64 { return g.bytes * int64(len(g.dirs)-1) }

// contentGroups returns the groups of directories of the same content, the
// most space to reclaim first.
func contentGroups(ctx context.Context, db *sql.DB) ([]dirGroup, error) {
	rows, err := db.QueryContext(ctx, `
		select sig, path, files, bytes from dirs where sig in (
			select sig from dirs where sig is not null group by sig having count(*) > 1)
		order by sig, path`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var groups []*dirGroup
	var sigs []string
	sigOf := map[string]string{}
	for rows.Next() {
		var sig []byte
		var path string
		var files, bytes int64
		if err := rows.Scan(&sig, &path, &files, &bytes); err != nil {
			return nil, err
		}
		sigOf[path] = string(sig)
		if len(sigs) == 0 || sigs[len(sigs)-1] != string(sig) {
			sigs = append(sigs, string(sig))
			groups = append(groups, &dirGroup{files: files, bytes: bytes})
		}
		g := groups[len(groups)-1]
		g.dirs = append(g.dirs, path)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []dirGroup
	for i, g := range groups {
		// A directory holding nothing but one copy has the same content as
		// that copy; the copy is the one to name.
		all := g.dirs
		g.dirs = slices.DeleteFunc(slices.Clone(all), func(d string) bool {
			for _, e := range all {
				if e != d && within(e, d) {
					return true
				}
			}
			return false
		})
		if len(g.dirs) < 2 || nested(g.dirs, sigs[i], sigOf) {
			continue
		}
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b dirGroup) int {
		if c := cmp.Compare(b.waste(), a.waste()); c != 0 {
			return c
		}
		return cmp.Compare(a.dirs[0], b.dirs[0])
	})
	return out, nil
}

// nested reports whether the dirs sit one each in directories that are
// copies of one another: then the group of the parents covers this one.
func nested(dirs []string, sig string, sigOf map[string]string) bool {
	seen := map[string]bool{}
	var parentSig string
	for i, d := range dirs {
		p := filepath.Dir(d)
		s, ok := sigOf[p]
		if !ok || seen[p] || s == sig || (i > 0 && s != parentSig) {
			return false
		}
		seen[p] = true
		parentSig = s
	}
	return true
}

// dirPair is two directories of the same name and what they share.
type dirPair struct {
	name        string
	a, b        dirSet
	common      int     // distinct files in both
	commonBytes int64   // their bytes
	score       float64 // common over the distinct files of either
}

// dirSet is the distinct files of a directory, by hash, and how many it has
// no hash for: those are unique, so they count only towards its total.
type dirSet struct {
	path     string
	files    int64
	bytes    int64
	hashes   map[string]int64
	unhashed int
}

func (s dirSet) total() int { return len(s.hashes) + s.unhashed }

// namePairs returns the pairs of directories of the same name that have at
// least the share similar of their files in common, the most bytes in
// common first. A pair inside a pair of their parents is left out.
func namePairs(ctx context.Context, db *sql.DB, similar float64, maxGroup int) ([]dirPair, int, error) {
	rows, err := db.QueryContext(ctx, `
		select lname, path, files, bytes from dirs where lname in (
			select lname from dirs group by lname having count(*) > 1)
		order by lname, path`)
	if err != nil {
		return nil, 0, err
	}
	type member struct {
		path  string
		files int64
		bytes int64
	}
	var names []string
	var groups [][]member
	for rows.Next() {
		var name string
		var m member
		if err := rows.Scan(&name, &m.path, &m.files, &m.bytes); err != nil {
			_ = rows.Close()
			return nil, 0, err
		}
		if len(names) == 0 || names[len(names)-1] != name {
			names = append(names, name)
			groups = append(groups, nil)
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], m)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	var pairs []dirPair
	skipped := 0
	for _, g := range groups {
		if len(g) > maxGroup {
			skipped++
			continue
		}
		sets := make([]dirSet, len(g))
		for i, m := range g {
			if sets[i], err = loadSet(ctx, db, m.path); err != nil {
				return nil, 0, err
			}
			sets[i].files, sets[i].bytes = m.files, m.bytes
		}
		for i := range sets {
			for j := i + 1; j < len(sets); j++ {
				a, b := sets[i], sets[j]
				if within(a.path, b.path) || within(b.path, a.path) {
					continue
				}
				if len(a.hashes) > len(b.hashes) {
					a, b = b, a
				}
				p := dirPair{name: filepath.Base(g[i].path), a: sets[i], b: sets[j]}
				for h, size := range a.hashes {
					if _, ok := b.hashes[h]; ok {
						p.common++
						p.commonBytes += size
					}
				}
				if p.common == 0 {
					continue
				}
				p.score = float64(p.common) / float64(a.total()+b.total()-p.common)
				if p.score >= similar {
					pairs = append(pairs, p)
				}
			}
		}
	}

	key := func(a, b string) string {
		if a > b {
			a, b = b, a
		}
		return a + "\x00" + b
	}
	found := map[string]bool{}
	for _, p := range pairs {
		found[key(p.a.path, p.b.path)] = true
	}
	pairs = slices.DeleteFunc(pairs, func(p dirPair) bool {
		return found[key(filepath.Dir(p.a.path), filepath.Dir(p.b.path))]
	})
	slices.SortFunc(pairs, func(x, y dirPair) int {
		if c := cmp.Compare(y.commonBytes, x.commonBytes); c != 0 {
			return c
		}
		return cmp.Compare(x.a.path, y.a.path)
	})
	return pairs, skipped, nil
}

func loadSet(ctx context.Context, db *sql.DB, dir string) (dirSet, error) {
	s := dirSet{path: dir, hashes: map[string]int64{}}
	lo, hi := under(dir)
	rows, err := db.QueryContext(ctx, `select hash, size from files where path >= ? and path < ?`, lo, hi)
	if err != nil {
		return s, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var hash []byte
		var size int64
		if err := rows.Scan(&hash, &size); err != nil {
			return s, err
		}
		if hash == nil {
			s.unhashed++
			continue
		}
		s.hashes[string(hash)] = size
	}
	return s, rows.Err()
}
