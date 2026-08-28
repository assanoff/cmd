// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// track is a single entry of an episode's tracklist.
type track struct {
	num      int
	startSec int64
	artist   string
	name     string
	url      string
}

// label is how a track is named in a chapter list.
func (t track) label() string {
	if t.name == "" {
		return t.artist
	}
	return t.artist + " — " + t.name
}

// episode is one .pl file: the descriptor that turns an audio file into a
// podcast episode.
type episode struct {
	title  string
	desc   string
	tracks []track
}

// parsePL reads an episode descriptor:
//
//	line 1   title
//	line 2   blank
//	line 3   description
//	line 4   blank
//	line 5+  tracklist, "NN. H:MM:SS Artist — Track"
//
// The four-line preamble is not decoration. Lines are read by position, so a
// file that opens with the title and goes straight to the tracks loses its
// first track to the description slot and its second to the blank line.
func parsePL(path string) (episode, error) {
	f, err := os.Open(path)
	if err != nil {
		return episode{}, err
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return episode{}, err
	}

	var ep episode
	if len(lines) >= 1 {
		ep.title = strings.TrimSpace(lines[0])
	}
	if len(lines) >= 3 {
		ep.desc = strings.TrimSpace(lines[2])
	}
	if len(lines) >= 5 {
		for _, line := range lines[4:] {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if t := parseTrackLine(line); t != nil {
				ep.tracks = append(ep.tracks, *t)
			}
		}
	}
	return ep, nil
}

// parseTrackLine reads "01. 0:00:00 Trouble Funk — Pump Me Up", with the index
// and the trailing URL both optional. A line it does not recognize is dropped
// rather than guessed at, so a stray note in the tracklist costs a chapter and
// not the whole file.
func parseTrackLine(line string) *track {
	rest := line

	var num int
	if idx := strings.Index(rest, ". "); idx > 0 && idx <= 3 {
		if n, err := strconv.Atoi(rest[:idx]); err == nil {
			num = n
			rest = rest[idx+2:]
		}
	}

	fields := strings.SplitN(rest, " ", 2)
	if len(fields) < 2 {
		return nil
	}
	sec, ok := parseTimestamp(fields[0])
	if !ok {
		return nil
	}

	remaining := fields[1]
	var urlStr string
	if strings.Contains(remaining, "http://") || strings.Contains(remaining, "https://") {
		parts := strings.Fields(remaining)
		if len(parts) > 0 {
			last := parts[len(parts)-1]
			if strings.HasPrefix(last, "http") {
				urlStr = last
				remaining = strings.TrimSpace(strings.Join(parts[:len(parts)-1], " "))
			}
		}
	}

	var artist, name string
	if parts := strings.SplitN(remaining, " — ", 2); len(parts) == 2 {
		artist = strings.TrimSpace(parts[0])
		name = strings.TrimSpace(parts[1])
	} else if parts := strings.SplitN(remaining, " - ", 2); len(parts) == 2 {
		artist = strings.TrimSpace(parts[0])
		name = strings.TrimSpace(parts[1])
	} else {
		artist = strings.TrimSpace(remaining)
	}

	return &track{num: num, startSec: sec, artist: artist, name: name, url: urlStr}
}

// parseTimestamp reads "H:MM:SS", "HH:MM:SS" or "MM:SS" as seconds.
func parseTimestamp(s string) (int64, bool) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	nums := make([]int64, len(parts))
	for i, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return 0, false
		}
		nums[i] = n
	}
	if len(nums) == 3 {
		return nums[0]*3600 + nums[1]*60 + nums[2], true
	}
	return nums[0]*60 + nums[1], true
}

// formatTimestamp writes seconds as HH:MM:SS, with two-digit hours because
// that is what Apple Podcasts expects in a chapter list.
func formatTimestamp(sec int64) string {
	return fmt.Sprintf("%02d:%02d:%02d", sec/3600, (sec%3600)/60, sec%60)
}

// formatShortDuration writes seconds as M:SS or H:MM:SS, unpadded, for
// itunes:duration.
func formatShortDuration(sec int64) string {
	if h := sec / 3600; h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, (sec%3600)/60, sec%60)
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}
