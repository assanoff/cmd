// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package hear

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/assanoff/cmd/ai/internal/asr"
	"github.com/assanoff/cmd/ai/internal/subs"
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
	var cues []subs.Cue
	for _, seg := range tr.Segments {
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		cues = append(cues, subs.Cue{
			Header: []string{
				strconv.Itoa(len(cues) + 1),
				timing(seg, ","),
			},
			Text: text,
		})
	}
	_, err := io.WriteString(w, subs.Render(nil, cues))
	return err
}

func renderVTT(w io.Writer, tr asr.Transcription) error {
	var cues []subs.Cue
	for _, seg := range tr.Segments {
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		cues = append(cues, subs.Cue{Header: []string{timing(seg, ".")}, Text: text})
	}
	_, err := io.WriteString(w, subs.Render([]string{"WEBVTT"}, cues))
	return err
}

// timing is the "start --> end" line of one cue.
func timing(seg asr.Segment, sep string) string {
	return stamp(seg.StartMs, sep) + " --> " + stamp(seg.EndMs, sep)
}

// stamp formats milliseconds as a cue timestamp.
func stamp(ms int64, sep string) string {
	return subs.FormatStamp(time.Duration(ms)*time.Millisecond, sep)
}
