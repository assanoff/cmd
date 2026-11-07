// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package seek

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assanoff/cmd/ai/internal/index"
)

func TestPrintText(t *testing.T) {
	hits := []index.Hit{{
		Path:  "/pods/ep 12.mp3",
		Start: time.Hour,
		Text:  "the passage",
		At:    time.Hour + 23*time.Minute + 45*time.Second,
		Quote: strings.Repeat("слово ", 100),
	}}
	var b bytes.Buffer
	if err := printText(&b, hits); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.HasPrefix(out, "01:23:45  /pods/ep 12.mp3\n    слово") || !strings.HasSuffix(out, "слово …\n\n") {
		t.Errorf("got %q", out)
	}
}

// Scripts read these field names, and an empty result as an empty list.
func TestPrintJSON(t *testing.T) {
	var b bytes.Buffer
	if err := printJSON(&b, nil); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(b.String()) != "[]" {
		t.Errorf("no hits: got %s, want []", b.String())
	}

	b.Reset()
	hit := index.Hit{
		Path: "/a.mp3", Title: "a", Start: 90 * time.Second, End: 150 * time.Second, Text: "t", Score: 0.5,
		At: 100 * time.Second, Quote: "q", Similarity: 0.4,
	}
	if err := printJSON(&b, []index.Hit{hit}); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"path": "/a.mp3", "title": "a", "at": "00:01:40", "at_ms": 100000.0, "quote": "q",
		"start_ms": 90000.0, "end_ms": 150000.0, "text": "t", "score": 0.5, "similarity": 0.4,
	}
	if len(got) != 1 || len(got[0]) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[0][k] != v {
			t.Errorf("%s = %v, want %v", k, got[0][k], v)
		}
	}
}

func TestSnippet(t *testing.T) {
	if got := snippet("short", 10); got != "short" {
		t.Errorf("got %q", got)
	}
	if got := snippet("один два три четыре", 12); got != "один два …" {
		t.Errorf("got %q", got)
	}
}

// A transcript beside a file is read as segments; a cue with no readable time
// is dropped rather than placed somewhere it was not said.
func TestSegmentsFromSubtitles(t *testing.T) {
	const srt = `1
00:00:01,000 --> 00:00:04,500
Привет.

2
broken --> timing
Без времени.

3
00:01:00,000 --> 00:01:02,000
Про налоги.
`
	got := segments(srt)
	want := []index.Segment{
		{Start: time.Second, End: 4500 * time.Millisecond, Text: "Привет."},
		{Start: time.Minute, End: 62 * time.Second, Text: "Про налоги."},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("segment %d: %+v, want %+v", i, got[i], want[i])
		}
	}
}
