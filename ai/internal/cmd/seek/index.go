// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package seek

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/assanoff/cmd/ai/internal/asr"
	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/config"
	"github.com/assanoff/cmd/ai/internal/index"
	"github.com/assanoff/cmd/ai/internal/llm"
	"github.com/assanoff/cmd/ai/internal/subs"
	"github.com/assanoff/cmd/ai/internal/walk"
)

// The passages a transcript is cut into; see index.Windows.
const (
	window  = time.Minute
	overlap = 15 * time.Second
)

// pinSpan is the size of the pieces a transcript is also cut into, and so how
// close to a remark a hit lands; see index.Search. A whisper segment alone is
// often half a sentence, too little to compare with a query; ten seconds is a
// sentence or two. The pieces do not overlap, so each remark is in exactly one
// and no two pieces tie over it.
const pinSpan = 10 * time.Second

// embedBatch is how many texts go to the model in one call. One call per text
// pays the per-call overhead hundreds of times for a long recording; the whole
// recording in one call is a batch the context window may not hold.
const embedBatch = 16

// indexCommand transcribes and indexes media files.
//
// The --ext default is the media set ai hear accepts. As there, it only
// filters directory walks; a file named on the command line is indexed
// whatever it is called.
type indexCommand struct {
	seek *Command

	Model   string `short:"m" long:"model" value-name:"NAME" description:"the embedding model: the embed role, or an explicit id"`
	ASR     string `long:"asr" value-name:"NAME" description:"the speech model for a file with no transcript beside it: a role (asr, asr-fast) or a whisper model"`
	Lang    string `short:"l" long:"lang" value-name:"CODE" description:"language hint for whisper, such as ru; empty autodetects"`
	Threads int    `long:"threads" value-name:"N" description:"CPU threads for whisper; defaults to $AI_ASR_THREADS, else every core but two"`
	Exts    string `long:"ext" default:"mp3,mp4,wav,m4a,flac,ogg,opus,mkv,avi,mov,wmv,flv,webm,m4v,mpg,mpeg,aac" description:"comma-separated extensions to accept when walking directories"`
	Recurse bool   `short:"r" long:"recurse" description:"descend into subdirectories of directory arguments"`
	Force   bool   `long:"force" description:"index again the files that are indexed and unchanged"`
	DryRun  bool   `short:"n" long:"dry-run" description:"print the plan and exit without loading a model"`

	Args struct {
		Files []string `positional-arg-name:"file" required:"1" description:"media files or directories"`
	} `positional-args:"yes"`
}

// file is one input that needs indexing.
type file struct {
	path    string // absolute: the index is shared by every directory
	info    os.FileInfo
	sidecar string // the transcript beside it, or ""
}

func (c *indexCommand) Execute([]string) error {
	ctx := c.seek.ctx
	p := cli.NewPrinter(Name, c.seek.Quiet)

	jobs, err := cli.Jobs(walk.Request{
		Args:    c.Args.Files,
		Recurse: c.Recurse,
		Exts:    c.Exts,
		Report:  p.Printf,
	})
	if err != nil {
		return err
	}

	idx, err := index.Open(c.seek.path())
	if err != nil {
		return err
	}
	defer func() { _ = idx.Close() }()

	// Before anything is loaded: an index built with another model cannot
	// take these vectors, and finding that out after an hour of whisper would
	// throw the hour away.
	model := config.Resolve(c.Model, config.RoleEmbed)
	have, _, err := idx.Model(ctx)
	if err != nil {
		return err
	}
	if have != "" && have != model {
		return cli.Usagef("%s was built with %s, and %s makes vectors it cannot compare: "+
			"index into another --db, or point the embed role back", c.seek.path(), have, model)
	}

	files, err := c.plan(ctx, idx, jobs, p)
	if err != nil {
		return err
	}
	if c.DryRun {
		c.describe(files, model)
		return nil
	}
	if len(files) == 0 {
		return nil
	}

	emb, err := llm.New(ctx, model, p.Logger(), p)
	if err != nil {
		return err
	}
	defer emb.Close()

	r := &run{c: c, ctx: ctx, p: p, idx: idx, emb: emb, model: model}
	defer r.close()

	todo := make([]walk.Job, len(files))
	byPath := make(map[string]file, len(files))
	for i, f := range files {
		todo[i] = walk.Job{In: f.path}
		byPath[f.path] = f
	}
	return cli.Each(ctx, p, todo, func(j walk.Job) error {
		return r.one(byPath[j.In])
	})
}

// plan drops the inputs that are indexed already and have not changed since,
// which is what makes re-running over a growing directory cheap.
func (c *indexCommand) plan(ctx context.Context, idx *index.Index, jobs []walk.Job, p *cli.Printer) ([]file, error) {
	var files []file
	for _, j := range jobs {
		if j.In == "-" {
			return nil, cli.Usagef("index reads files, not standard input")
		}
		path, err := filepath.Abs(j.In)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}

		m, ok, err := idx.Lookup(ctx, path)
		if err != nil {
			return nil, err
		}
		if ok && !c.Force && m.Size == info.Size() && m.ModTime.Equal(info.ModTime()) {
			p.Printf("skip %s: indexed and unchanged (use --force)", j.In)
			continue
		}
		files = append(files, file{path: path, info: info, sidecar: sidecar(path)})
	}
	return files, nil
}

