// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package hear

import (
	"strings"
	"testing"

	"github.com/assanoff/cmd/ai/internal/asr"
	"github.com/assanoff/cmd/ai/internal/subs"
)

// hear is the only writer of these files and tr and sum are the only readers,
// so the format contract spans two subcommands and is held by package subs.
// These tests are what keeps the two halves honest.

func transcription() asr.Transcription {
	return asr.Transcription{
		Text: "Hello there. Second line.",
		Segments: []asr.Segment{
			{Index: 0, StartMs: 0, EndMs: 1500, Text: " Hello there. "},
			{Index: 1, StartMs: 1500, EndMs: 3725, Text: "Second line."},
			{Index: 2, StartMs: 3725, EndMs: 4000, Text: "   "}, // dropped: no text
		},
	}
}

func TestSRTRoundTrips(t *testing.T) {
	var sb strings.Builder
	if err := render(&sb, transcription(), "srt"); err != nil {
		t.Fatal(err)
	}
	out := sb.String()

	if !subs.Is(out) {
		t.Fatalf("rendered SubRip is not recognized as subtitles:\n%s", out)
	}

	_, cues := subs.Parse(out)
	if len(cues) != 2 {
		t.Fatalf("got %d cues, want 2:\n%s", len(cues), out)
	}
	if cues[0].Text != "Hello there." {
		t.Errorf("cue 0 text = %q, want %q", cues[0].Text, "Hello there.")
	}
	want := []string{"1", "00:00:00,000 --> 00:00:01,500"}
	if strings.Join(cues[0].Header, "|") != strings.Join(want, "|") {
		t.Errorf("cue 0 header = %q, want %q", cues[0].Header, want)
	}
	if got := cues[1].Header[1]; got != "00:00:01,500 --> 00:00:03,725" {
		t.Errorf("cue 1 timing = %q", got)
	}
}

func TestVTTRoundTrips(t *testing.T) {
	var sb strings.Builder
	if err := render(&sb, transcription(), "vtt"); err != nil {
		t.Fatal(err)
	}
	out := sb.String()

	if !strings.HasPrefix(out, "WEBVTT\n\n") {
		t.Fatalf("WebVTT must open with its magic line:\n%s", out)
	}
	if !subs.Is(out) {
		t.Fatalf("rendered WebVTT is not recognized as subtitles:\n%s", out)
	}

	_, cues := subs.Parse(out)
	if len(cues) != 2 {
		t.Fatalf("got %d cues, want 2:\n%s", len(cues), out)
	}
	// WebVTT has no index line, and a period before the fraction.
	if got := cues[0].Header[len(cues[0].Header)-1]; got != "00:00:00.000 --> 00:00:01.500" {
		t.Errorf("cue 0 timing = %q", got)
	}
}

// A negative start time is clamped rather than formatted as a negative hour,
// which would make the cue unparseable.
func TestStampClampsNegative(t *testing.T) {
	if got := stamp(-1, ","); got != "00:00:00,000" {
		t.Errorf("stamp(-1) = %q, want 00:00:00,000", got)
	}
}
