// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func serveCommand() *command {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)

	var (
		addr     = fs.String("a", "", "bind address; the default listens on every interface")
		portFlag = fs.String("p", "", "port to listen on (default 8080)")
		prefix   = fs.String("prefix", "", "URL path the collection is served under (default \"/radio\")")
		quiet    = fs.Bool("q", false, "do not log requests")
	)

	return &command{
		name:  "serve",
		usage: "serve [-a addr] [-p port] [-prefix path] [-q] [dir]",
		short: "Serve a directory of episodes over HTTP",
		flags: fs,
		run: func(ctx context.Context, args []string) error {
			if len(args) > 1 {
				return usagef("serve takes one directory at most")
			}
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			if *portFlag != "" {
				os.Setenv("RADIO_PORT", *portFlag)
			}
			if *prefix != "" {
				os.Setenv("RADIO_PREFIX", *prefix)
			}

			casts, err := discover(root)
			if err != nil {
				return err
			}

			abs, err := filepath.Abs(root)
			if err != nil {
				return err
			}

			// Where the file tree hangs in URL space has to match what gen
			// wrote into the feeds, or every enclosure 404s. gen names a
			// podcast after its own directory, so serving a single podcast
			// means mounting it one level deeper than serving a shelf of them.
			mount := prefixSetting()
			if hasEpisodes(abs) {
				mount += "/" + pathEscape(filepath.Base(abs))
			}
			mount += "/"

			bind := *addr
			if bind == "" {
				bind = ":" + port()
			}

			// Take the port before announcing anything. ListenAndServe would
			// let the feed addresses reach standard output and only then fail
			// to bind, which reads as success to whatever consumed them.
			ln, err := net.Listen("tcp", bind)
			if err != nil {
				return err
			}

			srv := &http.Server{
				Handler:           handler(abs, mount, casts, *quiet),
				ReadHeaderTimeout: 10 * time.Second,
			}

			for _, p := range casts {
				fmt.Println(feedURL(p, mount))
			}
			say(*quiet, "serving %s on %s", abs, bind)

			errCh := make(chan error, 1)
			go func() { errCh <- srv.Serve(ln) }()

			select {
			case err := <-errCh:
				if errors.Is(err, http.ErrServerClosed) {
					return nil
				}
				return err
			case <-ctx.Done():
				say(*quiet, "shutting down")
				stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return srv.Shutdown(stopCtx)
			}
		},
	}
}

// feedURL is the address to paste into a podcast client.
func feedURL(p podcast, mount string) string {
	base := strings.TrimRight(defaultBaseURL(""), "/")
	if v := globalValue("RADIO_BASE_URL"); v != "" {
		base = strings.TrimRight(v, "/")
	}
	path := mount
	if !strings.HasSuffix(path, pathEscape(p.name)+"/") {
		path += pathEscape(p.name) + "/"
	}
	return base + path + "feed.xml"
}

func handler(dir, mount string, casts []podcast, quiet bool) http.Handler {
	mux := http.NewServeMux()

	files := http.StripPrefix(mount, http.FileServer(http.Dir(dir)))

	// Content types decide whether a client parses a file or downloads it. Go
	// serves .xml as text/xml and .json as application/json, and a podcast
	// client wants neither: it looks for an RSS type on the feed and for the
	// chapters media type on the sidecar. Everything else the standard
	// sniffing gets right, including audio, which also needs the range
	// requests http.FileServer already handles — that is what makes seeking
	// inside an episode work.
	mux.Handle(mount, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "feed.xml"):
			w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		case strings.HasSuffix(r.URL.Path, ".chapters.json"):
			w.Header().Set("Content-Type", "application/json+chapters; charset=utf-8")
		case strings.HasSuffix(r.URL.Path, ".md"):
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		case strings.HasSuffix(r.URL.Path, ".scr"), strings.HasSuffix(r.URL.Path, ".pl"):
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
		files.ServeHTTP(w, r)
	}))

	// The root exists so the address of the feed can be read off a phone
	// instead of copied from a terminal.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "radio, serving %s\n\n", dir)
		for _, p := range casts {
			fmt.Fprintf(w, "%s\n", feedURL(p, mount))
		}
	})

	if quiet {
		return mux
	}
	return logging(mux)
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		fmt.Fprintf(os.Stderr, "radio: %s %s from %s\n", r.Method, r.URL.Path, host)
		next.ServeHTTP(w, r)
	})
}
