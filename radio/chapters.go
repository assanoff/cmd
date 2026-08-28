// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"os"
)

// The Podcast Index chapter document.
// https://github.com/Podcastindex-org/podcast-namespace/blob/main/chapters/jsonChapters.md
type chaptersDoc struct {
	Version  string        `json:"version"`
	Title    string        `json:"title,omitempty"`
	Img      string        `json:"img,omitempty"`
	Chapters []chapterItem `json:"chapters"`
}

type chapterItem struct {
	StartTime int64  `json:"startTime"`
	Title     string `json:"title"`
	URL       string `json:"url,omitempty"`
	Img       string `json:"img,omitempty"`
}

// writeChaptersJSON writes the sidecar file the feed's <podcast:chapters>
// element points at.
//
// This is the published chapter list, not the one inside the audio file.
// Embedding chapters into the mp3 itself belongs to whoever produced it — the
// mix already carries them — so nothing here rewrites the media.
func writeChaptersJSON(path string, ep episode, imgURL string) error {
	// An episode with no tracklist still gets a document, with an empty list
	// rather than a null: a client reading "chapters": null is entitled to
	// treat the file as malformed.
	doc := chaptersDoc{
		Version:  "1.2.0",
		Title:    ep.title,
		Img:      imgURL,
		Chapters: []chapterItem{},
	}
	for _, t := range ep.tracks {
		doc.Chapters = append(doc.Chapters, chapterItem{
			StartTime: t.startSec,
			Title:     t.label(),
			URL:       t.url,
			Img:       imgURL,
		})
	}

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
