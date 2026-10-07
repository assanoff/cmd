// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package subs

import (
	"strings"
	"testing"
	"time"
)

// A wrong time here is invisible: the summary still reads well, it just points
// at the wrong minute of the recording. That is the reason this one piece has
// a test when the rest of the pipeline does not.
func TestParseStamp(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"00:00:01,000", time.Second, true},
		{"01:02:03,004", time.Hour + 2*time.Minute + 3*time.Second + 4*time.Millisecond, true},
		{"00:01:30.500", 90*time.Second + 500*time.Millisecond, true},
		{"02:07.250", 2*time.Minute + 7*time.Second + 250*time.Millisecond, true}, // WebVTT drops the hours
		{"00:00:05", 5 * time.Second, true},                                       // and the milliseconds
		{"00:00:00,5", 500 * time.Millisecond, true},                              // short fraction pads, not truncates
		{"", 0, false},
		{"nonsense", 0, false},
		{"1:2:3:4", 0, false},
	}

	for _, c := range cases {
		got, ok := ParseStamp(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseStamp(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

const sampleSRT = `1
00:00:01,000 --> 00:00:04,000
Hello there.

2
00:00:20,000 --> 00:00:24,000
Still the first minute.

3
00:01:30,000 --> 00:01:33,000
A new paragraph starts here.
`

func TestStampedParagraphs(t *testing.T) {
	if !Is(sampleSRT) {
		t.Fatal("Is did not recognize SubRip")
	}

	_, cues := Parse(sampleSRT)
	if len(cues) != 3 {
		t.Fatalf("Parse: got %d cues, want 3", len(cues))
	}

	got := StampedParagraphs(cues, time.Minute)
	want := "[00:00:01] Hello there. Still the first minute.\n\n" +
		"[00:01:30] A new paragraph starts here."
	if got != want {
		t.Errorf("StampedParagraphs:\n got %q\nwant %q", got, want)
	}

	// Paragraph breaks are what keep a marker attached to its own text when
	// chunk.Split later cuts the document up.
	if n := len(strings.Split(got, "\n\n")); n != 2 {
		t.Errorf("got %d paragraphs, want 2", n)
	}

	// --timestamps 0 asks for plain prose.
	if got := StampedParagraphs(cues, 0); strings.Contains(got, "[") {
		t.Errorf("StampedParagraphs with every=0 kept a marker: %q", got)
	}
}

// A cue whose timing line is unreadable must not take its words down with it.
func TestStampedParagraphsKeepsUntimedText(t *testing.T) {
	cues := []Cue{
		{Header: []string{"00:00:01,000 --> 00:00:02,000"}, Text: "first"},
		{Header: []string{"broken --> line"}, Text: "second"},
	}
	got := StampedParagraphs(cues, time.Minute)
	if !strings.Contains(got, "second") {
		t.Errorf("dropped the text of an untimed cue: %q", got)
	}
}

// Round-tripping is what --keep-format rests on: the timings that come out
// have to be the ones that went in, whatever happened to the words between.
func TestParseRenderRoundTrip(t *testing.T) {
	preamble, cues := Parse(sampleSRT)

	got := Render(preamble, cues)
	if got != sampleSRT+"\n" && got != sampleSRT {
		// Render ends every cue with a blank line, so the only legal drift is
		// one trailing newline.
		if strings.TrimRight(got, "\n") != strings.TrimRight(sampleSRT, "\n") {
			t.Errorf("Render:\n got %q\nwant %q", got, sampleSRT)
		}
	}

	for i, c := range cues {
		if _, ok := c.Start(); !ok {
			t.Errorf("cue %d lost its timing: %v", i, c.Header)
		}
	}
}

// WebVTT has no cue numbers and carries a header line that is not a cue.
func TestParseWebVTT(t *testing.T) {
	const in = `WEBVTT

00:00:01.000 --> 00:00:04.000
Hello there.
`
	if !Is(in) {
		t.Fatal("Is did not recognize WebVTT")
	}

	preamble, cues := Parse(in)
	if len(cues) != 1 {
		t.Fatalf("got %d cues, want 1", len(cues))
	}
	if len(preamble) == 0 || !strings.HasPrefix(preamble[0], "WEBVTT") {
		t.Errorf("lost the WEBVTT header: %q", preamble)
	}
	if cues[0].Text != "Hello there." {
		t.Errorf("got text %q", cues[0].Text)
	}
}
