// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/subcommands"
)

// The pages are templates and the styles and scripts static files, all
// built into the binary: the page works with no network, and a dupes from
// one release never serves the templates of another.
//
//go:embed static tmpl
var embedFS embed.FS

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"size":   formatSize,
	"date":   date,
	"pct":    func(f float64) string { return fmt.Sprintf("%.0f%%", 100*f) },
	"add":    func(a, b int) int { return a + b },
	"base":   filepath.Base,
	"dirURL": dirURL,
	"fileURL": func(path string) string {
		return dirURL(filepath.Dir(path)) + "#" + url.PathEscape(filepath.Base(path))
	},
	"plural": plural[int64],
	"pair":   func(a, b dirView) []dirView { return []dirView{a, b} },
	"fileCopies": func(path string) string {
		return "/copies?path=" + url.QueryEscape(path)
	},
	"dirCopies": func(dir string) string {
		return "/copies?dir=" + url.QueryEscape(dir)
	},
	"short": func(key string) string {
		if len(key) == 64 {
			return key[:12]
		}
		return key
	},
	"vals": vals,
	"fg": func(rank int, g fileGroup, by string) fgView {
		return fgView{Rank: rank, G: g, Hash: by == "hash"}
	},
	"toggle": func(path string, marked bool) toggleView {
		return toggleView{Path: path, Marked: marked}
	},
	"itoa": strconv.Itoa,
	"hrow": func(r removal) hrowView { return hrowView{removal: r} },
	// rw reports whether the pages mark files; each server says for itself.
	"rw": func() bool { return false },
}).ParseFS(embedFS, "tmpl/*.html"))

// defaultWebAddr is where dupes web serves unless -http says otherwise; the
// port is the one go-audit web does not take, 8781 less some.
const defaultWebAddr = "localhost:8754"

// pageSize is how many groups a page shows before its "more" button.
const pageSize = 50

type webCmd struct {
	db   string
	addr string
	none bool
	rw   bool
}

func (*webCmd) Name() string { return "web" }

func (*webCmd) Synopsis() string {
	return "browse the reports in a web browser"
}

func (*webCmd) Usage() string {
	return `web [-http addr] [-n] [-db file]

Serves the reports as pages on localhost and opens them in a browser: what was
scanned, the duplicate files and directories with the filters of the files and
dirs commands, and every scanned directory, with the copies of what it holds.
The pages read the database afresh after each scan, so web can stay running
while dupes scan runs again elsewhere. Ctrl-C stops it.

With -rw the pages mark files too, as dupes mark does, and move the marked
ones away as dupes apply does, into the .dupes-trash of their disk; History
lists what was moved and moves it back. Without it they change nothing.

A port alone, -http :8754, serves on localhost too. Whatever the address, the
pages answer only to a browser on this machine: they list the files of every
disk scanned.

`
}

func (c *webCmd) SetFlags(f *flag.FlagSet) {
	dbFlag(f, &c.db)
	f.StringVar(&c.addr, "http", defaultWebAddr, "serve on `addr`")
	f.BoolVar(&c.none, "n", false, "open no browser")
	f.BoolVar(&c.rw, "rw", false, "mark files and move the marked ones, as mark and apply do")
}

func (c *webCmd) Execute(ctx context.Context, f *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	if f.NArg() > 0 {
		return exit(usagef("web: takes no arguments"))
	}
	return exit(c.run(ctx))
}

