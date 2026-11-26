// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"rsc.io/markdown"
)

var (
	//go:embed page.html
	pageHTML string

	//go:embed style.css
	styleCSS string

	pages = template.Must(template.New("").Parse(pageHTML))
)

// themeJS puts back the theme the reader chose last and lets the button
// switch it. The choice is kept in the browser, for the address served on.
const themeJS = `try{var t=localStorage.getItem("prr-theme");` +
	`if(t)document.documentElement.dataset.theme=t}catch(e){}` +
	`document.addEventListener("DOMContentLoaded",function(){` +
	`document.getElementById("theme").onclick=function(){` +
	`var d=document.documentElement,t=d.dataset.theme==="dark"?"light":"dark";` +
	`d.dataset.theme=t;try{localStorage.setItem("prr-theme",t)}catch(e){}}})`

// The pages are files of other people's repositories: their HTML is
// rendered, as on GitLab and GitHub, but nothing in it runs. The one
// script allowed is themeJS, by its hash.
var csp = "default-src 'none'; style-src 'unsafe-inline'; img-src * data:; " +
	"script-src 'sha256-" + hash(themeJS) + "'; " +
	"form-action 'none'; base-uri 'none'; frame-ancestors 'none'"

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// A bundle is what prr read serves: for each repository, the tree of the
// target branch with the files of its PRs on top, as it will be once they
// all land.
type bundle struct {
	dir string
	prs []*readPR
}

// A readPR is a PR in a bundle: the directory of its repository there and
// its files.
type readPR struct {
	*PR
	Dir   string
	Files []change
}

func cmdRead(ctx context.Context, refs []string, addr string, open bool) error {
	prs, err := loadAll(ctx, refs)
	if err != nil {
		return err
	}
	b, err := build(ctx, prs)
	if err != nil {
		return err
	}
	return b.serve(ctx, addr, open)
}

// build writes the bundle of the PRs into ~/prr/read, replacing the one
// there from an earlier run.
func build(ctx context.Context, prs []*PR) (*bundle, error) {
	var names []string
	for _, p := range prs {
		names = append(names, fmt.Sprintf("%s-%d", path.Base(p.Repo), p.N))
	}
	b := &bundle{dir: filepath.Join(home(), "read", strings.Join(names, "+"))}
	if err := os.RemoveAll(b.dir); err != nil {
		return nil, err
	}
	dirs := map[string]string{} // the directory of each clone in the bundle
	used := map[string]bool{}
	for _, p := range prs {
		dir, ok := dirs[p.gitDir()]
		if !ok {
			dir = path.Base(p.Repo)
			for i := 2; used[dir]; i++ {
				dir = fmt.Sprintf("%s-%d", path.Base(p.Repo), i)
			}
			dirs[p.gitDir()], used[dir] = dir, true
			err := extract(ctx, p.gitDir(), "refs/remotes/origin/"+p.Base, nil, b.path(dir))
			if err != nil {
				return nil, err
			}
		}
		cs, err := p.changes(ctx)
		if err != nil {
			return nil, err
		}
		var files []string
		for _, c := range cs {
			if c.Status == "D" {
				_ = os.Remove(filepath.Join(b.path(dir), filepath.FromSlash(c.Path)))
				continue
			}
			files = append(files, c.Path)
		}
		if len(files) > 0 {
			if err := extract(ctx, p.gitDir(), p.Head, files, b.path(dir)); err != nil {
				return nil, err
			}
		}
		b.prs = append(b.prs, &readPR{PR: p, Dir: dir, Files: cs})
	}
	return b, nil
}

func (b *bundle) path(dir string) string {
	return filepath.Join(b.dir, dir)
}

// extract writes the regular files of rev, or only the named ones, into
// dir. Symbolic links are left out: one could point out of the tree.
func extract(ctx context.Context, gitDir, rev string, files []string, dir string) error {
	cmd := command(ctx, gitDir, "git", append([]string{"archive", "--format=tar", rev, "--"}, files...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	err = untar(stdout, dir)
	_, _ = io.Copy(io.Discard, stdout)
	if werr := cmd.Wait(); werr != nil {
		return fmt.Errorf("git archive %s: %w: %s", rev, werr, strings.TrimSpace(stderr.String()))
	}
	return err
}

func untar(r io.Reader, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if err := root.MkdirAll(path.Dir(h.Name), 0o755); err != nil {
			return err
		}
		f, err := root.Create(h.Name)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, tr)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
}

func (b *bundle) serve(ctx context.Context, addr string, open bool) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		ln, err = net.Listen("tcp", "localhost:0") // addr is busy: any port will do
		if err != nil {
			return err
		}
	}
	u := "http://" + ln.Addr().String() + "/"
	fmt.Printf("prr: serving %s on %s\n", b.dir, u)
	if open {
		openBrowser(u)
	}
	srv := &http.Server{Handler: b}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func openBrowser(u string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	_ = exec.Command(name, u).Start() // the URL is printed anyway
}

