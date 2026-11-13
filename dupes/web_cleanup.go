// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The pages of cleaning up: marks on the groups of files and directories,
// the list of what is marked with the button that moves it, and the history
// of what was moved. The marking and moving are those of the mark and apply
// commands, so what a page does, a command can do as well.

// vals writes pairs of names and values as the JSON of an hx-vals.
func vals(kv ...string) (string, error) {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	b, err := json.Marshal(m)
	return string(b), err
}

// fgView is a group of files on a page, which marks only files of the same
// content.
type fgView struct {
	Rank int
	G    fileGroup
	Hash bool
}

// toggleView is the mark of one file, which replaces itself when clicked.
type toggleView struct {
	Path   string
	Marked bool
	Err    string
}

// dirRow is a directory of a group with what is marked under it.
type dirRow struct {
	Path        string
	Marked      int64
	MarkedBytes int64
}

// dgView is a group of directories on a page.
type dgView struct {
	Rank int
	G    dirGroupView
	Rows []dirRow
}

// markGroups sets Marked on the files of the groups.
func (s *server) markGroups(ctx context.Context, groups []fileGroup) error {
	var paths []string
	for _, g := range groups {
		for _, f := range g.Files {
			paths = append(paths, f.Path)
		}
	}
	set, err := s.markedSet(ctx, paths)
	if err != nil {
		return err
	}
	for _, g := range groups {
		for i := range g.Files {
			g.Files[i].Marked = set[g.Files[i].Path]
		}
	}
	return nil
}

