// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"
)

// Exit codes. Scripts need to tell "you called me wrong" apart from a runtime
// failure, so these are part of the interface and must stay stable.
const (
	exitError = 1
	exitUsage = 2
)

// maxBody caps what one request may send. Anything larger is cut off and the
// record says so, which is better than holding an endless upload in memory.
const maxBody = 64 << 20

// bom is the UTF-8 byte order mark, spelled out because Go source may not
// carry one literally.
const bom = "\xef\xbb\xbf"

var (
	flagAddr    = flag.String("a", "127.0.0.1:8099", "address to listen on")
	flagOut     = flag.String("o", "", "also append every request to this file as a JSON line")
	flagStatus  = flag.Int("s", http.StatusOK, "status code to answer with")
	flagBody    = flag.String("b", `{"ok":true}`, "body to answer with")
	flagVersion = flag.Bool("version", false, "print the version")
)

func usage() {
	fmt.Fprint(os.Stderr, `usage: reqlog [-a addr] [-o file] [-s status] [-b body]

An HTTP endpoint that answers every request the same way and prints what came
in: method, path, headers and the body, with JSON indented.

    reqlog                          # http://127.0.0.1:8099, answers 200
    reqlog -o sent.jsonl            # also keep every request in a file
    reqlog -s 500                   # see how the sender retries

options:
`)
	flag.PrintDefaults()
	os.Exit(exitUsage)
}

func main() {
	log.SetPrefix("reqlog: ")
	log.SetFlags(0)
	flag.Usage = usage
	flag.Parse()

	if *flagVersion {
		printVersion()
		return
	}
	if flag.NArg() > 0 {
		usage()
	}
	// WriteHeader panics outside this range, on the first request rather
	// than at startup, so it is checked here.
	if *flagStatus < 100 || *flagStatus > 999 {
		log.Printf("-s %d: not an HTTP status code", *flagStatus)
		os.Exit(exitUsage)
	}

	if err := run(); err != nil {
		log.Print(err)
		os.Exit(exitError)
	}
}

func run() error {
	rc := &recorder{
		term:   os.Stdout,
		status: *flagStatus,
		reply:  []byte(*flagBody),
		now:    time.Now,
	}
	if *flagOut != "" {
		f, err := os.OpenFile(*flagOut, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		// Every line is written whole as it arrives, so there is nothing
		// buffered for Close to lose.
		defer f.Close() //nolint:errcheck // see above
		rc.file = f
	}

	ln, err := net.Listen("tcp", *flagAddr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: rc, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx) //nolint:errcheck // Serve reports the outcome
	}()

	log.Printf("listening on http://%s, answering %d", ln.Addr(), rc.status)
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// recorder answers every request with the same reply and records it.
type recorder struct {
	term   io.Writer // the human-readable copy
	file   io.Writer // the JSON lines; nil without -o
	status int
	reply  []byte
	now    func() time.Time

	// mu keeps concurrent requests from interleaving their output and
	// numbers them in the order they were recorded.
	mu sync.Mutex
	n  int
}

// record is one request, as written to the -o file.
type record struct {
	N       int         `json:"n"`
	Time    time.Time   `json:"time"`
	Remote  string      `json:"remote"`
	Method  string      `json:"method"`
	Host    string      `json:"host"`
	URL     string      `json:"url"`
	Headers http.Header `json:"headers"`
	Body    string      `json:"body"`
	Error   string      `json:"error,omitempty"`
}

func (rc *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	rec := record{
		Time:    rc.now(),
		Remote:  r.RemoteAddr,
		Method:  r.Method,
		Host:    r.Host,
		URL:     r.URL.RequestURI(),
		Headers: r.Header,
		Body:    string(body),
	}
	if err != nil {
		rec.Error = "reading the body: " + err.Error()
	}

	rc.mu.Lock()
	rc.n++
	rec.N = rc.n
	rc.print(rec)
	saveErr := rc.save(rec)
	rc.mu.Unlock()
	if saveErr != nil {
		log.Printf("writing %s: %v", *flagOut, saveErr)
	}

	ct := "text/plain; charset=utf-8"
	if json.Valid(rc.reply) {
		ct = "application/json; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(rc.status)
	w.Write(rc.reply) //nolint:errcheck // the sender hung up; nothing to do
}

func (rc *recorder) print(rec record) {
	var b strings.Builder
	fmt.Fprintf(&b, "── #%d %s  %s %s%s  %d bytes from %s\n",
		rec.N, rec.Time.Format("2006-01-02 15:04:05.000"),
		rec.Method, rec.Host, rec.URL, len(rec.Body), rec.Remote)
	for _, k := range slices.Sorted(maps.Keys(rec.Headers)) {
		for _, v := range rec.Headers[k] {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	if rec.Body != "" {
		b.WriteString("\n")
		b.WriteString(pretty(rec.Body))
		b.WriteString("\n")
	}
	if rec.Error != "" {
		fmt.Fprintf(&b, "!! %s\n", rec.Error)
	}
	b.WriteString("\n")
	io.WriteString(rc.term, b.String()) //nolint:errcheck // a closed terminal
}

func (rc *recorder) save(rec record) error {
	if rc.file == nil {
		return nil
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = rc.file.Write(append(line, '\n'))
	return err
}

// pretty indents a JSON body and returns anything else unchanged. A leading
// byte order mark is dropped first: json.Indent rejects it, and 1C writes one.
func pretty(body string) string {
	var buf bytes.Buffer
	trimmed := strings.TrimPrefix(body, bom)
	if err := json.Indent(&buf, []byte(trimmed), "", "  "); err != nil {
		return body
	}
	return buf.String()
}

func printVersion() {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Println("reqlog (devel)")
		return
	}
	fmt.Printf("reqlog %s\n", bi.Main.Version)
	fmt.Printf("go     %s\n", bi.GoVersion)
}
