// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package hear

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/assanoff/cmd/ai/internal/core/asr"
)

// The JSON shape is ours, not the SDK's: these field names are what scripts
// parse, so they must not drift when the SDK changes its struct.
type jsonTranscript struct {
	Text     string        `json:"text"`
	Language string        `json:"language"`
	Duration float64       `json:"duration"`
	Segments []jsonSegment `json:"segments"`
	Words    []jsonWord    `json:"words,omitempty"`
}

type jsonSegment struct {
	Index   int32   `json:"index"`
	StartMs int64   `json:"start_ms"`
	EndMs   int64   `json:"end_ms"`
	Text    string  `json:"text"`
	NoSpeed float32 `json:"no_speech_prob"`
}

type jsonWord struct {
	Text    string `json:"text"`
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
}

func render(w io.Writer, tr asr.Transcription, format string) error {
	switch format {
	case "json":
		return renderJSON(w, tr)
	case "srt":
		return renderSRT(w, tr)
	case "vtt":
		return renderVTT(w, tr)
	default:
		_, err := fmt.Fprintln(w, strings.TrimSpace(tr.Text))
		return err
	}
}

func renderJSON(w io.Writer, tr asr.Transcription) error {
	out := jsonTranscript{
		Text:     strings.TrimSpace(tr.Text),
		Language: tr.Language,
		Duration: tr.Duration,
		Segments: make([]jsonSegment, 0, len(tr.Segments)),
	}
	for _, seg := range tr.Segments {
		out.Segments = append(out.Segments, jsonSegment{
			Index:   seg.Index,
			StartMs: seg.StartMs,
			EndMs:   seg.EndMs,
			Text:    strings.TrimSpace(seg.Text),
			NoSpeed: seg.NoSpeechProb,
		})
	}
	for _, word := range tr.Words {
		out.Words = append(out.Words, jsonWord{
			Text:    word.Text,
			StartMs: word.StartMs,
			EndMs:   word.EndMs,
		})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// renderSRT numbers cues from 1 and separates the timestamp fields with a
// comma, as SubRip wants. Segments come from one whole-file decode, so the
// timings need no adjusting.
func renderSRT(w io.Writer, tr asr.Transcription) error {
	n := 0
	for _, seg := range tr.Segments {
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		n++
		_, err := fmt.Fprintf(w, "%d\n%s --> %s\n%s\n\n",
			n, stamp(seg.StartMs, ","), stamp(seg.EndMs, ","), text)
		if err != nil {
			return err
		}
	}
	return nil
}

func renderVTT(w io.Writer, tr asr.Transcription) error {
	if _, err := fmt.Fprint(w, "WEBVTT\n\n"); err != nil {
		return err
	}
	for _, seg := range tr.Segments {
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		_, err := fmt.Fprintf(w, "%s --> %s\n%s\n\n",
			stamp(seg.StartMs, "."), stamp(seg.EndMs, "."), text)
		if err != nil {
			return err
		}
	}
	return nil
}

// stamp formats milliseconds as HH:MM:SS<sep>mmm. SubRip uses a comma before
// the fraction, WebVTT a period.
func stamp(ms int64, sep string) string {
	if ms < 0 {
		ms = 0
	}
	d := time.Duration(ms) * time.Millisecond
	return fmt.Sprintf("%02d:%02d:%02d%s%03d",
		int(d/time.Hour),
		int(d%time.Hour/time.Minute),
		int(d%time.Minute/time.Second),
		sep,
		int(d%time.Second/time.Millisecond),
	)
}
