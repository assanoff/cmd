// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The cases that matter are the ones each shortcut of a scan could get wrong:
// files of the same size that differ only at the end, or only in the middle
// where the partial hash does not look, and a directory that is a copy only
// because the one it sits in is.

func TestScan(t *testing.T) {
	ctx := context.Background()
	root := tree(t, map[string][]byte{
		"a/Photos/x.jpg":      []byte("one"),
		"a/Photos/2019/y.jpg": noise(1, 200<<10),
		"b/Photos/x.jpg":      []byte("one"),
		"b/Photos/2019/y.jpg": noise(1, 200<<10),
		"c/Photos/x.jpg":      []byte("one"),
		"c/Photos/q.jpg":      []byte("zzz"),
		"d/big1/v.bin":        noise(2, 300<<10),
		"e/big2/v.bin":        noise(2, 300<<10),
		"d/tail.bin":          patch(noise(2, 300<<10), 300<<10-1, 'x'),
		"d/middle.bin":        patch(noise(2, 300<<10), 150<<10, 'x'),
		"a/note.txt":          []byte("unique"),
		"a/empty":             nil,
	})
	db := filepath.Join(t.TempDir(), "dupes.db")
	scan(t, db, root)

	got := fileGroups(t, db, "hash")
	want := [][]string{
		{"d/big1/v.bin", "e/big2/v.bin"},
		{"a/Photos/2019/y.jpg", "b/Photos/2019/y.jpg"},
		{"a/Photos/x.jpg", "b/Photos/x.jpg", "c/Photos/x.jpg"},
	}
	if !equal(got, want, root) {
		t.Errorf("files -by hash:\ngot  %q\nwant %q", rel(got, root), want)
	}

	sql, err := openDB(db, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sql.Close() }()
	groups, err := contentGroups(ctx, sql)
	if err != nil {
		t.Fatal(err)
	}
	var dirs [][]string
	for _, g := range groups {
		dirs = append(dirs, g.dirs)
	}
	// Photos/2019 is a copy too, but only because Photos is.
	want = [][]string{{"d/big1", "e/big2"}, {"a/Photos", "b/Photos"}}
	if !equal(dirs, want, root) {
		t.Errorf("dirs:\ngot  %q\nwant %q", rel(dirs, root), want)
	}

	pairs, _, err := namePairs(ctx, sql, 0.3, 50)
	if err != nil {
		t.Fatal(err)
	}
	var names [][]string
	for _, p := range pairs {
		names = append(names, []string{p.a.path, p.b.path})
	}
	// a and c share x.jpg of three files, a third; a and b share all.
	want = [][]string{{"a/Photos", "b/Photos"}, {"a/Photos", "c/Photos"}, {"b/Photos", "c/Photos"}}
	if !equal(names, want, root) {
		t.Errorf("dirs -by name:\ngot  %q\nwant %q", rel(names, root), want)
	}
}

func TestRescan(t *testing.T) {
	root := tree(t, map[string][]byte{
		"a/v.bin": noise(3, 300<<10),
		"b/v.bin": noise(3, 300<<10),
		"c/v.bin": noise(3, 300<<10),
	})
	db := filepath.Join(t.TempDir(), "dupes.db")
	scan(t, db, root)
	if got := fileGroups(t, db, "hash"); len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("first scan: %q", rel(got, root))
	}

	// The same size and a new time: the hash has to be read again.
	b := filepath.Join(root, "b", "v.bin")
	if err := os.WriteFile(b, patch(noise(3, 300<<10), 1000, 'x'), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(b, later, later); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "c", "v.bin")); err != nil {
		t.Fatal(err)
	}
	scan(t, db, root)
	if got := fileGroups(t, db, "hash"); len(got) != 0 {
		t.Errorf("after the change: %q, want no groups", rel(got, root))
	}
	if got := fileGroups(t, db, "name"); len(got) != 1 || len(got[0]) != 2 {
		t.Errorf("-by name after the change: %q, want a/v.bin and b/v.bin", rel(got, root))
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{
		"0":     0,
		"1":     1,
		"4K":    4 << 10,
		"4kib":  4 << 10,
		"100M":  100 << 20,
		"1.5GB": 3 << 29,
		"2T":    2 << 40,
	} {
		got, err := parseSize(in)
		if err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "M", "-1", "ten"} {
		if _, err := parseSize(in); err == nil {
			t.Errorf("parseSize(%q) did not fail", in)
		}
	}
}

func TestFormatSize(t *testing.T) {
	for in, want := range map[int64]string{
		0:          "0 B",
		1023:       "1023 B",
		1024:       "1.0 KB",
		1536 << 20: "1.5 GB",
	} {
		if got := formatSize(in); got != want {
			t.Errorf("formatSize(%d) = %q, want %q", in, got, want)
		}
	}
}

// The content of a directory may not depend on the order its files are met,
// which is the order of the rows, not of the disk.
func TestContentOrder(t *testing.T) {
	hashes := [][]byte{
		bytes.Repeat([]byte{0xff}, 32),
		bytes.Repeat([]byte{0x01}, 32),
		bytes.Repeat([]byte{0x80}, 32),
	}
	var a, b content
	for _, h := range hashes {
		a.addFile(1, h)
	}
	for _, h := range slices.Backward(hashes) {
		b.addFile(1, h)
	}
	if !bytes.Equal(a.sig(), b.sig()) {
		t.Error("the order of the files changed the content")
	}
	var c, d content
	c.addFile(1, hashes[0])
	d.addFile(1, hashes[1])
	d.addFile(1, hashes[2])
	c.add(&d)
	if !bytes.Equal(a.sig(), c.sig()) {
		t.Error("a directory summed from its parts differs from one summed from its files")
	}
}

// tree writes the files into a new directory and returns its canonical path.
func tree(t *testing.T, files map[string][]byte) string {
	t.Helper()
	root, err := canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func scan(t *testing.T, db, root string) {
	t.Helper()
	c := &scanCmd{db: db, min: "1", jobs: 2, quiet: true}
	if err := c.run(context.Background(), []string{root}); err != nil {
		t.Fatal(err)
	}
}

// fileGroups runs dupes files -by by -f json -n 0 and returns the paths of
// each group.
func fileGroups(t *testing.T, db, by string) [][]string {
	t.Helper()
	var buf bytes.Buffer
	stdout = &buf
	defer func() { stdout = os.Stdout }()
	c := &filesCmd{db: db, by: by, sort: "waste", min: "0"}
	c.out = output{n: 0, format: "json"}
	if err := c.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var groups []fileGroup
	if err := json.Unmarshal(buf.Bytes(), &groups); err != nil {
		t.Fatalf("%v\n%s", err, buf.Bytes())
	}
	var out [][]string
	for _, g := range groups {
		var paths []string
		for _, f := range g.Files {
			paths = append(paths, f.Path)
		}
		out = append(out, paths)
	}
	return out
}

// noise returns n bytes that are the same for the same seed.
func noise(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, seed))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func patch(b []byte, i int, c byte) []byte {
	b[i] = c
	return b
}

// rel turns the paths below root into slash-separated relative ones.
func rel(groups [][]string, root string) [][]string {
	var out [][]string
	for _, g := range groups {
		var r []string
		for _, p := range g {
			p = strings.TrimPrefix(p, withSep(root))
			r = append(r, filepath.ToSlash(p))
		}
		slices.Sort(r)
		out = append(out, r)
	}
	return out
}

func equal(got, want [][]string, root string) bool {
	return slices.EqualFunc(rel(got, root), want, slices.Equal)
}
