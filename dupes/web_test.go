// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
	srv := httptest.NewServer(newServer(context.Background(), sql, db, false).handler())
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

// The marking pages are the mark and apply commands behind buttons; what is
// theirs to get right is that they answer only to -rw and to this site, and
// that each button's answer is the piece of page it replaces.
func TestWebMarks(t *testing.T) {
	root := tree(t, map[string][]byte{
		"a/Photos/x.jpg": []byte("one"),
		"b/Photos/x.jpg": []byte("one"),
		"a/v.bin":        noise(6, 300<<10),
		"b/w.bin":        noise(6, 300<<10),
	})
	p := func(name string) string {
		return filepath.Join(root, filepath.FromSlash(name))
	}
	dbPath := filepath.Join(t.TempDir(), "dupes.db")
	scan(t, dbPath, root)
	db, err := openDB(dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	defer func(r func(string) error) { reveal = r }(reveal)
	var revealed []string
	reveal = func(path string) error {
		revealed = append(revealed, path)
		return nil
	}

	ro := httptest.NewServer(newServer(context.Background(), db, dbPath, false).handler())
	defer ro.Close()
	if body, code := post(t, ro.URL+"/mark", url.Values{"path": {p("a/v.bin")}, "on": {"1"}}, ""); code != http.StatusForbidden {
		t.Errorf("POST /mark without -rw: %d\n%s", code, body)
	}

	s := newServer(context.Background(), db, dbPath, true)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	if _, code := post(t, srv.URL+"/mark", url.Values{"path": {p("a/v.bin")}, "on": {"1"}}, "cross-site"); code != http.StatusForbidden {
		t.Errorf("POST /mark from another site: %d, want 403", code)
	}

	for _, tc := range []struct {
		path string
		form url.Values
		want []string
	}{
		{"/mark", url.Values{"path": {p("a/v.bin")}, "on": {"1"}}, []string{`class="toggle on"`, "unmark"}},
		{"/mark", url.Values{"path": {p("b/w.bin")}, "on": {"1"}}, []string{"the last copy"}},
		{"/keep", url.Values{"path": {p("a/v.bin")}, "rank": {"3"}}, []string{
			`<span class="rank">3</span>`, `class="toggle on"`, "keep only this",
		}},
		// a and b are copies, and their Photos only because they are.
		{"/dirs/mark", url.Values{"dir": {p("a")}, "keep": {"1"}, "rank": {"1"}}, []string{
			"2 files to move", "keep this one",
		}},
		{"/rule", url.Values{"from": {p("a")}}, []string{"marked 0 files", "2 files with no copy to keep"}},
		{"/open", url.Values{"path": {p("a")}}, nil},
	} {
		body, code := post(t, srv.URL+tc.path, tc.form, "")
		if code/100 != 2 {
			t.Errorf("POST %s %v: %d\n%s", tc.path, tc.form, code, body)
			continue
		}
		body = html.UnescapeString(body)
		for _, w := range tc.want {
			if !strings.Contains(body, w) {
				t.Errorf("POST %s %v has no %q\n%s", tc.path, tc.form, w, body)
			}
		}
	}
	if len(revealed) != 1 || revealed[0] != p("a") {
		t.Errorf("revealed %q", revealed)
	}
	if _, code := post(t, srv.URL+"/open", url.Values{"path": {filepath.Dir(root)}}, ""); code != http.StatusBadRequest {
		t.Errorf("POST /open outside the scanned paths: %d, want 400", code)
	}

	body, _ := get(t, srv.URL+"/marks", "")
	for _, w := range []string{"2</b><span>files marked", p("b/w.bin"), p("b/Photos/x.jpg"), `<datalist id="places">`} {
		if !strings.Contains(html.UnescapeString(body), w) {
			t.Errorf("GET /marks has no %q", w)
		}
	}

	run := func(form url.Values) *applyJob {
		t.Helper()
		if body, code := post(t, srv.URL+"/apply", form, ""); code != http.StatusOK {
			t.Fatalf("POST /apply: %d\n%s", code, body)
		}
		for range 500 {
			if j := s.jobView(); j != nil && j.Finished {
				return j
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("apply runs on and on")
		return nil
	}
	if j := run(url.Values{"dry": {"1"}}); j.Moved != 2 || j.Skipped != 0 {
		t.Fatalf("check: %+v", j)
	}
	if j := run(nil); j.Moved != 2 || j.Err != "" {
		t.Fatalf("move: %+v", j)
	}
	if _, err := os.Stat(p("b/w.bin")); err == nil {
		t.Fatal("b/w.bin was not moved")
	}

	body, _ = get(t, srv.URL+"/history", "")
	if !strings.Contains(html.UnescapeString(body), p("b/w.bin")) || !strings.Contains(body, trashDir) {
		t.Errorf("GET /history:\n%s", body)
	}
	rs, err := loadRemovals(context.Background(), db, `where path = ?`, p("b/w.bin"))
	if err != nil || len(rs) != 1 {
		t.Fatal(rs, err)
	}
	body, code := post(t, srv.URL+"/restore", url.Values{"id": {strconv.FormatInt(rs[0].ID, 10)}}, "")
	if code != http.StatusOK || !strings.Contains(body, "restored") {
		t.Errorf("POST /restore: %d\n%s", code, body)
	}
	if _, err := os.Stat(p("b/w.bin")); err != nil {
		t.Errorf("b/w.bin was not restored: %v", err)
	}
}

// post sends a form as a browser on this site would, or, given site, as
// one on another.
func post(t *testing.T, u string, form url.Values, site string) (string, int) {
	t.Helper()
	req, err := http.NewRequest("POST", u, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if site != "" {
		req.Header.Set("Sec-Fetch-Site", site)
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
