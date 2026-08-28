// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// chapter is one entry of a tracklist.
type chapter struct {
	start int64 // seconds from the beginning of the file
	title string
}

// The shapes a timestamp comes in. A tracklist is as often typed by hand into
// a video description as it is generated, so both ends of the line are worth
// looking at: "0:00 Artist — Track" and "Artist - Track 7:45" are equally
// common in the wild.
var (
	timeFirst = regexp.MustCompile(`^[\[(]?(?:[0-9]+:)?[0-9]{1,2}:[0-9]{2}[\])]?`)
	timeLast  = regexp.MustCompile(`[\[(]?(?:[0-9]+:)?[0-9]{1,2}:[0-9]{2}[\])]?$`)

	// An index in front of the time: "01. 4:31 ..." or "1) 4:31 ...".
	indexPrefix = regexp.MustCompile(`^[0-9]{1,3}[.)][ \t]+`)

	linkAnywhere = regexp.MustCompile(`https?://[^ \t]+`)
	leadJunk     = regexp.MustCompile(`^[ \t\-–—:.)\]]+`)
	trailJunk    = regexp.MustCompile(`[ \t\-–—:.(\[]+$`)
	runsOfSpace  = regexp.MustCompile(`[ \t]+`)
)

// parseTracklist pulls every line that reads like a track out of arbitrary
// text and ignores the rest.
//
// That tolerance is the whole point. A DJ set on YouTube almost always carries
// its tracklist in the description, surrounded by prose, links and thanks, and
// being able to paste the description whole is the difference between a
// command you use and one you talk yourself out of using.
func parseTracklist(text string) []chapter {
	var out []chapter

	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimSuffix(sc.Text(), "\r"))
		if line == "" {
			continue
		}
		line = indexPrefix.ReplaceAllString(line, "")

		sec := int64(-1)
		title := ""

		if m := timeFirst.FindString(line); m != "" {
			if s, ok := toSeconds(stripBrackets(m)); ok {
				sec, title = s, line[len(m):]
			}
		}
		if sec < 0 {
			if loc := timeLast.FindStringIndex(line); loc != nil {
				if s, ok := toSeconds(stripBrackets(line[loc[0]:loc[1]])); ok {
					sec, title = s, line[:loc[0]]
				}
			}
		}
		if sec < 0 {
			continue
		}

		title = cleanTitle(title)
		if title == "" {
			continue
		}
		out = append(out, chapter{start: sec, title: title})
	}

	return out
}

// parsePL reads the descriptor format radio and mix use, whose first four
// lines are a title, a blank, a description and a blank.
//
// Skipping the preamble matters: a description like "Recorded live at 21:30"
// would otherwise parse as a chapter, because it ends in something that looks
// exactly like a timestamp.
func parsePL(text string) []chapter {
	lines := strings.Split(text, "\n")
	if len(lines) <= 4 {
		return nil
	}
	return parseTracklist(strings.Join(lines[4:], "\n"))
}

// order sorts the chapters and drops any that do not move forward. A repeated
// or out-of-order timestamp makes a zero-length chapter, which players render
// as a glitch rather than as an error, so it is better lost loudly here.
func order(chs []chapter, quiet bool) []chapter {
	sort.SliceStable(chs, func(i, j int) bool { return chs[i].start < chs[j].start })

	var out []chapter
	for i, c := range chs {
		if i > 0 && c.start <= out[len(out)-1].start {
			if !quiet {
				fmt.Fprintf(os.Stderr, "chapters: skipping out-of-order %q at %s\n", c.title, hms(c.start))
			}
			continue
		}
		out = append(out, c)
	}
	return out
}

func stripBrackets(s string) string {
	return strings.NewReplacer("[", "", "]", "", "(", "", ")", "").Replace(s)
}

// toSeconds reads "MM:SS" or "H:MM:SS".
func toSeconds(ts string) (int64, bool) {
	parts := strings.Split(ts, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var total int64
	for _, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n < 0 {
			return 0, false
		}
		total = total*60 + n
	}
	return total, true
}

func cleanTitle(t string) string {
	t = linkAnywhere.ReplaceAllString(t, "") // a trailing link is not a title
	t = leadJunk.ReplaceAllString(t, "")
	t = trailJunk.ReplaceAllString(t, "")
	t = runsOfSpace.ReplaceAllString(t, " ")
	return strings.TrimSpace(t)
}

func hms(sec int64) string {
	return fmt.Sprintf("%d:%02d:%02d", sec/3600, (sec%3600)/60, sec%60)
}

// writePL writes the descriptor radio reads, so a tracklist pasted from a
// video description can go straight into a podcast feed.
func writePL(path, title string, chs []chapter) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s\n\n%d tracks\n\n", title, len(chs))
	for i, c := range chs {
		fmt.Fprintf(&b, "%d. %s %s\n", i+1, hms(c.start), c.title)
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}
