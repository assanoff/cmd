// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"strings"
	"testing"
)

// The parser is tested because everything it gets wrong is invisible. A
// dropped line does not fail: the file gets its chapters, the audio plays, and
// one chapter is simply not there. A line wrongly accepted is worse — a
// sentence out of the prose becomes a chapter in the middle of the set.

func TestParseTracklistShapes(t *testing.T) {
	in := `Recorded live at the club, summer 1982.
Thanks to everyone who came.

0:00 Trouble Funk - Pump Me Up
1. 04:31 Chic — Good Times
[1:02:14] Kraftwerk – Numbers
Herbie Hancock - Rockit 1:15:00
02) 1:20:00 Man Parrish — Hip Hop Be Bop

Follow me: https://example.com/me
`
	got := parseTracklist(in)

	want := []chapter{
		{0, "Trouble Funk - Pump Me Up"},
		{271, "Chic — Good Times"},
		{3734, "Kraftwerk – Numbers"},
		{4500, "Herbie Hancock - Rockit"},
		{4800, "Man Parrish — Hip Hop Be Bop"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d chapters, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chapter %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestParseTracklistRejects(t *testing.T) {
	// Lines that must not become chapters.
	for _, line := range []string{
		"Recorded live at the club",
		"Thanks to everyone",
		"0:00",             // a time with nothing after it
		"1:2:3:4 Too deep", // not a timestamp
		"https://example.com/only-a-link",
	} {
		if got := parseTracklist(line); len(got) != 0 {
			t.Errorf("parseTracklist(%q) = %+v, want nothing", line, got)
		}
	}
}

func TestParseTracklistStripsTrailingLink(t *testing.T) {
	got := parseTracklist("3. 0:01:00 Chic — Good Times https://example.com/x")
	if len(got) != 1 {
		t.Fatalf("got %d chapters, want 1", len(got))
	}
	if got[0].title != "Chic — Good Times" {
		t.Errorf("title = %q, want %q", got[0].title, "Chic — Good Times")
	}
}

// The reason parsePL exists: a .pl description that happens to end in
// something timestamp-shaped must not become a chapter.
func TestParsePLSkipsPreamble(t *testing.T) {
	in := "Late Set 12\n\nRecorded live at 21:30\n\n" +
		"1. 0:00:00 Trouble Funk — Pump Me Up\n" +
		"2. 0:00:30 Chic — Good Times\n"

	if got := parsePL(in); len(got) != 2 {
		t.Errorf("parsePL gave %d chapters, want 2: %+v", len(got), got)
	}
	if got := parseTracklist(in); len(got) != 3 {
		t.Errorf("the tolerant parser should see the description as a track here, got %d: %+v", len(got), got)
	}
}

func TestOrderDropsNonAdvancing(t *testing.T) {
	in := []chapter{
		{100, "third"},
		{0, "first"},
		{50, "second"},
		{50, "duplicate"},
	}
	got := order(in, true)

	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("got %d chapters, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].title != want[i] {
			t.Errorf("chapter %d = %q, want %q", i, got[i].title, want[i])
		}
	}
}

func TestToSeconds(t *testing.T) {
	tests := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0:00", 0, true},
		{"4:31", 271, true},
		{"04:31", 271, true},
		{"1:02:14", 3734, true},
		{"99", 0, false},
		{"1:2:3:4", 0, false},
		{"a:bc", 0, false},
		{"-1:00", 0, false},
	}
	for _, tt := range tests {
		got, ok := toSeconds(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("toSeconds(%q) = %d, %v; want %d, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestFFMetadataBoundaries(t *testing.T) {
	chs := []chapter{{0, "one"}, {30, "two"}, {60, "three"}}
	got := string(ffmetadata(chs, 90000))

	for _, want := range []string{
		"START=0\nEND=29999\ntitle=one",
		"START=30000\nEND=59999\ntitle=two",
		"START=60000\nEND=90000\ntitle=three",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ffmetadata is missing:\n%s\n\ngot:\n%s", want, got)
		}
	}
}

func TestEscapeMeta(t *testing.T) {
	// ffmetadata treats these four as syntax; a track called "A=B" would
	// otherwise start a new key.
	if got, want := escapeMeta(`a=b;c#d\e`), `a\=b\;c\#d\\e`; got != want {
		t.Errorf("escapeMeta = %q, want %q", got, want)
	}
}