// markedSet returns which of the paths are marked.
func (s *server) markedSet(ctx context.Context, paths []string) (map[string]bool, error) {
	set := map[string]bool{}
	for chunk := range slices.Chunk(paths, 500) {
		args := make([]any, len(chunk))
		for i, p := range chunk {
			args[i] = p
		}
		rows, err := s.db.QueryContext(ctx, `select path from marks where path in (?`+
			strings.Repeat(", ?", len(chunk)-1)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				_ = rows.Close()
				return nil, err
			}
			set[p] = true
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return set, nil
}

// dirRow returns what is marked under dir.
func (s *server) dirRow(ctx context.Context, dir string) (dirRow, error) {
	d := dirRow{Path: dir}
	lo, hi := under(dir)
	err := s.db.QueryRowContext(ctx, `
		select count(*), coalesce(sum(f.size), 0)
		from marks m join files f on f.path = m.path
		where m.path >= ? and m.path < ?`, lo, hi).Scan(&d.Marked, &d.MarkedBytes)
	return d, err
}

// dgViews turns groups of directories from rank first on into views.
func (s *server) dgViews(ctx context.Context, first int, groups []dirGroupView) ([]dgView, error) {
	out := make([]dgView, len(groups))
	for i, g := range groups {
		out[i] = dgView{Rank: first + i, G: g}
		for _, d := range g.Dirs {
			row, err := s.dirRow(ctx, d)
			if err != nil {
				return nil, err
			}
			out[i].Rows = append(out[i].Rows, row)
		}
	}
	return out, nil
}

// hashGroup returns the group of the copies of the file at path.
func (s *server) hashGroup(ctx context.Context, path string) (fileGroup, error) {
	var g fileGroup
	var hash []byte
	err := s.db.QueryRowContext(ctx, `select hash from files where path = ?`, path).Scan(&hash)
	if err != nil {
		return g, err
	}
	g.Key = hex.EncodeToString(hash)
	g.Files, err = listFiles(ctx, s.db,
		`select path, size, mtime from files where hash = ? order by path`, hash)
	if err != nil {
		return g, err
	}
	var sum int64
	for _, f := range g.Files {
		sum += f.Size
		g.Size = max(g.Size, f.Size)
	}
	g.Count, g.Wasted = len(g.Files), sum-g.Size
	return g, s.markGroups(ctx, []fileGroup{g})
}

func rank(r *http.Request) int {
	n, _ := strconv.Atoi(r.FormValue("rank"))
	return n
}

// markFile marks or unmarks one file and answers with its new mark.
func (s *server) markFile(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	v := toggleView{Path: r.FormValue("path")}
	if r.FormValue("on") == "1" {
		st, err := markCopies(ctx, s.db, v.Path, "", false)
		if err != nil {
			return err
		}
		if st.marked == 0 {
			v.Err = "the last copy: every other one is marked"
			if st.skipped == 0 {
				v.Err = "no copies of it are known"
			}
		}
	} else if _, err := unmark(ctx, s.db, v.Path); err != nil {
		return err
	}
	set, err := s.markedSet(ctx, []string{v.Path})
	if err != nil {
		return err
	}
	v.Marked = set[v.Path]
	return s.render(w, "toggle", v)
}

// keepFile marks every copy of a file but itself and answers with its group.
func (s *server) keepFile(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	path := r.FormValue("path")
	if err := keepOnly(ctx, s.db, path); err != nil {
		return err
	}
	g, err := s.hashGroup(ctx, path)
	if err != nil {
		return err
	}
	return s.render(w, "fgroup", fgView{Rank: rank(r), G: g, Hash: true})
}

// markDir keeps one directory of a group, marking what the others have of
// it, or unmarks one, and answers with the group.
func (s *server) markDir(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	dir := r.FormValue("dir")
	groups, err := s.dirGroups(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(groups, func(g dirGroupView) bool {
		return slices.Contains(g.Dirs, dir)
	})
	if i < 0 {
		return usagef("%s is in no group of copies any more: reload", dir)
	}
	g := groups[i]
	if r.FormValue("keep") == "1" {
		for _, d := range g.Dirs {
			if d == dir {
				continue
			}
			if _, err := markCopies(ctx, s.db, d, dir, false); err != nil {
				return err
			}
		}
	} else if _, err := unmark(ctx, s.db, dir); err != nil {
		return err
	}
	views, err := s.dgViews(ctx, rank(r), []dirGroupView{g})
	if err != nil {
		return err
	}
	return s.render(w, "dgroup", views[0])
}

// ---------------------------------------------------------------- marked

type markRow struct {
	Path, Keep string
	Size       int64
}

type marksPage struct {
	Total, Bytes int64
	Offset       int
	Rows         []markRow
	More, Next   string
	Keep, From   string // the fields of the rule
	Msg, Error   string
	Job          *applyJob
}

func (s *server) marksPage(r *http.Request) (marksPage, error) {
	ctx := r.Context()
	p := marksPage{Offset: offset(r), Job: s.jobView()}
	var err error
	if p.Total, p.Bytes, err = markTotals(ctx, s.db); err != nil {
		return p, err
	}
	rows, err := s.db.QueryContext(ctx, `
		select m.path, m.keep, coalesce(f.size, 0)
		from marks m left join files f on f.path = m.path
		order by f.size desc, m.path limit ? offset ?`, pageSize, p.Offset)
	if err != nil {
		return p, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var m markRow
		if err := rows.Scan(&m.Path, &m.Keep, &m.Size); err != nil {
			return p, err
		}
		p.Rows = append(p.Rows, m)
	}
	if next := p.Offset + len(p.Rows); int64(next) < p.Total {
		q := "offset=" + strconv.Itoa(next)
		p.More, p.Next = "/marks/more?"+q, "/marks?"+q
	}
	return p, rows.Err()
}

func (s *server) marks(w http.ResponseWriter, r *http.Request) error {
	p, err := s.marksPage(r)
	if err != nil {
		return err
	}
	return s.renderPage(w, r, "marks.html", "marked", "marks", p)
}

func (s *server) marksMore(w http.ResponseWriter, r *http.Request) error {
	p, err := s.marksPage(r)
	if err != nil {
		return err
	}
	return s.render(w, "mrows", p)
}

// rule marks the files under one path that have a copy kept under another,
// or anywhere when the other is empty, and answers with the page of marks.
func (s *server) rule(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	keepArg := strings.TrimSpace(r.FormValue("keep"))
	fromArg := strings.TrimSpace(r.FormValue("from"))
	msg, ruleErr := func() (string, error) {
		if fromArg == "" {
			return "", usagef("say where the copies to mark are")
		}
		roots, err := listRoots(ctx, s.db)
		if err != nil {
			return "", err
		}
		from, err := recorded(fromArg, roots)
		if err != nil {
			return "", usagef("%v", err)
		}
		keep := ""
		if keepArg != "" {
			if keep, err = recorded(keepArg, roots); err != nil {
				return "", usagef("%v", err)
			}
		}
		st, err := markCopies(ctx, s.db, from, keep, false)
		if err != nil {
			return "", err
		}
		msg := "marked " + plural(st.marked, "file") + ", " + formatSize(st.bytes) + " in " + from
		if st.skipped > 0 {
			msg += "; " + plural(st.skipped, "file") + " with no copy to keep left alone"
		}
		return msg, nil
	}()
	p, err := s.marksPage(r)
	if err != nil {
		return err
	}
	p.Keep, p.From, p.Msg = keepArg, fromArg, msg
	if ue := (usageError{}); errors.As(ruleErr, &ue) {
		p.Error = ruleErr.Error()
	} else if ruleErr != nil {
		return ruleErr
	}
	return s.renderPage(w, r, "marks.html", "marked", "marks", p)
}

func (s *server) unmarkAll(w http.ResponseWriter, r *http.Request) error {
	res, err := s.db.ExecContext(r.Context(), `delete from marks`)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	p, err := s.marksPage(r)
	if err != nil {
		return err
	}
	p.Msg = "unmarked " + plural(n, "file")
	return s.renderPage(w, r, "marks.html", "marked", "marks", p)
}

// ---------------------------------------------------------------- applying

// maxSkips is how many skipped files a run lists; the rest it counts.
const maxSkips = 500

// applyJob is a run of apply that a page started, which outlives the
// request: reading every file again takes as long as the disks do.
type applyJob struct {
	Dry      bool
	Total    int
	Done     int
	Moved    int
	Bytes    int64
	Skipped  int
	Skips    []skipView
	Finished bool
	Err      string
}

type skipView struct{ Path, Reason string }

// jobView returns a copy of the last run, or nil.
func (s *server) jobView() *applyJob {
	s.jmu.Lock()
	defer s.jmu.Unlock()
	if s.job == nil {
		return nil
	}
	j := *s.job
	j.Skips = slices.Clone(j.Skips)
	return &j
}

// startJob starts a run unless one is running, and returns a copy of the
// one that is.
func (s *server) startJob(dry bool) (*applyJob, error) {
	s.jmu.Lock()
	running := s.job != nil && !s.job.Finished
	s.jmu.Unlock()
	if running {
		return s.jobView(), nil
	}
	plan, err := loadPlan(s.ctx, s.db)
	if err != nil {
		return nil, err
	}
	m, err := newMover(s.ctx, s.db, dry)
	if err != nil {
		return nil, err
	}
	job := &applyJob{Dry: dry, Total: len(plan)}
	s.jmu.Lock()
	s.job = job
	s.jmu.Unlock()
	go func() {
		st, err := m.apply(s.ctx, plan, func(o outcome) {
			s.jmu.Lock()
			defer s.jmu.Unlock()
			job.Done++
			if o.Err != nil {
				job.Skipped++
				if len(job.Skips) < maxSkips {
					job.Skips = append(job.Skips, skipView{o.Path, o.Err.Error()})
				}
				return
			}
			job.Moved++
			job.Bytes += o.Size
		})
		if st.moved > 0 && !dry {
			s.changed()
		}
		s.jmu.Lock()
		defer s.jmu.Unlock()
		job.Finished = true
		if err != nil {
			job.Err = err.Error()
		}
	}()
	return s.jobView(), nil
}

func (s *server) apply(w http.ResponseWriter, r *http.Request) error {
	j, err := s.startJob(r.FormValue("dry") == "1")
	if err != nil {
		return err
	}
	return s.render(w, "job", j)
}

func (s *server) applyStatus(w http.ResponseWriter, _ *http.Request) error {
	return s.render(w, "job", s.jobView())
}

// ---------------------------------------------------------------- history

type historyPage struct {
	Total, Restored int64
	Bytes           int64 // moved and not restored
	Trashes         []string
	Offset          int
	Rows            []removal
	More, Next      string
}

func (s *server) historyPage(r *http.Request) (historyPage, error) {
	ctx := r.Context()
	p := historyPage{Offset: offset(r)}
	err := s.db.QueryRowContext(ctx, `
		select count(*), count(restored), coalesce(sum(case when restored is null then size end), 0)
		from removed`).Scan(&p.Total, &p.Restored, &p.Bytes)
	if err != nil {
		return p, err
	}
	if p.Rows, err = loadRemovals(ctx, s.db, `order by id desc limit ? offset ?`,
		pageSize, p.Offset); err != nil {
		return p, err
	}
	if next := p.Offset + len(p.Rows); int64(next) < p.Total {
		q := "offset=" + strconv.Itoa(next)
		p.More, p.Next = "/history/more?"+q, "/history?"+q
	}
	roots, err := listRoots(ctx, s.db)
	if err != nil {
		return p, err
	}
	for _, rt := range roots {
		if t := filepath.Join(rt.path, trashDir); isDir(t) {
			p.Trashes = append(p.Trashes, t)
		}
	}
	return p, nil
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func (s *server) history(w http.ResponseWriter, r *http.Request) error {
	p, err := s.historyPage(r)
	if err != nil {
		return err
	}
	return s.renderPage(w, r, "history.html", "history", "history", p)
}

func (s *server) historyMore(w http.ResponseWriter, r *http.Request) error {
	p, err := s.historyPage(r)
	if err != nil {
		return err
	}
	return s.render(w, "hrows", p)
}

// hrowView is a row of the history, with why it could not be restored.
type hrowView struct {
	removal
	Err string
}

func (s *server) restore(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		return usagef("id %q: not a number", r.FormValue("id"))
	}
	rs, err := loadRemovals(ctx, s.db, `where id = ?`, id)
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		return usagef("nothing moved has the number %d", id)
	}
	v := hrowView{removal: rs[0]}
	err = restore(ctx, s.db, v.removal)
	if sk := (skipped{}); errors.As(err, &sk) {
		v.Err = err.Error()
		return s.render(w, "hrow", v)
	}
	if err != nil {
		return err
	}
	if err := buildDirs(ctx, s.db); err != nil {
		return err
	}
	s.changed()
	rs, err = loadRemovals(ctx, s.db, `where id = ?`, id)
	if err != nil {
		return err
	}
	v.removal = rs[0]
	return s.render(w, "hrow", v)
}

// ---------------------------------------------------------------- showing

// open shows a scanned file or directory, or a trash, in the file manager.
func (s *server) open(w http.ResponseWriter, r *http.Request) error {
	path := r.FormValue("path")
	roots, err := listRoots(r.Context(), s.db)
	if err != nil {
		return err
	}
	scanned := func(rt root) bool { return within(path, rt.path) }
	if !slices.ContainsFunc(roots, scanned) {
		return usagef("%s is in no scanned path", path)
	}
	if err := reveal(path); err != nil {
		return usagef("%v", err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
