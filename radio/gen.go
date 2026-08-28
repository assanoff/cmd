// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func genCommand() *command {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)

	var (
		baseURL  = fs.String("u", "", "public base URL for the links in the feed; defaults to this machine's address on the network")
		prefix   = fs.String("prefix", "", "URL path the collection is served under (default \"/radio\")")
		portFlag = fs.String("p", "", "port the feed URLs point at (default 8080)")
		category = fs.String("category", "", "iTunes category (default \"Music\")")
		dry      = fs.Bool("n", false, "print what would be written and exit")
		quiet    = fs.Bool("q", false, "suppress progress on standard error")
	)

	return &command{
		name:  "gen",
		usage: "gen [-u url] [-p port] [-prefix path] [-category name] [-n] [-q] [dir]",
		short: "Generate podcast feeds from a directory of episodes",
		flags: fs,
		run: func(ctx context.Context, args []string) error {
			if len(args) > 1 {
				return usagef("gen takes one directory at most")
			}
			root := "."
			if len(args) == 1 {
				root = args[0]
			}

			if *portFlag != "" {
				// The port belongs to one setting shared with serve, and the
				// environment is how it reaches both.
				os.Setenv("RADIO_PORT", *portFlag)
			}

			casts, err := discover(root)
			if err != nil {
				return err
			}

			var failed bool
			for _, p := range casts {
				s := settingsFor(p.dir, p.name)
				if *baseURL != "" {
					s.baseURL = strings.TrimRight(*baseURL, "/")
					s.link = s.baseURL
				}
				if *prefix != "" {
					s.prefix = normalizePrefix(*prefix)
				}
				if *category != "" {
					s.category = *category
				}

				if *dry {
					describe(p, s)
					continue
				}

				feedPath, n, err := generate(p, s, false, *quiet)
				if err != nil {
					fmt.Fprintf(os.Stderr, "radio: %s: %v\n", p.name, err)
					failed = true
					continue
				}
				say(*quiet, "%s: %d episodes at %s", p.name, n, strings.TrimRight(s.baseURL, "/")+s.prefix+"/"+pathEscape(p.name)+"/feed.xml")
				fmt.Println(feedPath)
			}

			if failed {
				return fmt.Errorf("one or more collections failed")
			}
			return nil
		},
	}
}

func describe(p podcast, s settings) {
	feedPath, n, err := generate(p, s, true, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "radio: %s: %v\n", p.name, err)
		return
	}
	fmt.Printf("collection %s\n", p.name)
	fmt.Printf("directory  %s\n", p.dir)
	fmt.Printf("episodes   %d\n", n)
	fmt.Printf("title      %s\n", s.title)
	fmt.Printf("author     %s\n", s.author)
	fmt.Printf("category   %s\n", s.category)
	fmt.Printf("base url   %s\n", strings.TrimRight(s.baseURL, "/")+s.prefix+"/"+pathEscape(p.name))
	fmt.Printf("feed       %s\n", feedPath)
	fmt.Println()
}

// discover works out what the given directory is.
//
// A directory holding .pl files is one podcast — that is the case when you are
// standing in a finished mix and want a feed for it. A directory whose
// children hold them is a collection of podcasts, which is how an archive of
// several stations is laid out. Anything else is a mistake worth naming.
func discover(root string) ([]podcast, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, usagef("%s is not a directory", root)
	}

	if hasEpisodes(abs) {
		return []podcast{{dir: abs, name: filepath.Base(abs)}}, nil
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}

	var casts []podcast
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(abs, e.Name())
		if hasEpisodes(dir) {
			casts = append(casts, podcast{dir: dir, name: e.Name()})
		}
	}
	if len(casts) == 0 {
		return nil, fmt.Errorf("no .pl episode files in %s or its subdirectories", root)
	}
	return casts, nil
}

func hasEpisodes(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".pl") {
			return true
		}
	}
	return false
}
