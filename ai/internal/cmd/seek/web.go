// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package seek

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/index"
)

// htmx is served from the binary rather than a CDN: the page has to work on a
// machine with no network, which is the machine the models run on.
//
//go:embed static
var static embed.FS

//go:embed page.html
var pageHTML string

var pages = template.Must(template.New("").Parse(pageHTML))

type webCommand struct {
	seek *Command

	Addr string `short:"a" long:"addr" default:"localhost:8782" value-name:"HOST:PORT" description:"where to serve"`
}

// Execute serves until interrupted. The URL goes to standard output, so a
// script that starts the server can read where it is.
func (c *webCommand) Execute(args []string) error {
	if len(args) > 0 {
		return cli.Usagef("web takes no arguments")
	}
	ctx := c.seek.ctx
	p := cli.NewPrinter(Name, c.seek.Quiet)

	idx, err := c.seek.open()
	if err != nil {
		return err
	}
	defer func() { _ = idx.Close() }()

	emb, err := c.seek.embedder(idx, p)
	if err != nil {
		return err
	}
	defer emb.Close()

	ln, err := net.Listen("tcp", c.Addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           newWeb(idx, emb, c.seek.Limit),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Printf("http://%s/\n", ln.Addr())

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(stop)
}

// web serves the search page, the page of one file with its player and
// transcript, and the file itself.
type web struct {
	idx   *index.Index
	emb   embedder
	limit int
}

func newWeb(idx *index.Index, emb embedder, limit int) http.Handler {
	s := &web{idx: idx, emb: emb, limit: limit}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.search)
	mux.HandleFunc("GET /search", s.search)
	mux.HandleFunc("GET /media/{id}", s.media)
	mux.HandleFunc("GET /file/{id}", s.file)
	mux.Handle("GET /static/", http.FileServerFS(static))
	return localOnly(mux)
}

// localOnly lets through only requests for a local host. The server hands out
// any file the index names, so a page elsewhere must not reach it through a
// name that resolves to this machine (DNS rebinding).
func localOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		host = strings.Trim(host, "[]")
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "ai seek web answers only to localhost", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

type searchView struct {
	Query string
	Hits  []hitView
	Files []fileView // with no query: everything indexed
	Error string
}

type hitView struct {
	MediaID int64
	Title   string
	Path    string
	Sec     int64
	Clock   string
	Text    string
}

type fileView struct {
	ID       int64
	Title    string
	Duration string
}

// search answers the page and the query. htmx asks for the results alone and
// gets the fragment; anything else — a reload, a shared link, a browser with
// scripts off — gets the whole page, with the results already in it.
func (s *web) search(w http.ResponseWriter, r *http.Request) {
	v := searchView{Query: strings.TrimSpace(r.FormValue("q"))}
	if v.Query == "" {
		ms, err := s.idx.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, m := range ms {
			v.Files = append(v.Files, fileView{ID: m.ID, Title: m.Title, Duration: clock(m.Duration)})
		}
	} else if err := s.find(r.Context(), &v); err != nil {
		v.Error = err.Error()
	}

	name := "page"
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true" {
		name = "results"
	}
	w.Header().Set("Vary", "HX-Request")
	render(w, name, v)
}

func (s *web) find(ctx context.Context, v *searchView) error {
	hits, err := search(ctx, s.idx, s.emb, v.Query, s.limit)
	if err != nil {
		return err
	}
	for _, h := range hits {
		v.Hits = append(v.Hits, hitView{
			MediaID: h.MediaID,
			Title:   h.Title,
			Path:    h.Path,
			Sec:     int64(h.At / time.Second),
			Clock:   clock(h.At),
			Text:    h.Quote,
		})
	}
	return nil
}

type mediaView struct {
	ID       int64
	Title    string
	Path     string
	Duration string
	Video    bool
	Start    float64
	Segments []segmentView
}

type segmentView struct {
	Sec   int64
	Clock string
	Text  string
}

// media is the page of one file: a player that opens at ?t=, in seconds, and
// the whole transcript, each line a link to its moment.
func (s *web) media(w http.ResponseWriter, r *http.Request) {
	m, ok := s.lookup(w, r)
	if !ok {
		return
	}
	segs, err := s.idx.Transcript(r.Context(), m.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	v := mediaView{
		ID:       m.ID,
		Title:    m.Title,
		Path:     m.Path,
		Duration: clock(m.Duration),
		Video:    isVideo(m.Path),
	}
	if t, err := strconv.ParseFloat(r.FormValue("t"), 64); err == nil && t > 0 {
		v.Start = t
	}
	for _, sg := range segs {
		v.Segments = append(v.Segments, segmentView{
			Sec:   int64(sg.Start / time.Second),
			Clock: clock(sg.Start),
			Text:  sg.Text,
		})
	}
	render(w, "media", v)
}

// file serves the media itself. ServeContent answers range requests, which is
// what lets the player seek without downloading everything before the point.
func (s *web) file(w http.ResponseWriter, r *http.Request) {
	m, ok := s.lookup(w, r)
	if !ok {
		return
	}
	f, err := os.Open(m.Path)
	if err != nil {
		http.Error(w, "the file is no longer there: "+m.Path, http.StatusNotFound)
		return
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, filepath.Base(m.Path), info.ModTime(), f)
}

// lookup finds the file named by the {id} of the request, and answers the
// request itself when there is none.
func (s *web) lookup(w http.ResponseWriter, r *http.Request) (index.Media, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return index.Media{}, false
	}
	m, err := s.idx.Get(r.Context(), id)
	switch {
	case errors.Is(err, os.ErrNotExist):
		http.NotFound(w, r)
		return index.Media{}, false
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return index.Media{}, false
	}
	return m, true
}

func render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		// The status line is gone by now; all that is left is to say so on
		// the page, and if even that fails the client has gone.
		_, _ = fmt.Fprintf(w, "<p>%s</p>", template.HTMLEscapeString(err.Error()))
	}
}

// isVideo says whether a file gets a video player rather than an audio one.
// A video in an audio element still plays its sound, so a wrong guess costs
// the picture, never the search result.
func isVideo(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v", ".mkv", ".mov", ".webm", ".avi", ".wmv", ".flv", ".mpg", ".mpeg":
		return true
	}
	return false
}
