// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// The pages are templates over the same queries as the commands, so what
// is worth testing is that each one renders at all — a template fails only
// when it runs — and that the links between them lead somewhere.

func TestWeb(t *testing.T) {
	files := map[string][]byte{
		"a/Photos/x.jpg": []byte("one"),
		"b/Photos/x.jpg": []byte("one"),
		"a/v.bin":        noise(6, 300<<10),
		"b/w.bin":        noise(6, 300<<10),
		"a/only.txt":     []byte("a size of its own"),
	}
	// More groups than a page shows, for the "more" button.
	for i := range pageSize + 5 {
		data := fmt.Appendf(nil, "copy %d", i)
		files[fmt.Sprintf("c/%d.txt", i)] = data
		files[fmt.Sprintf("d/%d.txt", i)] = data
	}
	root := tree(t, files)
	db := filepath.Join(t.TempDir(), "dupes.db")
	scan(t, db, root)
	sql, err := openDB(db, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sql.Close() }()
	srv := httptest.NewServer((&server{db: sql, path: db}).handler())
	defer srv.Close()

	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, tc := range []struct {
		path string
		want []string
	}{
		{"/", []string{"Overview", filepath.Join(a, "v.bin"), filepath.Join(b, "w.bin")}},
		{"/files", []string{"57 groups", "Show more", `hx-get="/files/more?offset=50"`}},
		{"/files?by=name&ext=jpg", []string{"1 groups", filepath.Join(a, "Photos", "x.jpg")}},
		{"/files?min=ten", []string{`class="error"`}},
		{"/files/more?offset=50", []string{`<span class="rank">51</span>`}},
		{"/dirs", []string{filepath.Join(a, "Photos"), filepath.Join(b, "Photos")}},
		{"/dirs?by=name&similar=0.9", []string{"Photos", "100% alike"}},
		{"/dir?path=" + url.QueryEscape(a), []string{
			"Photos", "only.txt", "unique", `hx-get="/copies?path=`,
		}},
		{"/copies?path=" + url.QueryEscape(filepath.Join(a, "v.bin")), []string{filepath.Join(b, "w.bin")}},
		{"/copies?dir=" + url.QueryEscape(filepath.Join(a, "Photos")), []string{filepath.Join(b, "Photos")}},
		{"/files/export?f=csv", []string{"group,key,count,wasted,path,size,modified"}},
		{"/dirs/export?f=json", []string{`"dirs": [`}},
		{"/htmx.min.js", []string{`"4.0.1"`}},
	} {
		body, code := get(t, srv.URL+tc.path, "")
		if code != http.StatusOK {
			t.Errorf("GET %s: %d\n%s", tc.path, code, body)
			continue
		}
		body = html.UnescapeString(body)
		for _, w := range tc.want {
			if !strings.Contains(body, w) {
				t.Errorf("GET %s has no %q", tc.path, w)
			}
		}
	}

	if _, code := get(t, srv.URL+"/files/export?f=xml", ""); code != http.StatusBadRequest {
		t.Errorf("GET /files/export?f=xml: %d, want 400", code)
	}
	// A page of another site that a name points here must get nothing.
	if _, code := get(t, srv.URL+"/", "evil.example:80"); code != http.StatusForbidden {
		t.Errorf("GET with Host evil.example: %d, want 403", code)
	}
}

func get(t *testing.T, u, host string) (string, int) {
	t.Helper()
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body), resp.StatusCode
}
