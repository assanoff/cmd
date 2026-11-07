// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package seek finds the places in audio and video files where something was
// said: it indexes their transcripts once, by meaning and by words, and
// answers a query with files and times.
package seek

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/config"
	"github.com/assanoff/cmd/ai/internal/index"
	"github.com/assanoff/cmd/ai/internal/llm"
)

// Name is how this subcommand labels its own progress.
const Name = "ai seek"

const (
	Short = "Find where something was said in audio and video"
	Long  = `Finds the places in audio and video files where something was said, by
meaning as well as by words:

    ai seek index -r ~/podcasts       transcribe and index, once
    ai seek "talked about taxes"      the places, best first
    ai seek "про налоги" -f json      the same for a script
    ai seek web                       the same in a browser, with a player

index takes the transcript beside a file when there is one — the .srt or .vtt
"ai hear -f srt" writes — and runs whisper when there is not. Each file is cut
into minute-long overlapping passages, and each passage is embedded by the
embed role's model and stored with its words in one SQLite file: --db, else
AI_SEEK_DB, else ~/.local/share/ai/seek.db.

A search embeds the query with the model the index was built with, and merges
the passages nearest to it with those that share its words: the vector finds
what was meant, the words find the names and terms. The time of each answer is
not the start of its minute but the ten seconds in it nearest the query. A
query that is also the name of a subcommand goes after --: ai seek -- web.`
)

// Command is the subcommand group. Given no subcommand of its own it searches,
// which is what "ai seek <query>" reads as. Its flags are the group's, so the
// nested subcommands take them too: "ai seek ls -f json", "ai seek web --db x".
type Command struct {
	ctx context.Context

	DB     string `long:"db" value-name:"FILE" description:"the index; defaults to $AI_SEEK_DB, else ~/.local/share/ai/seek.db"`
	Limit  int    `short:"n" long:"limit" default:"10" value-name:"N" description:"how many places a search returns"`
	Format string `short:"f" long:"format" default:"text" choice:"text" choice:"json" description:"output format"` //nolint:staticcheck // go-flags takes one choice tag per allowed value; SA5008 reads the repeat as a mistake
	Quiet  bool   `short:"q" long:"quiet" description:"suppress progress on standard error"`

	Index  *indexCommand  `command:"index" description:"Transcribe and index media files"`
	Web    *webCommand    `command:"web" description:"Search in a browser, and play what was found"`
	List   *listCommand   `command:"ls" description:"List the indexed files"`
	Remove *removeCommand `command:"rm" description:"Drop files from the index"`
}

// New returns the group with every nested subcommand bound to the run's
// context and to the group's own flags.
func New(ctx context.Context) *Command {
	c := &Command{ctx: ctx}
	c.Index = &indexCommand{seek: c}
	c.Web = &webCommand{seek: c}
	c.List = &listCommand{seek: c}
	c.Remove = &removeCommand{seek: c}
	return c
}

// Execute searches.
func (c *Command) Execute(args []string) error {
	q := strings.TrimSpace(strings.Join(args, " "))
	if q == "" {
		return cli.Usagef("give a query, or one of index, web, ls, rm")
	}
	if c.Limit < 1 {
		return cli.Usagef("--limit must be at least 1")
	}

	p := cli.NewPrinter(Name, c.Quiet)
	idx, err := c.open()
	if err != nil {
		return err
	}
	defer func() { _ = idx.Close() }()

	emb, err := c.embedder(idx, p)
	if err != nil {
		return err
	}
	defer emb.Close()

	hits, err := search(c.ctx, idx, emb, q, c.Limit)
	if err != nil {
		return err
	}

	if c.Format == "json" {
		return printJSON(os.Stdout, hits)
	}
	return printText(os.Stdout, hits)
}

// path is where the index lives.
func (c *Command) path() string {
	return cmp.Or(c.DB, config.Value("AI_SEEK_DB"), filepath.Join(config.DataDir(), "ai", "seek.db"))
}

// open opens an index that has to exist already. Only index creates one: a
// mistyped --db given to a search should say so, not answer with an empty
// file it just made.
func (c *Command) open() (*index.Index, error) {
	path := c.path()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no index at %s: \"ai seek index\" makes one", path)
	}
	return index.Open(path)
}

// embedder is what a search needs of a model: an *llm.Engine, or a stand-in
// in a test.
type embedder interface {
	EmbedQuery(ctx context.Context, q string) ([]float32, error)
}

// search answers a query for the command line and the web alike.
func search(ctx context.Context, idx *index.Index, emb embedder, q string, limit int) ([]index.Hit, error) {
	vec, err := emb.EmbedQuery(ctx, q)
	if err != nil {
		return nil, err
	}
	return idx.Search(ctx, vec, q, limit)
}

// embedder loads the model the index was built with — not the one the embed
// role names now. A query embedded by another model lands in another space,
// and the nearest passages there are noise; if the role has moved on, the
// index still answers the way it was built to until it is rebuilt.
func (c *Command) embedder(idx *index.Index, p *cli.Printer) (*llm.Engine, error) {
	model, _, err := idx.Model(c.ctx)
	if err != nil {
		return nil, err
	}
	if model == "" {
		return nil, fmt.Errorf("%s is empty: \"ai seek index\" fills it", c.path())
	}
	return llm.New(c.ctx, model, p.Logger(), p)
}

// snippetRunes is how much of a quote a line of text output shows. A quote is
// two segments, which is usually less; the cap is for the monologue whisper
// left in one piece. The JSON carries all of it.
const snippetRunes = 300

func printText(w io.Writer, hits []index.Hit) error {
	for _, h := range hits {
		_, err := fmt.Fprintf(w, "%s  %s\n    %s\n\n", clock(h.At), h.Path, snippet(h.Quote, snippetRunes))
		if err != nil {
			return err
		}
	}
	return nil
}

// The JSON shape is ours, not the index's: these field names are what scripts
// parse, so they must not drift when the index changes its struct.
//
// at is the moment to play from, and quote what is said there; start_ms,
// end_ms and text are the whole passage it was found in. similarity is the
// cosine similarity of the query and the quote: the hits listed are those
// within 85% of the best one's.
type jsonHit struct {
	Path    string  `json:"path"`
	Title   string  `json:"title"`
	At      string  `json:"at"`
	AtMs    int64   `json:"at_ms"`
	Quote   string  `json:"quote"`
	StartMs int64   `json:"start_ms"`
	EndMs   int64   `json:"end_ms"`
	Text    string  `json:"text"`
	Score   float64 `json:"score"`
	Sim     float64 `json:"similarity"`
}

func printJSON(w io.Writer, hits []index.Hit) error {
	// Never null: a script reads "nothing found" as an empty list.
	out := make([]jsonHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, jsonHit{
			Path:    h.Path,
			Title:   h.Title,
			At:      clock(h.At),
			AtMs:    h.At.Milliseconds(),
			Quote:   h.Quote,
			StartMs: h.Start.Milliseconds(),
			EndMs:   h.End.Milliseconds(),
			Text:    h.Text,
			Score:   h.Score,
			Sim:     h.Similarity,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// clock formats a time in a recording the way players take it: mpv --start,
// ffmpeg -ss and a YouTube link all read 01:23:45.
func clock(d time.Duration) string {
	s := int(d / time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, s/60%60, s%60)
}

// snippet shortens text to about n runes, at a word boundary.
func snippet(text string, n int) string {
	r := []rune(text)
	if len(r) <= n {
		return text
	}
	cut := string(r[:n])
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	}
	return cut + " …"
}
