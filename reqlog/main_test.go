// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newRecorder(status int, reply string) (*recorder, *bytes.Buffer, *bytes.Buffer) {
	var term, file bytes.Buffer
	rc := &recorder{
		term:   &term,
		file:   &file,
		status: status,
		reply:  []byte(reply),
		now: func() time.Time {
			return time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
		},
	}
	return rc, &term, &file
}

// A 1C JSON body: the byte order mark in front and Cyrillic inside. The
// terminal gets it indented, the file gets it exactly as sent.
func TestRecordsJSONBody(t *testing.T) {
	rc, term, file := newRecorder(http.StatusOK, `{"ok":true}`)
	sent := "\uFEFF" + `{"order_id":"Заказ 1","items":[{"n":1}]}`
	req := httptest.NewRequest(http.MethodPost,
		"http://shop.example/local/scripts/1c/add.php?x=1", strings.NewReader(sent))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	rc.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK || resp.Body.String() != `{"ok":true}` {
		t.Errorf("answered %d %q", resp.Code, resp.Body.String())
	}
	if ct := resp.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", ct)
	}

	out := term.String()
	for _, want := range []string{
		"── #1 2026-11-14 10:00:00.000  POST shop.example/local/scripts/1c/add.php?x=1",
		"Content-Type: application/json\n",
		"{\n  \"order_id\": \"Заказ 1\",\n  \"items\": [\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("terminal output lacks %q:\n%s", want, out)
		}
	}

	var rec record
	if err := json.Unmarshal(file.Bytes(), &rec); err != nil {
		t.Fatalf("file line: %v\n%s", err, file.String())
	}
	if rec.N != 1 || rec.Method != http.MethodPost || rec.Body != sent {
		t.Errorf("file record = %+v", rec)
	}
	if rec.URL != "/local/scripts/1c/add.php?x=1" || rec.Host != "shop.example" {
		t.Errorf("file record URL = %q, host = %q", rec.URL, rec.Host)
	}
}

// A non-JSON body is printed as is, the configured status is returned, and
// requests are numbered one after another.
func TestPlainBodyAndStatus(t *testing.T) {
	rc, term, file := newRecorder(http.StatusInternalServerError, "boom")
	for range 2 {
		req := httptest.NewRequest(http.MethodPut, "/x", strings.NewReader("a=1&b=2"))
		resp := httptest.NewRecorder()
		rc.ServeHTTP(resp, req)
		if resp.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", resp.Code)
		}
		if ct := resp.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("Content-Type = %q, want plain text", ct)
		}
	}
	if out := term.String(); !strings.Contains(out, "\na=1&b=2\n") ||
		!strings.Contains(out, "── #2 ") {
		t.Errorf("terminal output:\n%s", out)
	}
	if lines := strings.Count(file.String(), "\n"); lines != 2 {
		t.Errorf("file has %d lines, want 2", lines)
	}
}

func TestPretty(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":1}`:      "{\n  \"a\": 1\n}",
		"\uFEFF[1]":    "[\n  1\n]",
		"not json":     "not json",
		"\uFEFFbroken": "\uFEFFbroken",
	} {
		if got := pretty(in); got != want {
			t.Errorf("pretty(%q) = %q, want %q", in, got, want)
		}
	}
}
