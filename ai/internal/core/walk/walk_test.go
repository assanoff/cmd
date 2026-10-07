// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package walk

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// txt is the Ext function most subcommands pass: one extension, whatever the
// input was called.
func txt(string) string { return ".txt" }

// tree writes a set of files under a fresh temp directory and returns its path.
func tree(t *testing.T, names ...string) string {
	t.Helper()

	root := t.TempDir()
	for _, name := range names {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("body\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// One input is the pipeline case and goes straight to standard output.
func TestOneFileGoesToStdout(t *testing.T) {
	root := tree(t, "a.txt")

	jobs, err := Jobs(Request{Args: []string{filepath.Join(root, "a.txt")}, Exts: "txt", Ext: txt})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	if jobs[0].Out != "" {
		t.Errorf("Out = %q, want empty for standard output", jobs[0].Out)
	}
}

func TestOneFileWithOutFlag(t *testing.T) {
	root := tree(t, "a.txt")

	jobs, err := Jobs(Request{
		Args: []string{filepath.Join(root, "a.txt")},
		Out:  filepath.Join(root, "answer.md"),
		Exts: "txt", Ext: txt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if jobs[0].Out != filepath.Join(root, "answer.md") {
		t.Errorf("Out = %q", jobs[0].Out)
	}
}

// -o names one file, so it cannot stand for several results.
func TestOutFlagRejectsABatch(t *testing.T) {
	root := tree(t, "a.txt", "b.txt")

	_, err := Jobs(Request{
		Args: []string{filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt")},
		Out:  filepath.Join(root, "answer.txt"),
		Exts: "txt", Ext: txt,
	})

	var be *BatchError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v, want a *BatchError", err)
	}
	if be.Count != 2 {
		t.Errorf("BatchError.Count = %d, want 2", be.Count)
	}
}

// Standard input has no name to derive an output file from.
func TestStdinCannotJoinABatch(t *testing.T) {
	root := tree(t, "a.txt")

	_, err := Jobs(Request{
		Args:   []string{"-", filepath.Join(root, "a.txt")},
		OutDir: t.TempDir(),
		Exts:   "txt", Ext: txt,
	})

	var be *BatchError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v, want a *BatchError", err)
	}
}

// A directory contributes the files directly inside it, and nothing below,
// until -r says otherwise.
func TestDirectoryIsShallowByDefault(t *testing.T) {
	root := tree(t, "a.txt", "b.txt", "deep/c.txt", "skip.bin")

	jobs, err := Jobs(Request{Args: []string{root}, Exts: "txt", Ext: txt, OutDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2 (a.txt and b.txt)", len(jobs))
	}
	for _, j := range jobs {
		if filepath.Base(j.In) == "c.txt" {
			t.Error("descended into a subdirectory without -r")
		}
		if filepath.Ext(j.In) == ".bin" {
			t.Error("accepted a file the --ext list excludes")
		}
	}
}

func TestRecurseDescends(t *testing.T) {
	root := tree(t, "a.txt", "deep/c.txt")

	jobs, err := Jobs(Request{Args: []string{root}, Recurse: true, Exts: "txt", Ext: txt, OutDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(jobs))
	}
}

// -O must not flatten a tree into one directory: two files called c.txt in
// different places would overwrite each other.
func TestOutDirPreservesLayout(t *testing.T) {
	root := tree(t, "deep/c.txt")
	out := t.TempDir()

	jobs, err := Jobs(Request{
		Args: []string{root}, Recurse: true, OutDir: out,
		Exts: "txt", Ext: txt, Suffix: "-sum",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}

	want := filepath.Join(out, "deep", "c-sum.txt")
	if jobs[0].Out != want {
		t.Errorf("Out = %q, want %q", jobs[0].Out, want)
	}
}

// Without -O a result lands beside its source.
func TestBatchWritesBesideTheSource(t *testing.T) {
	root := tree(t, "a.txt", "b.txt")

	jobs, err := Jobs(Request{Args: []string{root}, Exts: "txt", Ext: txt, Suffix: "-sum"})
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if filepath.Dir(j.Out) != root {
			t.Errorf("Out = %q, want it beside %q", j.Out, j.In)
		}
	}
}

// tr keeps the input's own extension: translating subtitles has to produce
// subtitles.
func TestExtFunctionSeesTheInput(t *testing.T) {
	root := tree(t, "a.srt", "b.md")

	jobs, err := Jobs(Request{
		Args: []string{root}, Exts: "srt,md", Suffix: "-en",
		Ext: filepath.Ext,
	})
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, j := range jobs {
		got[filepath.Base(j.Out)] = true
	}
	for _, want := range []string{"a-en.srt", "b-en.md"} {
		if !got[want] {
			t.Errorf("missing %s; got %v", want, got)
		}
	}
}

// A named file is read whatever it is called; --ext only filters a walk.
func TestNamedFileIgnoresExtFilter(t *testing.T) {
	root := tree(t, "notes.rst")

	jobs, err := Jobs(Request{Args: []string{filepath.Join(root, "notes.rst")}, Exts: "txt", Ext: txt})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
}

// A directory that matches nothing is worth saying out loud, not worth failing
// over: it is usually one bad --ext in a run over several directories.
func TestEmptyDirectoryIsReported(t *testing.T) {
	root := tree(t, "skip.bin")

	var said bool
	jobs, err := Jobs(Request{
		Args: []string{root}, Exts: "txt", Ext: txt,
		Report: func(string, ...any) { said = true },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Errorf("got %d jobs, want none", len(jobs))
	}
	if !said {
		t.Error("said nothing about a directory that matched no files")
	}
}

func TestMissingFileIsAnError(t *testing.T) {
	if _, err := Jobs(Request{Args: []string{filepath.Join(t.TempDir(), "nope.txt")}, Exts: "txt", Ext: txt}); err == nil {
		t.Error("a missing input did not fail")
	}
}

// The walk order has to be reproducible, or a batch resumed after an
// interruption does the work in a different order every time.
func TestWalkOrderIsLexical(t *testing.T) {
	root := tree(t, "c.txt", "a.txt", "b.txt")

	jobs, err := Jobs(Request{Args: []string{root}, Exts: "txt", Ext: txt})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.txt", "b.txt", "c.txt"}
	for i, j := range jobs {
		if filepath.Base(j.In) != want[i] {
			t.Fatalf("job %d is %q, want %q", i, filepath.Base(j.In), want[i])
		}
	}
}

func TestExtSetToleratesSpacingAndDots(t *testing.T) {
	root := tree(t, "a.txt", "b.md")

	jobs, err := Jobs(Request{Args: []string{root}, Exts: " .TXT , md ", Ext: txt})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Errorf("got %d jobs, want 2; --ext parsing is too strict", len(jobs))
	}
}
