// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package index

import (
	"fmt"
	"testing"
	"time"
)

// segments returns n five-second segments back to back.
func segments(n int) []Segment {
	segs := make([]Segment, n)
	for i := range segs {
		start := time.Duration(i) * 5 * time.Second
		segs[i] = Segment{Start: start, End: start + 5*time.Second, Text: fmt.Sprint(i)}
	}
	return segs
}

func TestWindowsOverlap(t *testing.T) {
	got := Windows(segments(30), time.Minute, 15*time.Second)

	want := []struct{ start, end time.Duration }{
		{0, 60 * time.Second},
		{45 * time.Second, 105 * time.Second},
		{90 * time.Second, 150 * time.Second},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d windows, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Start != w.start || got[i].End != w.end {
			t.Errorf("window %d: %v-%v, want %v-%v", i, got[i].Start, got[i].End, w.start, w.end)
		}
	}
	if got[0].Text != "0 1 2 3 4 5 6 7 8 9 10 11" {
		t.Errorf("window 0 text %q", got[0].Text)
	}
}

// A segment longer than a window still gets indexed, alone, and the windows
// after it carry on.
func TestWindowsLongSegment(t *testing.T) {
	segs := []Segment{
		{Start: 0, End: 3 * time.Minute, Text: "monologue"},
		{Start: 3 * time.Minute, End: 3*time.Minute + 5*time.Second, Text: "reply"},
	}
	got := Windows(segs, time.Minute, 15*time.Second)
	if len(got) != 2 || got[0].Text != "monologue" || got[1].Text != "reply" {
		t.Errorf("got %+v", got)
	}
}

func TestWindowsSkipSilence(t *testing.T) {
	segs := []Segment{
		{Start: 0, End: time.Second, Text: "  "},
		{Start: time.Second, End: 2 * time.Second, Text: " hello "},
	}
	got := Windows(segs, time.Minute, 15*time.Second)
	if len(got) != 1 || got[0].Text != "hello" || got[0].Start != time.Second {
		t.Errorf("got %+v", got)
	}
	if got := Windows(nil, time.Minute, 15*time.Second); len(got) != 0 {
		t.Errorf("no segments: got %+v", got)
	}
}