func (b *bundle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", csp)
	name := strings.Trim(path.Clean(r.URL.Path), "/")
	if name == "" {
		b.index(w)
		return
	}
	fsys := os.DirFS(b.dir)
	info, err := fs.Stat(fsys, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var body template.HTML
	switch data, rerr := fs.ReadFile(fsys, name); {
	case info.IsDir():
		body = listing(fsys, name)
	case rerr != nil:
		http.Error(w, rerr.Error(), http.StatusInternalServerError)
		return
	case strings.HasSuffix(name, ".md"):
		body = renderMarkdown(string(data))
	case utf8.Valid(data) && !bytes.Contains(data, []byte{0}):
		body = template.HTML("<pre>" + html.EscapeString(string(data)) + "</pre>")
	default:
		http.ServeFileFS(w, r, fsys, name)
		return
	}
	b.page(w, path.Base(name), name, body)
}

func (b *bundle) index(w http.ResponseWriter) {
	type indexPR struct {
		*readPR
		Desc template.HTML
	}
	var prs []indexPR
	for _, p := range b.prs {
		prs = append(prs, indexPR{p, renderMarkdown(p.Body)})
	}
	var buf bytes.Buffer
	if err := pages.ExecuteTemplate(&buf, "index", prs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b.page(w, "prr read", "", template.HTML(buf.String()))
}

// listing is the body of the page of a directory.
func listing(fsys fs.FS, name string) template.HTML {
	entries, err := fs.ReadDir(fsys, name)
	if err != nil {
		return template.HTML("<p>" + html.EscapeString(err.Error()) + "</p>")
	}
	var b strings.Builder
	b.WriteString(`<ul class="ls">`)
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() {
			n += "/"
		}
		href := "/" + name + "/" + n
		fmt.Fprintf(&b, `<li><a href="%s">%s</a></li>`, html.EscapeString(href), html.EscapeString(n))
	}
	b.WriteString("</ul>")
	return template.HTML(b.String())
}

func renderMarkdown(src string) template.HTML {
	p := &markdown.Parser{
		HeadingID:     true,
		Strikethrough: true,
		TaskList:      true,
		AutoLinkText:  true,
		Table:         true,
	}
	return template.HTML(markdown.ToHTML(p.Parse(src)))
}

type navPR struct {
	Ref, URL, Title, Prefix string
	Files                   []navFile
}

type navFile struct {
	Status, Name, Href string
	Current            bool
}

type crumb struct {
	Name, Href string
}

// page writes a page: the files of the PRs on the side, the path of the
// page on top and its body.
func (b *bundle) page(w http.ResponseWriter, title, name string, body template.HTML) {
	var nav []navPR
	for _, p := range b.prs {
		var paths []string
		for _, c := range p.Files {
			paths = append(paths, c.Path)
		}
		n := navPR{Ref: p.Ref(), URL: p.URL, Title: p.Title, Prefix: commonDir(paths)}
		for _, c := range p.Files {
			full := p.Dir + "/" + c.Path
			f := navFile{Status: c.Status, Name: strings.TrimPrefix(c.Path, n.Prefix), Current: full == name}
			if c.Status != "D" {
				f.Href = "/" + full
			}
			n.Files = append(n.Files, f)
		}
		nav = append(nav, n)
	}
	var crumbs []crumb
	if name != "" {
		elems := strings.Split(name, "/")
		for i, e := range elems {
			c := crumb{Name: e}
			if i < len(elems)-1 {
				c.Href = "/" + strings.Join(elems[:i+1], "/") + "/"
			}
			crumbs = append(crumbs, c)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := pages.ExecuteTemplate(w, "page", struct {
		Title  string
		Style  template.CSS
		Script template.JS
		PRs    []navPR
		Crumbs []crumb
		Body   template.HTML
	}{title, template.CSS(styleCSS), template.JS(themeJS), nav, crumbs, body})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// commonDir returns the directory all the paths are in, with a slash, or "".
func commonDir(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	dir := path.Dir(paths[0])
	for _, p := range paths[1:] {
		for dir != "." && !strings.HasPrefix(p, dir+"/") {
			dir = path.Dir(dir)
		}
	}
	if dir == "." || dir == "/" {
		return ""
	}
	return dir + "/"
}
