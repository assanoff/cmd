// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The tracklist parser is worth a test because everything it gets wrong is
// invisible. A dropped track does not fail: the feed builds, the episode
// plays, and one chapter is simply not there. The generator this replaces
// shipped with exactly that bug — its playlist writer left out the four-line
// preamble, so the first track became the description and the second vanished
// into the blank line, in every mix it ever produced.

func TestParseTrackLine(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   *track
		absent bool
	}{
		{
			name: "indexed with em dash",
			line: "01. 0:00:00 Trouble Funk — Pump Me Up",
			want: &track{num: 1, startSec: 0, artist: "Trouble Funk", name: "Pump Me Up"},
		},
		{
			name: "no index, hyphen, MM:SS",
			line: "4:31 Chic - Good Times",
			want: &track{startSec: 271, artist: "Chic", name: "Good Times"},
		},
		{
			name: "hours",
			line: "12. 1:02:14 Artist — Track",
			want: &track{num: 12, startSec: 3734, artist: "Artist", name: "Track"},
		},
		{
			name: "trailing url",
			line: "3. 0:01:00 Chic — Good Times https://example.com/x",
			want: &track{num: 3, startSec: 60, artist: "Chic", name: "Good Times", url: "https://example.com/x"},
		},
		{
			name: "no separator leaves the whole thing as the artist",
			line: "5. 0:02:00 Unknown Artist",
			want: &track{num: 5, startSec: 120, artist: "Unknown Artist"},
		},
		{
			name:   "prose is not a track",
			line:   "Recorded live in 1982",
			absent: true,
		},
		{
			name:   "a bad timestamp is not a track",
			line:   "1. 99 Artist — Track",
			absent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTrackLine(tt.line)
			if tt.absent {
				if got != nil {
					t.Fatalf("parseTrackLine(%q) = %+v, want nil", tt.line, *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("parseTrackLine(%q) = nil, want %+v", tt.line, *tt.want)
			}
			if *got != *tt.want {
				t.Errorf("parseTrackLine(%q):\n got %+v\nwant %+v", tt.line, *got, *tt.want)
			}
		})
	}
}

func TestParsePLPreamble(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "set.pl")
	content := "Late Set 12\n\nThree tracks recorded live\n\n" +
		"1. 0:00:00 Trouble Funk — Pump Me Up\n" +
		"2. 0:00:30 Chic — Good Times\n" +
		"3. 0:01:00 Unknown Artist\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	ep, err := parsePL(path)
	if err != nil {
		t.Fatal(err)
	}
	if ep.title != "Late Set 12" {
		t.Errorf("title = %q, want %q", ep.title, "Late Set 12")
	}
	if ep.desc != "Three tracks recorded live" {
		t.Errorf("desc = %q, want %q", ep.desc, "Three tracks recorded live")
	}
	if len(ep.tracks) != 3 {
		t.Fatalf("got %d tracks, want 3: %+v", len(ep.tracks), ep.tracks)
	}
	if ep.tracks[0].artist != "Trouble Funk" {
		t.Errorf("first track = %+v, want it to be the first line of the tracklist", ep.tracks[0])
	}
}

func TestParsePLMissingPreambleLosesTracks(t *testing.T) {
	// Not a wish, a warning: this is what a file without the blank lines does,
	// and the reason mix writes them.
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.pl")
	content := "Late Set 12\n\n" +
		"1. 0:00:00 Trouble Funk — Pump Me Up\n" +
		"2. 0:00:30 Chic — Good Times\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	ep, err := parsePL(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(ep.tracks) != 0 {
		t.Errorf("got %d tracks, want 0 — a four-line preamble is required", len(ep.tracks))
	}
}

func TestParseTimestamp(t *testing.T) {
	tests := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0:00", 0, true},
		{"4:31", 271, true},
		{"0:04:31", 271, true},
		{"1:02:14", 3734, true},
		{"01:02:14", 3734, true},
		{"99", 0, false},
		{"1:2:3:4", 0, false},
		{"a:bc", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseTimestamp(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("parseTimestamp(%q) = %d, %v; want %d, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestFormatDurations(t *testing.T) {
	if got := formatTimestamp(3734); got != "01:02:14" {
		t.Errorf("formatTimestamp(3734) = %q, want %q", got, "01:02:14")
	}
	if got := formatShortDuration(3734); got != "1:02:14" {
		t.Errorf("formatShortDuration(3734) = %q, want %q", got, "1:02:14")
	}
	if got := formatShortDuration(90); got != "1:30" {
		t.Errorf("formatShortDuration(90) = %q, want %q", got, "1:30")
	}
}