// openBrowser opens a URL in the web browser; a variable, so that tests open
// none.
var openBrowser = func(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (c *webCmd) run(ctx context.Context) error {
	db, err := openDB(c.db, false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	addr := c.addr
	if host, port, err := net.SplitHostPort(addr); err == nil && host == "" {
		addr = net.JoinHostPort("localhost", port)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s := newServer(ctx, db, c.db, c.rw)
	srv := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}
	home := "http://" + browseAddr(addr, ln.Addr()) + "/"
	fmt.Fprintf(os.Stderr, "serving %s on %s\n", c.db, home)
	if !c.none {
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
// with localhost for a host that stands for every interface and the port the
// listener got for port 0.
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

// localOnly lets through only requests for a local host, so that a page
// elsewhere cannot read the list of this machine's files through a name that
// resolves to it (DNS rebinding), and a machine on the network gets nothing
// from a server bound to every interface.
func localOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		host = strings.Trim(host, "[]")
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "dupes web answers only to localhost", http.StatusForbidden)
			return
		}
		if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			if a := net.ParseIP(ip); a != nil && !a.IsLoopback() {
				http.Error(w, "dupes web answers only to localhost", http.StatusForbidden)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

// server serves the pages. Its GETs change nothing; its POSTs mark files
// and move them, and answer only to a server with -rw, but for showing a
// file in the file manager.
type server struct {
	db   *sql.DB
	path string
	rw   bool
	ctx  context.Context // the server's own, which a move outlives a request in
	tmpl *template.Template

	jmu sync.Mutex
	job *applyJob // the last apply, running or done

	// The groups of directories, the totals and the counts take seconds on a
	// database of a whole disk, so each is computed once for each state of
	// the database. data_version changes when another connection, a scan,
	// commits; memo is then thrown away.
	mu      sync.Mutex
	version int64
	memo    map[string]any
}

func newServer(ctx context.Context, db *sql.DB, path string, rw bool) *server {
	s := &server{db: db, path: path, rw: rw, ctx: ctx}
	s.tmpl = template.Must(tmpl.Clone()).Funcs(template.FuncMap{
		"rw": func() bool { return s.rw },
	})
	return s
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handle(s.index))
	mux.HandleFunc("GET /files", s.handle(s.files))
	mux.HandleFunc("GET /files/more", s.handle(s.filesMore))
	mux.HandleFunc("GET /files/export", s.handle(s.filesExport))
	mux.HandleFunc("GET /dirs", s.handle(s.dirs))
	mux.HandleFunc("GET /dirs/more", s.handle(s.dirsMore))
	mux.HandleFunc("GET /dirs/export", s.handle(s.dirsExport))
	mux.HandleFunc("GET /dir", s.handle(s.dir))
	mux.HandleFunc("GET /copies", s.handle(s.copies))
	mux.HandleFunc("GET /marks", s.handle(s.marks))
	mux.HandleFunc("GET /marks/more", s.handle(s.marksMore))
	mux.HandleFunc("GET /history", s.handle(s.history))
	mux.HandleFunc("GET /history/more", s.handle(s.historyMore))
	mux.HandleFunc("GET /apply", s.handle(s.applyStatus))
	mux.HandleFunc("POST /open", s.handle(s.open))
	for path, h := range map[string]func(http.ResponseWriter, *http.Request) error{
		"/mark":       s.markFile,
		"/keep":       s.keepFile,
		"/dirs/mark":  s.markDir,
		"/rule":       s.rule,
		"/unmark-all": s.unmarkAll,
		"/apply":      s.apply,
		"/restore":    s.restore,
	} {
		mux.HandleFunc("POST "+path, s.handle(s.writing(h)))
	}
	for _, name := range []string{"style.css", "dupes.js", "htmx.min.js", "htmx.LICENSE"} {
		mux.HandleFunc("GET /"+name, func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, embedFS, "static/"+name)
		})
	}
	// A form on another site posting here is refused by the browser's own
	// word for where it comes from, Sec-Fetch-Site or Origin.
	return localOnly(http.NewCrossOriginProtection().Handler(mux))
}

// writing lets a handler that changes the database through only on a
// server with -rw.
func (s *server) writing(h func(http.ResponseWriter, *http.Request) error) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		if !s.rw {
			http.Error(w, "dupes web -rw marks and moves files; this one only shows them",
				http.StatusForbidden)
			return nil
		}
		if err := ensureRecords(s.db, true); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return nil
		}
		return h(w, r)
	}
}

// handle turns a handler that returns an error into an http.HandlerFunc:
// a wrong form is the caller's mistake, anything else the server's.
func (s *server) handle(h func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := h(w, r)
		if err == nil {
			return
		}
		code := http.StatusInternalServerError
		if ue := (usageError{}); errors.As(err, &ue) {
			code = http.StatusBadRequest
		} else {
			log.Printf("%s: %v", r.URL, err)
		}
		http.Error(w, err.Error(), code)
	}
}

// render executes a template into a buffer first, so that a failure halfway
// is an error page and not half a page.
func (s *server) render(w http.ResponseWriter, name string, data any) error {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := w.Write(buf.Bytes())
	return err
}

