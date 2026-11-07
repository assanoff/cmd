// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package index

import (
	"strings"
	"time"
)

// Windows cuts a transcript into windows of about size, each opening overlap
// before the previous one closes.
//
// A whisper segment is a few seconds, too little to say what it is about, and
// a whole recording is far too much to put in one vector. A minute of speech
// is a thought, and is what a person searching remembers. The overlap is for
// the thought that falls on an edge: without it, a remark split between two
// windows is half in each and found in neither.
//
// The edges fall between segments, never inside one, so a window still knows
// exactly when it starts. A segment longer than size gets a window of its own.
func Windows(segs []Segment, size, overlap time.Duration) []Chunk {
	var spoken []Segment
	for _, s := range segs {
		if s.Text = strings.TrimSpace(s.Text); s.Text != "" {
			spoken = append(spoken, s)
		}
	}

	step := size - overlap
	var out []Chunk
	for i := 0; i < len(spoken); {
		j := i + 1
		for j < len(spoken) && spoken[j].End-spoken[i].Start <= size {
			j++
		}
		out = append(out, window(spoken[i:j]))
		if j == len(spoken) {
			break
		}

		// The next window opens at the first segment step or more after this
		// one opened, and at the latest where this one closed.
		k := i + 1
		for k < j && spoken[k].Start-spoken[i].Start < step {
			k++
		}
		i = k
	}
	return out
}

func window(segs []Segment) Chunk {
	text := make([]string, len(segs))
	for i, s := range segs {
		text[i] = s.Text
	}
	return Chunk{
		Start: segs[0].Start,
		End:   segs[len(segs)-1].End,
		Text:  strings.Join(text, " "),
	}
}