// describe prints what a run would do. This is the payload of -n, so it goes
// to standard output.
func (c *indexCommand) describe(files []file, model string) {
	fmt.Printf("index   %s\n", c.seek.path())
	fmt.Printf("embed   %s\n", model)
	fmt.Printf("speech  %s\n", config.Resolve(c.ASR, config.RoleASR))
	for _, f := range files {
		from := "whisper"
		if f.sidecar != "" {
			from = f.sidecar
		}
		fmt.Printf("%s <- %s\n", f.path, from)
	}
	fmt.Printf("%d file(s)\n", len(files))
}

// sidecar finds the transcript ai hear -f srt (or -f vtt) left beside a file.
// A transcript already made is hours of whisper not spent again, and may be a
// better one than this run would make: from large-v3-turbo, or corrected by
// hand.
func sidecar(path string) string {
	base := strings.TrimSuffix(path, filepath.Ext(path))
	for _, ext := range []string{".srt", ".vtt"} {
		if _, err := os.Stat(base + ext); err == nil {
			return base + ext
		}
	}
	return ""
}

// docEmbedder is what indexing needs of a model: an *llm.Engine, or a
// stand-in in a test.
type docEmbedder interface {
	EmbedDocs(ctx context.Context, texts []string) ([][]float32, error)
}

// run is the state one index run shares across its files: the models stay
// loaded from the first file to the last.
type run struct {
	c     *indexCommand
	ctx   context.Context
	p     *cli.Printer
	idx   *index.Index
	emb   docEmbedder
	model string

	// whisper is loaded the first time a file has no transcript beside it,
	// and never when every file has one.
	whisper *asr.Engine
}

func (r *run) close() {
	if r.whisper != nil {
		r.whisper.Close()
	}
}

func (r *run) one(f file) error {
	segs, lang, err := r.transcript(f)
	if err != nil {
		return err
	}
	passages := index.Windows(segs, window, overlap)
	if len(passages) == 0 {
		return fmt.Errorf("no speech in %s", f.path)
	}
	pieces := index.Windows(segs, pinSpan, 0)

	// One pass over both, so a short file fills a batch rather than two half
	// ones.
	all := slices.Concat(passages, pieces)
	texts := make([]string, len(all))
	for i, c := range all {
		texts[i] = c.Text
	}
	vecs, err := embedAll(r.ctx, r.emb, texts)
	if err != nil {
		return err
	}
	for i := range passages {
		passages[i].Vec = vecs[i]
	}
	for i := range pieces {
		pieces[i].Vec = vecs[len(passages)+i]
	}

	m := index.Media{
		Path:     f.path,
		Title:    strings.TrimSuffix(filepath.Base(f.path), filepath.Ext(f.path)),
		Size:     f.info.Size(),
		ModTime:  f.info.ModTime(),
		Duration: segs[len(segs)-1].End,
		Lang:     lang,
	}
	if err := r.idx.Put(r.ctx, m, segs, passages, pieces, r.model); err != nil {
		return err
	}
	r.p.Printf("%s: %d passages, %s", f.path, len(passages), clock(m.Duration))
	return nil
}

// embedAll embeds texts embedBatch at a time.
func embedAll(ctx context.Context, emb docEmbedder, texts []string) ([][]float32, error) {
	vecs := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += embedBatch {
		v, err := emb.EmbedDocs(ctx, texts[i:min(i+embedBatch, len(texts))])
		if err != nil {
			return nil, err
		}
		vecs = append(vecs, v...)
	}
	return vecs, nil
}

// transcript reads the transcript beside the file, or makes one.
func (r *run) transcript(f file) ([]index.Segment, string, error) {
	if f.sidecar != "" {
		r.p.Printf("%s: transcript from %s", f.path, filepath.Base(f.sidecar))
		text, err := os.ReadFile(f.sidecar)
		if err != nil {
			return nil, "", err
		}
		segs := segments(string(text))
		if len(segs) == 0 {
			return nil, "", fmt.Errorf("%s has no timed cues", f.sidecar)
		}
		return segs, "", nil
	}

	if r.whisper == nil {
		threads := r.c.Threads
		if threads <= 0 {
			threads = config.ASRThreads(r.p.Printf)
		}
		eng, err := asr.New(r.ctx, config.Resolve(r.c.ASR, config.RoleASR), threads, r.p.Logger(), r.p)
		if err != nil {
			return nil, "", err
		}
		r.whisper = eng
	}

	r.p.Printf("%s: transcribing", f.path)
	o := asr.Options{Language: cmp.Or(r.c.Lang, config.Value("AI_LANG"))}
	if !r.p.Quiet() {
		// Whisper says nothing until it is finished; see ai hear.
		o.Waiting = func(elapsed, audio time.Duration) {
			r.p.Printf("still decoding: %s so far, %s of audio", elapsed, audio.Round(time.Second))
		}
	}
	tr, err := r.whisper.TranscribePath(r.ctx, f.path, o)
	if err != nil {
		return nil, "", err
	}

	segs := make([]index.Segment, 0, len(tr.Segments))
	for _, s := range tr.Segments {
		segs = append(segs, index.Segment{
			Start: time.Duration(s.StartMs) * time.Millisecond,
			End:   time.Duration(s.EndMs) * time.Millisecond,
			Text:  strings.TrimSpace(s.Text),
		})
	}
	return segs, tr.Language, nil
}

// segments reads a SubRip or WebVTT transcript. A cue without readable times
// cannot be placed in the recording, so it is left out rather than guessed.
func segments(text string) []index.Segment {
	_, cues := subs.Parse(text)
	var segs []index.Segment
	for _, c := range cues {
		start, ok1 := c.Start()
		end, ok2 := c.End()
		if ok1 && ok2 {
			segs = append(segs, index.Segment{Start: start, End: end, Text: strings.TrimSpace(c.Text)})
		}
	}
	return segs
}