// cached returns what fn computes for key, computing it once for each state
// of the database.
func cached[T any](ctx context.Context, s *server, key string, fn func() (T, error)) (T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var zero T
	var v int64
	if err := s.db.QueryRowContext(ctx, `pragma data_version`).Scan(&v); err != nil {
		return zero, err
	}
	if s.memo == nil || v != s.version {
		s.memo, s.version = map[string]any{}, v
	}
	if x, ok := s.memo[key]; ok {
		return x.(T), nil
	}
	x, err := fn()
	if err != nil {
		return zero, err
	}
	s.memo[key] = x
	return x, nil
}

// page is what every page has around its own data.
type page struct {
	Title  string
	Active string
	DB     string
	RW     bool
	Marked int64    // files marked, for the menu
	Places []string // the scanned paths and what is right below them
	P      any
}

// renderPage renders a whole page, with p as its own data.
func (s *server) renderPage(w http.ResponseWriter, r *http.Request, name, title, active string, p any) error {
	ctx := r.Context()
	pg := page{Title: title, Active: active, DB: s.path, RW: s.rw, P: p}
	if err := s.db.QueryRowContext(ctx, `select count(*) from marks`).Scan(&pg.Marked); err != nil {
		return err
	}
	var err error
	if pg.Places, err = s.places(ctx); err != nil {
		return err
	}
	return s.render(w, name, pg)
}

// places returns what the fields of a path offer to pick from: each scanned
// path and the directories right below it.
func (s *server) places(ctx context.Context) ([]string, error) {
	return cached(ctx, s, "places", func() ([]string, error) {
		roots, err := listRoots(ctx, s.db)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, r := range roots {
			out = append(out, r.path)
			lo, hi := under(r.path)
			rows, err := s.db.QueryContext(ctx, `
				select path from dirs
				where path >= ? and path < ? and instr(substr(path, ?), ?) = 0
				order by path limit 500`,
				lo, hi, utf8.RuneCountInString(lo)+1, string(os.PathSeparator))
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var p string
				if err := rows.Scan(&p); err != nil {
					_ = rows.Close()
					return nil, err
				}
				out = append(out, p)
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
		}
		return out, nil
	})
}

// changed forgets what was computed from the files, which the server has
// just changed itself: data_version only counts other connections' commits.
func (s *server) changed() {
	s.mu.Lock()
	s.memo = nil
	s.mu.Unlock()
}

func dirURL(dir string) string { return "/dir?path=" + url.QueryEscape(dir) }

// date writes a time, or a time that may be nil, as the pages show them.
func date(t any) string {
	switch t := t.(type) {
	case time.Time:
		return t.Format("2006-01-02 15:04")
	case *time.Time:
		if t != nil {
			return t.Format("2006-01-02 15:04")
		}
	}
	return ""
}

// ---------------------------------------------------------------- overview

type rootView struct {
	Path    string
	Scanned time.Time
	Files   int64
	Bytes   int64
	Offline bool
}

type indexPage struct {
	Summary summary
	Roots   []rootView
	Files   filesPage
	Dirs    dirsPage
}

func (s *server) index(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var p indexPage
	var err error
	// The groups of directories are the summary's count of them as well;
	// summarize would compute them a second time.
	groups, err := s.dirGroups(ctx)
	if err != nil {
		return err
	}
	if p.Dirs.Views, err = s.dgViews(ctx, 1, groups[:min(5, len(groups))]); err != nil {
		return err
	}
	if p.Summary, err = cached(ctx, s, "summary", func() (summary, error) {
		return summarizeFiles(ctx, s.db)
	}); err != nil {
		return err
	}
	p.Summary.DirGroups = len(groups)
	if p.Roots, err = cached(ctx, s, "roots", func() ([]rootView, error) {
		return s.roots(ctx)
	}); err != nil {
		return err
	}
	q, err := newFileQuery("hash", "waste", filter{}, nil)
	if err != nil {
		return err
	}
	p.Files.Form.By = "hash"
	if p.Files.Groups, err = q.groups(ctx, s.db, 0, 5); err != nil {
		return err
	}
	if err := s.markGroups(ctx, p.Files.Groups); err != nil {
		return err
	}
	return s.renderPage(w, r, "index.html", "overview", "index", p)
}

