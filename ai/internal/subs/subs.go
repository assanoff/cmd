// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package subs reads and writes SubRip and WebVTT subtitles.
//
// Subtitles are how a timestamp travels through the pipeline. hear writes
// plain text without times and SubRip with them; tr translates the words and
// leaves the timings alone; sum reads the file back as timestamped prose,
// which is what makes a chapter list say where each chapter starts.
//
// sum and tr used to hold two copies of the parser, and ai-sum's copy said so
// in a comment: "if the cue format handling changes there, it changes here
// too". It never did. This is that one copy.
package subs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cue is one subtitle entry: everything before the text, then the text. The
// header is carried through untouched, which is the whole point of translating
// with --keep-format — timings are data, not prose, and a model asked to
// translate them will eventually round one.
type Cue struct {
	Header []string // index line and timing line for SubRip, timing line for WebVTT
	Text   string   // the spoken text, lines joined by a space
}

// Is reports whether text looks like SubRip or WebVTT. Detection is by content
// rather than by file extension so that a piped stream works too: hear writes
// SubRip to standard output and sum has to recognize it there, where there is
// no file name to look at.
func Is(text string) bool {
	if strings.HasPrefix(strings.TrimSpace(text), "WEBVTT") {
		return true
	}
	// SubRip has no magic line, so look for its timing arrow near the start.
	head := text
	if len(head) > 4096 {
		head = head[:4096]
	}
	return strings.Contains(head, "-->")
}

// Parse splits a subtitle file into cues plus whatever preamble came before
// the first one, such as the WEBVTT line.
func Parse(text string) (preamble []string, cues []Cue) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	i := 0
	for ; i < len(lines); i++ {
		if isTiming(lines[i]) {
			break
		}
	}
	// Everything above the first timing line is preamble, except an index line
	// belonging to that first cue.
	end := i
	if end > 0 && isIndex(lines[end-1]) {
		end--
	}
	preamble = trimTrailingBlank(lines[:end])

	for i = end; i < len(lines); {
		var header []string

		if isIndex(lines[i]) {
			header = append(header, lines[i])
			i++
		}
		if i >= len(lines) || !isTiming(lines[i]) {
			// Not a cue after all; skip the line rather than lose it silently.
			if i < len(lines) && strings.TrimSpace(lines[i]) != "" {
				preamble = append(preamble, lines[i])
			}
			i++
			continue
		}
		header = append(header, lines[i])
		i++

		var body []string
		for ; i < len(lines) && strings.TrimSpace(lines[i]) != ""; i++ {
			body = append(body, lines[i])
		}
		for ; i < len(lines) && strings.TrimSpace(lines[i]) == ""; i++ {
		}

		cues = append(cues, Cue{Header: header, Text: strings.Join(body, " ")})
	}

	return preamble, cues
}

// Render puts a subtitle file back together from its cues.
func Render(preamble []string, cues []Cue) string {
	var sb strings.Builder

	for _, line := range preamble {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	if len(preamble) > 0 {
		sb.WriteString("\n")
	}

	for _, c := range cues {
		for _, line := range c.Header {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		sb.WriteString(strings.TrimSpace(c.Text))
		sb.WriteString("\n\n")
	}

	return sb.String()
}

// isTiming recognizes both SubRip and WebVTT timing lines by their arrow.
func isTiming(line string) bool {
	return strings.Contains(line, "-->")
}

// isIndex recognizes a SubRip cue number: a line of digits and nothing else.
func isIndex(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	for _, r := range line {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func trimTrailingBlank(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// Start reads the cue's begin time out of its timing line.
func (c Cue) Start() (time.Duration, bool) {
	for _, line := range c.Header {
		if !isTiming(line) {
			continue
		}
		field, _, _ := strings.Cut(line, "-->")
		return ParseStamp(strings.TrimSpace(field))
	}
	return 0, false
}

// ParseStamp reads HH:MM:SS,mmm and the WebVTT spellings of the same thing:
// the separator may be a comma or a period, the milliseconds may be missing,
// and the hours may be missing.
func ParseStamp(s string) (time.Duration, bool) {
	if s == "" {
		return 0, false
	}
	s = strings.ReplaceAll(s, ",", ".")

	clock, frac, _ := strings.Cut(s, ".")
	fields := strings.Split(clock, ":")
	if len(fields) < 2 || len(fields) > 3 {
		return 0, false
	}

	var total time.Duration
	for _, f := range fields {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n < 0 {
			return 0, false
		}
		total = total*60 + time.Duration(n)*time.Second
	}

	if frac != "" {
		// Pad or trim to exactly three digits, so "5" reads as 500ms.
		for len(frac) < 3 {
			frac += "0"
		}
		if n, err := strconv.Atoi(frac[:3]); err == nil {
			total += time.Duration(n) * time.Millisecond
		}
	}
	return total, true
}

// FormatStamp formats a duration as a cue timing: HH:MM:SS<sep>mmm. SubRip
// puts a comma before the fraction, WebVTT a period.
//
// It lives beside the parser on purpose. hear writes these files and tr and
// sum read them back, so the two halves of the format have to agree; they
// agree by being one package.
func FormatStamp(d time.Duration, sep string) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%02d:%02d:%02d%s%03d",
		int(d/time.Hour),
		int(d%time.Hour/time.Minute),
		int(d%time.Minute/time.Second),
		sep,
		int(d%time.Second/time.Millisecond),
	)
}

// Stamp formats a duration the way the marker appears in prose.
func Stamp(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d / time.Second)
	return fmt.Sprintf("[%02d:%02d:%02d]", s/3600, s/60%60, s%60)
}

// StampedParagraphs turns cues back into prose: one paragraph per interval of
// recording, each opening with the time it starts at.
//
// A paragraph is the unit here because chunk.Split breaks on blank lines. That
// keeps a marker glued to the text it introduces no matter how the document is
// later chunked or folded — which is the only reason the times survive several
// rounds of compression.
//
// With every == 0 the times are dropped and the result is plain prose.
func StampedParagraphs(cues []Cue, every time.Duration) string {
	var (
		paras   []string
		current []string
		openAt  time.Duration
		haveAt  bool
	)

	flush := func() {
		if len(current) == 0 {
			return
		}
		body := strings.Join(current, " ")
		if every > 0 && haveAt {
			body = Stamp(openAt) + " " + body
		}
		paras = append(paras, body)
		current = nil
		haveAt = false
	}

	for _, c := range cues {
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		at, ok := c.Start()

		// A cue with no readable time cannot open a paragraph, but it can
		// still join the one in progress: losing the words would be worse
		// than losing one marker.
		if every > 0 && ok {
			switch {
			case !haveAt:
				openAt, haveAt = at, true
			case at-openAt >= every:
				flush()
				openAt, haveAt = at, true
			}
		}
		current = append(current, text)
	}
	flush()

	return strings.Join(paras, "\n\n")
}
