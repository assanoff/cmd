// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// defaultWebAddr is where go-audit web serves unless -a says otherwise.
const defaultWebAddr = "localhost:8781"

// maxSource is the largest file the page of a file shows.
const maxSource = 4 << 20

func webCommand() *command {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	addr := fs.String("a", defaultWebAddr, "serve on `addr`")
	quiet := fs.Bool("n", false, "open no browser")
	return &command{
		name:  "web",
		usage: "web [-a addr] [-n]",
		short: "Serve the store on localhost: the reports, and the code with their findings",
		flags: fs,
		run: func(ctx context.Context, args []string) error {
			if len(args) > 0 {
				return usagef("web takes no arguments")
			}
			return runWeb(ctx, *addr, !*quiet)
		},
	}
}

// runWeb serves the store on addr until ctx is done, and opens the index
// in a browser if browse is set.
func runWeb(ctx context.Context, addr string, browse bool) error {
	store, err := storeDir()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: newWeb(store), ReadHeaderTimeout: 10 * time.Second}
	home := "http://" + browseAddr(addr, ln.Addr()) + "/"
	if _, err := fmt.Fprintf(stdout, "serving %s on %s\n", store, home); err != nil {
		_ = ln.Close()
		return err
	}
	if browse {
		if err := openBrowser(home); err != nil {
			log.Printf("opening a browser: %v", err)
		}
	}
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

// browseAddr returns the host and port to open in a browser: those of addr,
// with localhost for a host that stands for every interface and the port
// the listener got for port 0.
func browseAddr(addr string, got net.Addr) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return got.String()
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "localhost"
	}
	if port == "0" {
		_, port, _ = net.SplitHostPort(got.String())
	}
	return net.JoinHostPort(host, port)
}

// A web serves the store: the list of the reports, those of a module, a
// report, and a file of a module with the findings of a report under its
// lines. It reads the store afresh for each request, so that a new report
// is there without a restart.
type web struct {
	store string
}