func (s *server) roots(ctx context.Context) ([]rootView, error) {
	roots, err := listRoots(ctx, s.db)
	if err != nil {
		return nil, err
	}
	var out []rootView
	for _, r := range roots {
		v := rootView{Path: r.path, Scanned: r.scanned}
		lo, hi := under(r.path)
		err := s.db.QueryRowContext(ctx,
			`select count(*), coalesce(sum(size), 0) from files where path >= ? and path < ?`,
			lo, hi).Scan(&v.Files, &v.Bytes)
		if err != nil {
			return nil, err
		}
		_, err = os.Stat(r.path)
		v.Offline = err != nil
		out = append(out, v)
	}
	return out, nil
}

// ---------------------------------------------------------------- files

// filesForm is the form of the files page, which is the flags of the files
// command.
type filesForm struct {
	By, Sort, Ext, Min, Under string
}

type filesPage struct {
	Form   filesForm
	Total  int
	Offset int
	Groups []fileGroup
	More   string // the URL of the next groups as a fragment, or ""
	Next   string // the same as a page, for a browser without scripts
	CSV    string // the URLs of the whole report as a download
	JSON   string
	Error  string // a wrong filter, shown beside the form
}

func (s *server) fileQuery(r *http.Request) (filesForm, *fileQuery, error) {
	f := filesForm{
		By:    formValue(r, "by", "hash"),
		Sort:  formValue(r, "sort", "waste"),
		Ext:   r.FormValue("ext"),
		Min:   formValue(r, "min", "0"),
		Under: strings.TrimSpace(r.FormValue("under")),
	}
	flt, err := parseFilter(f.Ext, f.Min)
	if err != nil {
		return f, nil, err
	}
	scope, err := s.scope(r.Context(), f.Under)
	if err != nil {
		return f, nil, err
	}
	q, err := newFileQuery(f.By, f.Sort, flt, scope)
	return f, q, err
}

func (f filesForm) values() url.Values {
	v := url.Values{}
	set(v, "by", f.By, "hash")
	set(v, "sort", f.Sort, "waste")
	set(v, "ext", f.Ext, "")
	set(v, "min", f.Min, "0")
	set(v, "under", f.Under, "")
	return v
}

func (s *server) filesPage(r *http.Request) (filesPage, error) {
	ctx := r.Context()
	form, q, err := s.fileQuery(r)
	p := filesPage{Form: form}
	p.CSV, p.JSON = exportURLs("/files/export", form.values())
	if err != nil {
		return p, err
	}
	p.Offset = offset(r)
	p.Total, err = cached(ctx, s, "count "+form.values().Encode(), func() (int, error) {
		return q.count(ctx, s.db)
	})
	if err != nil {
		return p, err
	}
	if p.Groups, err = q.groups(ctx, s.db, p.Offset, pageSize); err != nil {
		return p, err
	}
	if err := s.markGroups(ctx, p.Groups); err != nil {
		return p, err
	}
	if next := p.Offset + len(p.Groups); next < p.Total {
		v := form.values()
		v.Set("offset", strconv.Itoa(next))
		p.More, p.Next = "/files/more?"+v.Encode(), "/files?"+v.Encode()
	}
	return p, nil
}

func (s *server) files(w http.ResponseWriter, r *http.Request) error {
	p, err := s.filesPage(r)
	// A wrong filter is shown beside the form, which stays to fix it.
	if ue := (usageError{}); errors.As(err, &ue) {
		p.Error = err.Error()
	} else if err != nil {
		return err
	}
	return s.renderPage(w, r, "files.html", "files", "files", p)
}

func (s *server) filesMore(w http.ResponseWriter, r *http.Request) error {
	p, err := s.filesPage(r)
	if err != nil {
		return err
	}
	return s.render(w, "fgroups", p)
}

func (s *server) filesExport(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	_, q, err := s.fileQuery(r)
	if err != nil {
		return err
	}
	groups, err := q.groups(ctx, s.db, 0, -1)
	if err != nil {
		return err
	}
	return export(w, r, "files", len(groups), func(wr *writer) error {
		return writeFileGroups(wr, groups)
	})
}

// exportURLs returns the URLs of a report as CSV and as JSON.
func exportURLs(path string, v url.Values) (csv, json string) {
	v.Set("f", "csv")
	csv = path + "?" + v.Encode()
	v.Set("f", "json")
	return csv, path + "?" + v.Encode()
}

