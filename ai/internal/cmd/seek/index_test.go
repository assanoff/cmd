// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package seek

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/index"
)

// taxes stands in for a model that understands one topic: every query is
// about taxes, and a text is near it exactly when it mentions them.
type taxes struct{}

func (taxes) EmbedQuery(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, nil
}

func (taxes) EmbedDocs(_ context.Context, texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i, t := range texts {
		vecs[i] = []float32{0, 1}
		if strings.Contains(t, "налог") {
			vecs[i] = []float32{1, 0}
		}
	}
	return vecs, nil
}

// A recording with its transcript beside it goes into the index without
// whisper, and a search lands on the remark, not on the start of the minute
// it was said in.
func TestIndexFromSidecar(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	media := filepath.Join(dir, "ep.mp3")
	var srt strings.Builder
	for i := range 24 {
		text := fmt.Sprintf("фраза %d.", i)
		if i == 7 {
			text = "рассказали про налоги."
		}
		fmt.Fprintf(&srt, "%d\n00:00:%02d,000 --> 00:00:%02d,000\n%s\n\n", i+1, 5*i/2, 5*(i+1)/2, text)
	}
	if err := os.WriteFile(media, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ep.srt"), []byte(srt.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, err := index.Open(filepath.Join(dir, "seek.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	info, err := os.Stat(media)
	if err != nil {
		t.Fatal(err)
	}
	r := &run{
		c:     &indexCommand{seek: New(ctx)},
		ctx:   ctx,
		p:     cli.NewPrinter(Name, true),
		idx:   idx,
		emb:   taxes{},
		model: "test",
	}
	if err := r.one(file{path: media, info: info, sidecar: sidecar(media)}); err != nil {
		t.Fatal(err)
	}

	hits, err := search(ctx, idx, taxes{}, "про налоги", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("nothing found")
	}
	// The cues are two and a half seconds each, so the remark is said at 17s,
	// and the piece holding it is the second: 10s to 20s.
	h := hits[0]
	if h.Start != 0 || h.At != 10*time.Second || !strings.Contains(h.Quote, "налоги") {
		t.Errorf("got start %v, at %v, quote %q; want the piece from 10s", h.Start, h.At, h.Quote)
	}
}
