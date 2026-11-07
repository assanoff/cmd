// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package seek

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assanoff/cmd/ai/internal/index"
)

// fixture is an index of one recording on disk, and a server over it whose
// "model" understands only taxes.
func fixture(t *testing.T) (http.Handler, index.Media) {
	t.Helper()
	dir := t.TempDir()

	media := filepath.Join(dir, "ep 12.mp4")
	if err := os.WriteFile(media, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := index.Open(filepath.Join(dir, "seek.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	segs := []index.Segment{
		{Start: 0, End: 70 * time.Second, Text: "вступление и <b>новости</b>"},
		{Start: 70 * time.Second, End: 140 * time.Second, Text: "рассказывали про налоги"},
	}
	chunks := []index.Chunk{
		{Start: 0, End: 70 * time.Second, Text: segs[0].Text, Vec: []float32{0, 1}},
		{Start: 70 * time.Second, End: 140 * time.Second, Text: segs[1].Text, Vec: []float32{1, 0}},
	}
	m := index.Media{Path: media, Title: "ep 12", Duration: 140 * time.Second}
	err = idx.Put(context.Background(), m, segs, chunks, chunks, "test")
	if err != nil {
		t.Fatal(err)
	}
	m, _, err = idx.Lookup(context.Background(), media)
	if err != nil {
		t.Fatal(err)
	}

	return newWeb(idx, taxes{}, 5), m
}

func get(t *testing.T, h http.Handler, target string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://localhost:8782"+target, nil)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// htmx gets the results alone; a reload of the URL htmx pushed gets the whole
// page with the same results in it.
func TestSearchFragmentAndPage(t *testing.T) {
	h, m := fixture(t)
	link := fmt.Sprintf(`href="/media/%d?t=70"`, m.ID)

	w := get(t, h, "/search?q=налоги", "HX-Request", "true")
	if w.Code != http.StatusOK {
		t.Fatalf("fragment: %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if strings.Contains(body, "<html") || !strings.Contains(body, link) || !strings.Contains(body, "00:01:10") {
		t.Errorf("fragment: want the hit at 00:01:10 and no page around it:\n%s", body)
	}

	body = get(t, h, "/search?q=налоги").Body.String()
	if !strings.Contains(body, "<html") || !strings.Contains(body, link) {
		t.Errorf("page: want a whole page with the hit in it:\n%s", body)
	}

	body = get(t, h, "/search?q=налоги", "HX-Request", "true", "HX-History-Restore-Request", "true").Body.String()
	if !strings.Contains(body, "<html") {
		t.Error("history restore: want the whole page")
	}
}

func TestHomeListsFiles(t *testing.T) {
	h, m := fixture(t)
	body := get(t, h, "/").Body.String()
	if !strings.Contains(body, fmt.Sprintf(`href="/media/%d"`, m.ID)) || !strings.Contains(body, "00:02:20") {
		t.Errorf("home: want the file and its length:\n%s", body)
	}
}

func TestMediaPage(t *testing.T) {
	h, m := fixture(t)
	w := get(t, h, fmt.Sprintf("/media/%d?t=70", m.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{
		fmt.Sprintf(`<video id="player" controls preload="metadata" src="/file/%d#t=70">`, m.ID),
		"рассказывали про налоги",
		"&lt;b&gt;новости&lt;/b&gt;", // transcript text is text, not markup
		"mark( 70 , true)",           // html/template pads a number in a script
	} {
		if !strings.Contains(body, want) {
			t.Errorf("media page lacks %q:\n%s", want, body)
		}
	}

	if w := get(t, h, "/media/999"); w.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d, want 404", w.Code)
	}
	if w := get(t, h, "/media/x"); w.Code != http.StatusNotFound {
		t.Errorf("bad id: %d, want 404", w.Code)
	}
}

// The player seeks with range requests; a server that ignored them would make
// every seek download the file up to that point.
func TestFileServesRanges(t *testing.T) {
	h, m := fixture(t)
	w := get(t, h, fmt.Sprintf("/file/%d", m.ID), "Range", "bytes=2-4")
	if w.Code != http.StatusPartialContent || w.Body.String() != "234" {
		t.Errorf("range: %d %q, want 206 \"234\"", w.Code, w.Body)
	}

	if err := os.Remove(m.Path); err != nil {
		t.Fatal(err)
	}
	if w := get(t, h, fmt.Sprintf("/file/%d", m.ID)); w.Code != http.StatusNotFound {
		t.Errorf("a file gone from disk: %d, want 404", w.Code)
	}
}

func TestLocalOnly(t *testing.T) {
	h, _ := fixture(t)
	for host, want := range map[string]int{
		"localhost:8782": http.StatusOK,
		"127.0.0.1:8782": http.StatusOK,
		"[::1]:8782":     http.StatusOK,
		"evil.example":   http.StatusForbidden,
		"192.168.1.5:80": http.StatusForbidden,
		"localhost.evil": http.StatusForbidden,
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %s: %d, want %d", host, w.Code, want)
		}
	}
}

func TestStaticHtmx(t *testing.T) {
	h, _ := fixture(t)
	w := get(t, h, "/static/htmx.min.js")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "htmx") {
		t.Errorf("htmx: %d", w.Code)
	}
}
