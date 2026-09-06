// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// Exit codes. Scripts need to tell "you called me wrong" apart from a runtime
// failure, so these are part of the interface and must stay stable.
const (
	exitError = 1
	exitUsage = 2
)

var (
	flagTitle   = flag.String("t", "", "set the file's title; without it the existing one is left alone")
	flagCover   = flag.String("i", "", "cover image to attach")
	flagList    = flag.String("l", "", "file to read the tracklist from")
	flagPL      = flag.String("p", "", "also write a normalized .pl descriptor here")
	flagOut     = flag.String("o", "", "write the result here instead of into the audio file")
	flagDry     = flag.Bool("n", false, "print the chapters and exit without writing")
	flagQuiet   = flag.Bool("q", false, "suppress progress on standard error")
	flagVersion = flag.Bool("version", false, "print the version")
)

// The extensions treated as audio when walking a directory.
var audioExts = []string{".mp3", ".m4a", ".m4b", ".aac", ".wav", ".flac", ".ogg", ".opus", ".wma"}

func init() {
	flag.StringVar(flagTitle, "title", "", "alias for -t")
	flag.StringVar(flagCover, "image", "", "alias for -i")
	flag.BoolVar(flagDry, "dry", false, "alias for -n")
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: chapters [options] <audio> [tracklist]
       chapters [options] <dir>

Writes a timecoded tracklist into an audio file as real chapters, so players
show the names on the scrubber and can seek between them.

    chapters set.mp3 set.pl                 # tracklist from the .pl beside it
    chapters set.mp3                        # same, found automatically
    yt-dlp --get-description URL | chapters set.mp3
    chapters ./mixes                        # every file, each with its own .pl

Any line carrying a timestamp is read as a track and everything else is
ignored, so pasting a whole video description works:

    0:00 Artist - Track
    1. 04:31 Artist — Track
    [1:02:14] Artist – Track
    Artist - Track 7:45

The audio is stream-copied, never re-encoded. Read the result back with
    ffprobe -show_chapters -of default=nw=1 file.mp3

options:
`)
	flag.PrintDefaults()
	os.Exit(exitUsage)
}

func main() {
	log.SetPrefix("chapters: ")
	log.SetFlags(0)
	flag.Usage = usage
	flag.Parse()

	if *flagVersion {
		printVersion()
		return
	}

	if err := run(); err != nil {
		log.Print(err)
		os.Exit(exitCode(err))
	}
}

type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

func usagef(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

func exitCode(err error) int {
	var ue usageError
	if errors.As(err, &ue) {
		return exitUsage
	}
	return exitError
}

func run() error {
	args := flag.Args()
	if len(args) == 0 || len(args) > 2 {
		usage()
	}

	target := args[0]
	list := *flagList
	if len(args) == 2 {
		if list != "" {
			return usagef("give the tracklist either with -l or as an argument, not both")
		}
		list = args[1]
	}

	info, err := os.Stat(target)
	if err != nil {
		return err
	}

	if info.IsDir() {
		if list != "" || *flagOut != "" || *flagPL != "" || *flagTitle != "" {
			return usagef("-l, -o, -p and -t name a single file; with a directory each one is found beside its audio")
		}
		return runDir(target)
	}
	return runFile(target, list, *flagOut, *flagPL, *flagTitle)
}

// runDir does a whole folder, each file with the .pl lying beside it. Files
// without one are left alone rather than guessed at.
func runDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	var done, skipped int
	var failed bool
	for _, e := range entries {
		if e.IsDir() || !isAudio(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		sidecar := stem(path) + ".pl"
		if _, err := os.Stat(sidecar); err != nil {
			say("%s: no tracklist beside it, leaving it alone", e.Name())
			skipped++
			continue
		}
		if err := runFile(path, sidecar, "", "", ""); err != nil {
			fmt.Fprintf(os.Stderr, "chapters: %s: %v\n", e.Name(), err)
			failed = true
			continue
		}
		done++
	}

	say("%d written, %d left alone", done, skipped)
	if failed {
		return errors.New("one or more files failed")
	}
	if done == 0 && skipped == 0 {
		return fmt.Errorf("no audio files in %s", dir)
	}
	return nil
}

func runFile(audio, list, out, plOut, title string) error {
	text, source, err := tracklist(audio, list)
	if err != nil {
		return err
	}

	var chs []chapter
	if strings.EqualFold(filepath.Ext(source), ".pl") {
		chs = parsePL(text)
	} else {
		chs = parseTracklist(text)
	}
	chs = order(chs, *flagQuiet)
	if len(chs) == 0 {
		return fmt.Errorf("no track lines found in %s", source)
	}

	if *flagCover != "" {
		if _, err := os.Stat(*flagCover); err != nil {
			return err
		}
	}

	// A descriptor needs a name for its first line, so -p falls back to the
	// file's. Writing that same fallback into the audio would be a different
	// matter: it would replace whatever title the file already has, on every
	// run, with something no better than the name it is stored under.
	plTitle := title
	if plTitle == "" {
		plTitle = filepath.Base(stem(audio))
	}

	if *flagDry {
		fmt.Printf("audio      %s\n", audio)
		fmt.Printf("tracklist  %s\n", source)
		if title != "" {
			fmt.Printf("title      %s\n", title)
		} else {
			fmt.Printf("title      (unchanged)\n")
		}
		if *flagCover != "" {
			fmt.Printf("cover      %s\n", *flagCover)
		}
		fmt.Printf("chapters   %d\n\n", len(chs))
		for _, c := range chs {
			fmt.Printf("%9s  %s\n", hms(c.start), c.title)
		}
		return nil
	}

	sec, err := durationSec(audio)
	if err != nil {
		return err
	}
	meta := ffmetadata(chs, int64(sec*1000))

	say("writing %d chapters into %s", len(chs), filepath.Base(audio))

	// -o naming the input itself is the in-place case, however it was spelled:
	// embed would hand ffmpeg the same file as source and destination, and the
	// mix would be gone. Route it through the temporary instead of refusing —
	// "put the chapters into this file" is what the caller meant — but say so,
	// because it is not what -o normally does.
	dest := out
	switch {
	case dest == "" || sameFile(dest, audio):
		if dest != "" {
			say("-o names the input itself — writing in place")
		}
		if err := writeInPlace(audio, *flagCover, title, meta); err != nil {
			return err
		}
		dest = audio
	default:
		if err := embed(audio, dest, *flagCover, title, meta); err != nil {
			return err
		}
	}

	fmt.Println(dest)

	if plOut != "" {
		if err := writePL(plOut, plTitle, chs); err != nil {
			return err
		}
		fmt.Println(plOut)
	}
	return nil
}

// tracklist finds the text to read chapters from, and says where it came from.
//
// Named file, then standard input, then the .pl beside the audio. The pipe
// outranks the sibling deliberately: piping a description in is a thing
// someone chose to do, while a .pl lying next to the file is often just
// left over from an earlier run, and preferring it would quietly write the old
// tracklist and report success.
func tracklist(audio, list string) (text, source string, err error) {
	if list != "" {
		b, err := os.ReadFile(list)
		if err != nil {
			return "", "", err
		}
		return string(b), list, nil
	}

	if b, ok := readStdin(); ok {
		return string(b), "standard input", nil
	}

	sidecar := stem(audio) + ".pl"
	if b, err := os.ReadFile(sidecar); err == nil {
		return string(b), sidecar, nil
	}
	return "", "", usagef("no tracklist: name one, put a .pl beside the audio, or pipe it in")
}

// readStdin reports a piped tracklist.
//
// Only a real pipe or a redirected file counts. "Not a terminal" is the
// tempting test and it is wrong: standard input can also be a socket inherited
// from whatever started the process, and that never reaches end of file, so
// reading it hangs forever with no output to explain why. The shell version
// this replaces used that test and could hang the same way.
//
// An empty pipe does not count either, so `chapters set.mp3` still finds the
// .pl beside the audio when it runs under cron, where standard input is
// /dev/null.
func readStdin() ([]byte, bool) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return nil, false
	}
	mode := info.Mode()
	if mode&os.ModeNamedPipe == 0 && !mode.IsRegular() {
		return nil, false
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return nil, false
	}
	return b, true
}

func stem(path string) string {
	return strings.TrimSuffix(path, filepath.Ext(path))
}

func isAudio(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range audioExts {
		if ext == e {
			return true
		}
	}
	return false
}

func say(format string, args ...any) {
	if *flagQuiet {
		return
	}
	fmt.Fprintf(os.Stderr, "chapters: "+format+"\n", args...)
}

func printVersion() {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Println("chapters (devel)")
		return
	}
	fmt.Printf("chapters %s\n", bi.Main.Version)
	fmt.Printf("go       %s\n", bi.GoVersion)
}