func newWeb(store string) http.Handler {
	w := &web{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /src/{path...}", w.source)
	mux.HandleFunc("GET /{path...}", w.reports)
	return localOnly(mux)
}

// localOnly lets through only requests for a local host, so that a page
// elsewhere cannot reach the store through a name that resolves to this
// machine (DNS rebinding).
func localOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		host = strings.Trim(host, "[]")
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "go-audit web answers only to localhost", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// reports serves the index, the page of a module and the page of a report.
func (s *web) reports(w http.ResponseWriter, r *http.Request) {
	all, err := loadReports(s.store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p := r.PathValue("path")
	switch {
	case p == "":
		s.list(w, "go-audit reports", "", all)
		return
	case strings.HasSuffix(p, "/"):
		mod := strings.TrimSuffix(p, "/")
		var of []*report
		for _, rep := range all {
			if rep.Module == mod {
				of = append(of, rep)
			}
		}
		if len(of) == 0 {
			http.NotFound(w, r)
			return
		}
		s.list(w, mod, mod, of)
		return
	}
	mod, name := path.Split(p)
	mod = strings.TrimSuffix(mod, "/")
	for _, rep := range all {
		if rep.Module == mod && rep.name() == name {
			s.report(w, rep)
			return
		}
	}
	// A module without the slash at the end.
	for _, rep := range all {
		if rep.Module == p {
			http.Redirect(w, r, moduleURL(p), http.StatusMovedPermanently)
			return
		}
	}
	http.NotFound(w, r)
}

// A reportRow is a report as a line of a list.
type reportRow struct {
	Time, Module, What, Summary string
	Total                       string
	URL, ModuleURL              string
}

var listPage = template.Must(template.New("list").Parse(`<nav><a href="/">reports</a>
{{- with .Module}} › {{.}}{{end}}</nav>
<h1>{{.Title}}</h1>
<p class="muted">{{len .Rows}} reports · {{.Store}}</p>
<table>
<thead><tr><th>Time</th><th>Module</th><th>Report</th><th>Findings</th><th>Summary</th></tr></thead>
<tbody>
{{- range .Rows}}
<tr><td><a href="{{.URL}}">{{.Time}}</a></td><td><a href="{{.ModuleURL}}">{{.Module}}</a></td><td>{{.What}}</td><td>{{.Total}}</td><td>{{.Summary}}</td></tr>
{{- end}}
</tbody>
</table>
`))

// list serves a list of reports: all of them, or those of the module mod.
func (s *web) list(w http.ResponseWriter, title, mod string, reports []*report) {
	var rows []reportRow
	for _, r := range reports {
		row := reportRow{
			Time: "-", Module: orDash(r.Module), What: r.Kind + " " + r.Label,
			Total: strconv.Itoa(r.Total), Summary: r.Summary,
			URL: reportURL(r), ModuleURL: moduleURL(r.Module),
		}
		if !r.Time.IsZero() {
			row.Time = r.Time.Local().Format("2006-01-02 15:04")
		}
		if r.err != nil {
			row.What, row.Total, row.Summary = r.name(), "-", r.err.Error()
		}
		rows = append(rows, row)
	}
	var b bytes.Buffer
	err := listPage.Execute(&b, struct {
		Title, Module, Store string
		Rows                 []reportRow
	}{title, mod, filepath.Join(s.store, "reports"), rows})
	s.serve(w, title, template.HTML(b.String()), err) // the template escaped it
}

// report serves a report, its file:line links leading to the pages of the
// files.
func (s *web) report(w http.ResponseWriter, r *report) {
	md, err := s.read(r.path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	root := ""
	if r.err == nil {
		root = r.Root
	}
	link := func(file string, line, _ int) string {
		return sourceURL(r, file, line)
	}
	var b bytes.Buffer
	err = navPage.Execute(&b, struct{ Module, ModuleURL, Name string }{
		r.Module, moduleURL(r.Module), r.name(),
	})
	body := template.HTML(b.String()) + renderMarkdown(string(md), root, link)
	s.serve(w, r.Module+" · "+r.name(), body, err)
}

var navPage = template.Must(template.New("nav").Parse(
	`<nav><a href="/">reports</a> › <a href="{{.ModuleURL}}">{{.Module}}</a> › {{.Name}}</nav>
`))

// read returns a file of the store, through the store as a root, so that
// nothing outside it can be read.
func (s *web) read(file string) ([]byte, error) {
	rel, err := filepath.Rel(s.store, file)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.store)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(rel)
}

// serve writes a page, or the error of making it.
func (s *web) serve(w http.ResponseWriter, title string, body template.HTML, err error) {
	var page []byte
	if err == nil {
		page, err = renderPage(title, body)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(page)
}

// A sourceFinding is a finding of either kind of report as the page of a
// file shows it: go-audit names the check, go-style the tool and the
// severity, and may say a finding is about more lines.
type sourceFinding struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Col       int    `json:"col"`
	Rule      string `json:"rule"`
	Message   string `json:"message"`
	Check     string `json:"check"`
	Tool      string `json:"tool"`
	Severity  string `json:"severity"`
	Verified  *bool  `json:"verified"`
	AlsoLines []int  `json:"also_lines"`
}

// Who says what made the finding: the severity, the check or the tool, the
// rule, and whether go-style left it unverified.
func (f sourceFinding) Who() string {
	var parts []string
	for _, p := range []string{f.Severity, cmp.Or(f.Check, f.Tool), f.Rule} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if f.Verified != nil && !*f.Verified {
		parts = append(parts, "not verified")
	}
	return strings.Join(parts, " · ")
}

// A sourceLine is a line of a file with the findings about it.
type sourceLine struct {
	N        int
	Text     string
	Findings []sourceFinding
}

var sourcePage = template.Must(template.New("source").Funcs(template.FuncMap{
	"moduleURL": moduleURL,
}).Parse(`<nav><a href="/">reports</a> › <a href="{{moduleURL .Report.Module}}">{{.Report.Module}}</a> ›
<a href="{{.ReportURL}}">{{.Report.Name}}</a> › {{.File}}</nav>
<h1>{{.File}}</h1>
<p class="muted">{{.Count}} findings of the report in this file{{with .Editor}} · <a href="{{.}}">open in the editor</a>{{end}}</p>
{{- if .Changed}}
<p class="warn">The file changed after the report, at {{.Changed}}: its lines may have moved since.</p>
{{- end}}
{{- range .Whole}}
<div class="finding"><span class="who">{{.Who}}</span> {{.Message}}</div>
{{- end}}
<table class="src">
{{- range .Lines}}
<tr id="L{{.N}}"><td class="n"><a href="#L{{.N}}">{{.N}}</a></td><td><pre>{{.Text}}</pre></td></tr>
{{- range .Findings}}
<tr class="finding"><td></td><td><span class="who">{{.Who}}</span> {{.Message}}</td></tr>
{{- end}}
{{- end}}
</table>
`))

// source serves a file of a module with the findings of a report under its
// lines. The module and its root come from the reports of the store, and
// the file is read through the root, so that nothing outside the module can
// be read.
func (s *web) source(w http.ResponseWriter, r *http.Request) {
	all, err := loadReports(s.store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rep, file := pickSource(all, r.PathValue("path"), r.URL.Query().Get("report"))
	if rep == nil || rep.err != nil || rep.Root == "" || !filepath.IsLocal(filepath.FromSlash(file)) {
		http.NotFound(w, r)
		return
	}
	data, fi, err := readSource(rep.Root, file)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var findings []sourceFinding
	if js, err := s.read(strings.TrimSuffix(rep.path, ".md") + ".json"); err == nil {
		var twin struct {
			Findings []sourceFinding `json:"findings"`
		}
		if json.Unmarshal(js, &twin) == nil {
			for _, f := range twin.Findings {
				if f.File == file {
					findings = append(findings, f)
				}
			}
		}
	}

	text := strings.TrimSuffix(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var lines []sourceLine
	for i, t := range strings.Split(text, "\n") {
		lines = append(lines, sourceLine{N: i + 1, Text: t})
	}
	var whole []sourceFinding // about the file as a whole, or past its end now
	for _, f := range findings {
		placed := false
		for _, n := range append([]int{f.Line}, f.AlsoLines...) {
			if n >= 1 && n <= len(lines) {
				lines[n-1].Findings = append(lines[n-1].Findings, f)
				placed = true
			}
		}
		if !placed {
			whole = append(whole, f)
		}
	}

	editor := ""
	if cfg, err := loadConfig(s.store); err == nil && cfg.Editor != "" {
		editor = editorLinker(cfg.Editor, rep.Root)(file, 1, 1)
	}
	changed := ""
	if fi.ModTime().After(rep.Time.Add(time.Second)) {
		changed = fi.ModTime().Local().Format("2006-01-02 15:04:05")
	}
	var b bytes.Buffer
	err = sourcePage.Execute(&b, struct {
		Report    struct{ Module, Name string }
		ReportURL string
		File      string
		Count     int
		Editor    template.URL
		Changed   string
		Whole     []sourceFinding
		Lines     []sourceLine
	}{
		Report:    struct{ Module, Name string }{rep.Module, rep.name()},
		ReportURL: reportURL(rep),
		File:      file,
		Count:     len(findings),
		Editor:    template.URL(editor), // the editor of one's own config
		Changed:   changed,
		Whole:     whole,
		Lines:     lines,
	})
	s.serve(w, file+" · "+rep.Module, template.HTML(b.String()), err) // the template escaped it
}

// pickSource returns the report and the file that the path of a page of a
// file and its report parameter name: the module is the longest module of
// the reports that the path starts with, and the report is the one named,
// or the newest of the module without a name.
func pickSource(all []*report, p, name string) (*report, string) {
	var rep *report
	file := ""
	for _, r := range all {
		rest, ok := strings.CutPrefix(p, r.Module+"/")
		if !ok || r.Module == "" || name != "" && r.name() != name {
			continue
		}
		// The longest module; of one module, the first report, the newest.
		if rep == nil || len(r.Module) > len(rep.Module) {
			rep, file = r, rest
		}
	}
	return rep, file
}

// readSource returns a file under root and what it is, through root, so
// that a link out of it leads nowhere.
func readSource(root, file string) ([]byte, os.FileInfo, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = r.Close() }()
	fi, err := r.Stat(filepath.FromSlash(file))
	if err != nil {
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxSource {
		return nil, nil, errors.New("not a file to show")
	}
	data, err := r.ReadFile(filepath.FromSlash(file))
	return data, fi, err
}

// moduleURL returns the address of the page of a module.
func moduleURL(mod string) string {
	return "/" + escapePath(mod) + "/"
}

// reportURL returns the address of the page of a report.
func reportURL(r *report) string {
	return "/" + escapePath(r.Module) + "/" + url.PathEscape(r.name())
}

// sourceURL returns the address of the page of a file of the module of a
// report, at a line of it if line is not 0.
func sourceURL(r *report, file string, line int) string {
	u := "/src/" + escapePath(r.Module) + "/" + escapePath(file) + "?report=" + url.QueryEscape(r.name())
	if line > 0 {
		u += "#L" + strconv.Itoa(line)
	}
	return u
}
