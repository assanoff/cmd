// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"strings"
)

// A cue is one subtitle entry: everything before the text, then the text. The
// header is carried through untouched, which is the whole point of -keep-format
// — timings are data, not prose, and a model asked to translate them will
// eventually round one.
type cue struct {
	header []string // index line and timing line for SubRip, timing line for WebVTT
	text   string   // the spoken text, lines joined by a space
}

// subtitles reports whether text looks like SubRip or WebVTT. Detection is by
// content rather than by file extension so that a piped stream works too.
func subtitles(text string) bool {
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

// parseSubtitles splits a subtitle file into cues plus whatever preamble came
// before the first one, such as the WEBVTT line.
func parseSubtitles(text string) (preamble []string, cues []cue) {
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

		cues = append(cues, cue{header: header, text: strings.Join(body, " ")})
	}

	return preamble, cues
}

// renderSubtitles puts a subtitle file back together from its cues.
func renderSubtitles(preamble []string, cues []cue) string {
	var sb strings.Builder

	for _, line := range preamble {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	if len(preamble) > 0 {
		sb.WriteString("\n")
	}

	for _, c := range cues {
		for _, line := range c.header {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		sb.WriteString(strings.TrimSpace(c.text))
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