// export writes a whole report as a CSV or JSON download.
func export(w http.ResponseWriter, r *http.Request, name string, total int, fn func(*writer) error) error {
	format := r.FormValue("f")
	ctype := map[string]string{"csv": "text/csv; charset=utf-8", "json": "application/json"}[format]
	if ctype == "" {
		return usagef("f=%s: csv or json", format)
	}
	var buf bytes.Buffer
	if err := fn(&writer{Writer: &buf, format: format, total: total}); err != nil {
		return err
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="dupes-%s.%s"`, name, format))
	_, err := w.Write(buf.Bytes())
	return err
}

// ---------------------------------------------------------------- dirs

type dirsForm struct {
	By, Min, Similar, Under string
}

type dirView struct {
	Path  string
	Files int64
	Bytes int64
}

type dirGroupView struct {
	Dirs  []string
	Files int64
	Bytes int64
	Waste int64
}

type pairView struct {
	Name        string
	Score       float64
	Common      int
	CommonBytes int64
	A, B        dirView
}

type dirsPage struct {
	Form   dirsForm
	Total  int
	Offset int
	Views  []dgView
	Pairs  []pairView
	More   string
	Next   string
	CSV    string
	JSON   string
	Error  string
}

func (f dirsForm) values() url.Values {
	v := url.Values{}
	set(v, "by", f.By, "content")
	set(v, "min", f.Min, "0")
	set(v, "similar", f.Similar, "0.5")
	set(v, "under", f.Under, "")
	return v
}

func (s *server) dirGroups(ctx context.Context) ([]dirGroupView, error) {
	return cached(ctx, s, "dir groups", func() ([]dirGroupView, error) {
		groups, err := contentGroups(ctx, s.db)
		if err != nil {
			return nil, err
		}
		out := make([]dirGroupView, len(groups))
		for i, g := range groups {
			out[i] = dirGroupView{Dirs: g.dirs, Files: g.files, Bytes: g.bytes, Waste: g.waste()}
		}
		return out, nil
	})
}

func (s *server) pairs(ctx context.Context, similar float64) ([]pairView, error) {
	key := "pairs " + strconv.FormatFloat(similar, 'f', -1, 64)
	return cached(ctx, s, key, func() ([]pairView, error) {
		pairs, _, err := namePairs(ctx, s.db, similar, 50)
		if err != nil {
			return nil, err
		}
		out := make([]pairView, len(pairs))
		for i, p := range pairs {
			out[i] = pairView{
				Name: p.name, Score: p.score, Common: p.common, CommonBytes: p.commonBytes,
				A: dirView{p.a.path, p.a.files, p.a.bytes},
				B: dirView{p.b.path, p.b.files, p.b.bytes},
			}
		}
		return out, nil
	})
}

// dirsAll returns every group or pair the form asks for, in order.
func (s *server) dirsAll(r *http.Request) (dirsForm, []dirGroupView, []pairView, error) {
	ctx := r.Context()
	f := dirsForm{
		By:      formValue(r, "by", "content"),
		Min:     formValue(r, "min", "0"),
		Similar: formValue(r, "similar", "0.5"),
		Under:   strings.TrimSpace(r.FormValue("under")),
	}
	minSize, err := parseSize(f.Min)
	if err != nil {
		return f, nil, nil, usagef("min %s: %v", f.Min, err)
	}
	scope, err := s.scope(ctx, f.Under)
	if err != nil {
		return f, nil, nil, err
	}
	switch f.By {
	case "content":
		all, err := s.dirGroups(ctx)
		if err != nil {
			return f, nil, nil, err
		}
		var groups []dirGroupView
		for _, g := range all {
			if g.Bytes >= minSize && anyIn(scope, g.Dirs...) {
				groups = append(groups, g)
			}
		}
		return f, groups, nil, nil
	case "name":
		similar, err := strconv.ParseFloat(f.Similar, 64)
		if err != nil || similar < 0 || similar > 1 {
			return f, nil, nil, usagef("similar %s: a share from 0 to 1", f.Similar)
		}
		all, err := s.pairs(ctx, similar)
		if err != nil {
			return f, nil, nil, err
		}
		var pairs []pairView
		for _, p := range all {
			if max(p.A.Bytes, p.B.Bytes) >= minSize && anyIn(scope, p.A.Path, p.B.Path) {
				pairs = append(pairs, p)
			}
		}
		return f, nil, pairs, nil
	}
	return f, nil, nil, usagef("by %s: content or name", f.By)
}

func (s *server) dirsPage(r *http.Request) (dirsPage, error) {
	form, groups, pairs, err := s.dirsAll(r)
	p := dirsPage{Form: form, Offset: offset(r)}
	p.CSV, p.JSON = exportURLs("/dirs/export", form.values())
	if err != nil {
		return p, err
	}
	p.Total = len(groups) + len(pairs)
	end := min(p.Offset+pageSize, p.Total)
	start := min(p.Offset, end)
	if groups != nil {
		if p.Views, err = s.dgViews(r.Context(), start+1, groups[start:end]); err != nil {
			return p, err
		}
	} else {
		p.Pairs = pairs[start:end]
	}
	if end < p.Total {
		v := form.values()
		v.Set("offset", strconv.Itoa(end))
		p.More, p.Next = "/dirs/more?"+v.Encode(), "/dirs?"+v.Encode()
	}
	return p, nil
}

func (s *server) dirs(w http.ResponseWriter, r *http.Request) error {
	p, err := s.dirsPage(r)
	if ue := (usageError{}); errors.As(err, &ue) {
		p.Error = err.Error()
	} else if err != nil {
		return err
	}
	return s.renderPage(w, r, "dirs.html", "directories", "dirs", p)
}

func (s *server) dirsMore(w http.ResponseWriter, r *http.Request) error {
	p, err := s.dirsPage(r)
	if err != nil {
		return err
	}
	if p.Form.By == "name" {
		return s.render(w, "pairs", p)
	}
	return s.render(w, "dgroups", p)
}

func (s *server) dirsExport(w http.ResponseWriter, r *http.Request) error {
	_, groups, pairs, err := s.dirsAll(r)
	if err != nil {
		return err
	}
	if groups != nil {
		dg := make([]dirGroup, len(groups))
		for i, g := range groups {
			dg[i] = dirGroup{dirs: g.Dirs, files: g.Files, bytes: g.Bytes}
		}
		return export(w, r, "dirs", len(dg), func(wr *writer) error {
			return writeDirGroups(wr, dg)
		})
	}
	dp := make([]dirPair, len(pairs))
	for i, p := range pairs {
		dp[i] = dirPair{
			name: p.Name, score: p.Score, common: p.Common, commonBytes: p.CommonBytes,
			a: dirSet{path: p.A.Path, files: p.A.Files, bytes: p.A.Bytes},
			b: dirSet{path: p.B.Path, files: p.B.Files, bytes: p.B.Bytes},
		}
	}
	return export(w, r, "dirs", len(dp), func(wr *writer) error {
		return writePairs(wr, dp)
	})
}

// ---------------------------------------------------------------- a directory

type crumb struct {
	Name string
	Path string
}

type entry struct {
	Name     string
	Path     string
	Files    int64 // a directory's
	Size     int64
	Modified time.Time // a file's
	Copies   int       // other files of its hash, other directories of its content
	Unique   bool      // a file without a hash: its size is no other's
	Marked   bool      // a file's, for apply to move
}

type dirPage struct {
	Path   string
	Crumbs []crumb
	Info   entry
	Marks  dirRow // what is marked below
	Known  bool
	Dirs   []entry
	Files  []entry
}

func (s *server) dir(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	path := r.FormValue("path")
	if path == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return nil
	}
	roots, err := listRoots(ctx, s.db)
	if err != nil {
		return err
	}
	if path, err = recorded(path, roots); err != nil {
		return err
	}
	p := dirPage{Path: path, Crumbs: crumbs(path, roots)}

	var sig []byte
	err = s.db.QueryRowContext(ctx, `select files, bytes, sig from dirs where path = ?`, path).
		Scan(&p.Info.Files, &p.Info.Size, &sig)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return s.renderPage(w, r, "dir.html", filepath.Base(path), "dirs", p)
	case err != nil:
		return err
	}
	p.Known = true
	if p.Marks, err = s.dirRow(ctx, path); err != nil {
		return err
	}
	if sig != nil {
		if err := s.db.QueryRowContext(ctx, `select count(*) - 1 from dirs where sig = ?`, sig).
			Scan(&p.Info.Copies); err != nil {
			return err
		}
	}

	lo, hi := under(path)
	rows, err := s.db.QueryContext(ctx, `
		select d.path, d.files, d.bytes,
			case when d.sig is null then 0
			else (select count(*) from dirs e where e.sig = d.sig) - 1 end
		from dirs d
		where d.path >= ? and d.path < ? and instr(substr(d.path, ?), ?) = 0
		order by d.bytes desc, d.path`,
		lo, hi, utf8.RuneCountInString(lo)+1, string(os.PathSeparator))
	if err != nil {
		return err
	}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.Path, &e.Files, &e.Size, &e.Copies); err != nil {
			_ = rows.Close()
			return err
		}
		e.Name = filepath.Base(e.Path)
		p.Dirs = append(p.Dirs, e)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = s.db.QueryContext(ctx, `
		select f.path, f.name, f.size, f.mtime, f.hash is null,
			case when f.hash is null then 0
			else (select count(*) from files g where g.hash = f.hash) - 1 end,
			exists (select 1 from marks m where m.path = f.path)
		from files f where f.dir = ?
		order by f.size desc, f.name`, path)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var e entry
		var mtime int64
		err := rows.Scan(&e.Path, &e.Name, &e.Size, &mtime, &e.Unique, &e.Copies, &e.Marked)
		if err != nil {
			return err
		}
		e.Modified = time.Unix(0, mtime)
		p.Files = append(p.Files, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return s.renderPage(w, r, "dir.html", filepath.Base(path), "dirs", p)
}

// crumbs returns the directories from the root a path is in down to it.
func crumbs(path string, roots []root) []crumb {
	for _, r := range roots {
		if !within(path, r.path) {
			continue
		}
		out := []crumb{{Name: r.path, Path: r.path}}
		rest := strings.TrimPrefix(strings.TrimPrefix(path, r.path), string(os.PathSeparator))
		dir := r.path
		for name := range strings.SplitSeq(rest, string(os.PathSeparator)) {
			if name == "" {
				continue
			}
			dir = filepath.Join(dir, name)
			out = append(out, crumb{Name: name, Path: dir})
		}
		return out
	}
	return []crumb{{Name: path, Path: path}}
}

type copiesPart struct {
	Dirs  []dirView
	Files []fileInfo
}

// copies serves the list of the other copies of a file or a directory, which
// the page of a directory opens under it.
func (s *server) copies(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var p copiesPart
	if dir := r.FormValue("dir"); dir != "" {
		rows, err := s.db.QueryContext(ctx, `
			select path, files, bytes from dirs
			where sig = (select sig from dirs where path = ?) and path <> ?
			order by path`, dir, dir)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var d dirView
			if err := rows.Scan(&d.Path, &d.Files, &d.Bytes); err != nil {
				return err
			}
			p.Dirs = append(p.Dirs, d)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return s.render(w, "copies", p)
	}
	path := r.FormValue("path")
	files, err := listFiles(ctx, s.db, `
		select path, size, mtime from files
		where hash = (select hash from files where path = ?) and path <> ?
		order by path`, path, path)
	if err != nil {
		return err
	}
	p.Files = files
	return s.render(w, "copies", p)
}

// ---------------------------------------------------------------- helpers

// scope returns the path of an "under" field as the database has it, or
// none for an empty field.
func (s *server) scope(ctx context.Context, under string) ([]string, error) {
	if under == "" {
		return nil, nil
	}
	roots, err := listRoots(ctx, s.db)
	if err != nil {
		return nil, err
	}
	p, err := recorded(under, roots)
	if err != nil {
		return nil, usagef("under %s: %v", under, err)
	}
	return []string{p}, nil
}

// anyIn reports whether one of the paths is in the scope.
func anyIn(scope []string, paths ...string) bool {
	for _, p := range paths {
		if inAny(scope, p) {
			return true
		}
	}
	return false
}

func formValue(r *http.Request, name, def string) string {
	if v := strings.TrimSpace(r.FormValue(name)); v != "" {
		return v
	}
	return def
}

// set puts a field into a query unless it has its default value, so that
// the URLs stay short.
func set(v url.Values, name, value, def string) {
	if value != "" && value != def {
		v.Set(name, value)
	}
}

func offset(r *http.Request) int {
	n, err := strconv.Atoi(r.FormValue("offset"))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
