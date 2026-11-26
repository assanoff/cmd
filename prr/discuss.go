// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// A discussion is a thread of comments on the host, placed where review
// can show it.
type discussion struct {
	ID       string // GitLab's id, which a reply to it needs
	File     string // "" for the request as a whole
	Line     int    // in the head; 0 for the file as a whole
	Where    string // where it was written, when that is no line of the head
	Quote    string // the text of the line it was written on, if gone since
	Resolved bool
	Notes    []note
}

type note struct {
	Author, Body, URL string
}

// discussions gets the discussions of the request from the host, the
// system notes of GitLab left out.
func (p *PR) discussions(ctx context.Context) ([]*discussion, error) {
	if p.Kind == github {
		return p.githubDiscussions(ctx)
	}
	type position struct {
		NewPath string `json:"new_path"`
		OldPath string `json:"old_path"`
		NewLine int    `json:"new_line"`
		OldLine int    `json:"old_line"`
		HeadSHA string `json:"head_sha"`
	}
	type glNote struct {
		ID       int64
		Body     string
		System   bool
		Resolved bool
		Author   struct{ Username string }
		Position *position
	}
	all, err := list[struct {
		ID    string
		Notes []glNote
	}](ctx, p, p.apiPath()+"/discussions")
	if err != nil {
		return nil, err
	}
	var ds []*discussion
	for _, gd := range all {
		if len(gd.Notes) == 0 || gd.Notes[0].System {
			continue
		}
		first := gd.Notes[0]
		d := &discussion{ID: gd.ID, Resolved: first.Resolved}
		if pos := first.Position; pos != nil {
			switch {
			case pos.NewLine > 0:
				p.place(ctx, d, pos.NewPath, pos.NewLine, pos.HeadSHA)
			case pos.OldLine > 0:
				d.File, d.Where = pos.OldPath, fmt.Sprintf("removed line %d", pos.OldLine)
			default:
				d.File = pos.NewPath
			}
		}
		for _, n := range gd.Notes {
			if !n.System {
				u := fmt.Sprintf("%s#note_%d", p.URL, n.ID)
				d.Notes = append(d.Notes, note{n.Author.Username, n.Body, u})
			}
		}
		ds = append(ds, d)
	}
	return ds, nil
}

// githubDiscussions gets the review comments, threaded by the comment they
// answer, and the comments on the conversation, each a thread of its own.
func (p *PR) githubDiscussions(ctx context.Context) ([]*discussion, error) {
	type ghUser struct{ Login string }
	comments, err := list[struct {
		ID             int64
		InReplyTo      int64 `json:"in_reply_to_id"`
		Body, Path     string
		Side           string
		Line           int
		OriginalLine   int    `json:"original_line"`
		CommitID       string `json:"commit_id"`
		OriginalCommit string `json:"original_commit_id"`
		HTMLURL        string `json:"html_url"`
		User           ghUser
	}](ctx, p, p.apiPath()+"/comments")
	if err != nil {
		return nil, err
	}
	var ds []*discussion
	byID := map[int64]*discussion{}
	for _, c := range comments {
		n := note{c.User.Login, c.Body, c.HTMLURL}
		if d := byID[c.InReplyTo]; d != nil {
			d.Notes = append(d.Notes, n)
			continue
		}
		d := &discussion{Notes: []note{n}}
		switch {
		case c.Side == "LEFT":
			d.File, d.Where = c.Path, fmt.Sprintf("removed line %d", c.OriginalLine)
		case c.Line > 0:
			p.place(ctx, d, c.Path, c.Line, c.CommitID)
		default: // outdated
			p.place(ctx, d, c.Path, c.OriginalLine, c.OriginalCommit)
		}
		byID[c.ID] = d
		ds = append(ds, d)
	}
	issue, err := list[struct {
		Body    string
		HTMLURL string `json:"html_url"`
		User    ghUser
	}](ctx, p, fmt.Sprintf("repos/%s/issues/%d/comments", p.Repo, p.N))
	if err != nil {
		return nil, err
	}
	for _, c := range issue {
		ds = append(ds, &discussion{Notes: []note{{c.User.Login, c.Body, c.HTMLURL}}})
	}
	return ds, nil
}

// place puts a discussion written on a line of file at commit sha on the
// same line of the head: the line with the same text nearest to where it
// was. When the text is gone, the discussion goes on the file, with the
// place it was written on and the text there.
func (p *PR) place(ctx context.Context, d *discussion, file string, line int, sha string) {
	d.File = file
	if line <= 0 {
		return
	}
	if sha == p.Head {
		d.Line = line
		return
	}
	d.Where = fmt.Sprintf("line %d at %.8s", line, sha)
	old := p.lines(ctx, sha, file)
	if line > len(old) {
		return
	}
	if n := nearest(p.lines(ctx, p.Head, file), old[line-1], line); n > 0 {
		d.Line, d.Where = n, ""
		return
	}
	d.Quote = old[line-1]
}

// lines returns the lines of file at commit sha: from the clone, or, for a
// commit it lacks, such as the head before a force push, from GitLab.
func (p *PR) lines(ctx context.Context, sha, file string) []string {
	cmd := command(ctx, p.gitDir(), "git", "show", sha+":"+file)
	data, err := cmd.Output()
	if err != nil && p.Kind == gitlab {
		var s string
		s, err = p.call(ctx, "GET", fmt.Sprintf("projects/%s/repository/files/%s/raw?ref=%s",
			url.PathEscape(p.Repo), url.PathEscape(file), sha), nil)
		data = []byte(s)
	}
	if err != nil {
		return nil
	}
	return strings.Split(string(data), "\n")
}

// nearest returns the line of lines with text that is nearest to line n,
// or 0 when there is none. A blank line says nothing about where it is.
func nearest(lines []string, text string, n int) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	best := 0
	for i, l := range lines {
		if l == text && (best == 0 || dist(i+1, n) < dist(best, n)) {
			best = i + 1
		}
	}
	return best
}

func dist(a, b int) int {
	return max(a-b, b-a)
}

// hostLink is the link to the comment on the host that ends a comment
// prr imports, which is how a later run knows it is there.
var hostLink = regexp.MustCompile(`\n\[(?:GitLab|GitHub)\]\((\S+)\)\s*$`)

// text is the body of an imported comment: what was written, then the link.
func (n note) text(kind string) string {
	label := "GitLab"
	if kind == github {
		label = "GitHub"
	}
	return strings.TrimSpace(n.Body) + "\n\n[" + label + "](" + n.URL + ")"
}

// importDiscussions brings the discussions of the request on the host into
// review: a thread for each, with the comments of its authors, placed on
// the line of the head they are about. Comments already brought in by an
// earlier run are left alone, so running again adds only what is new.
func (p *PR) importDiscussions(ctx context.Context, wt string) error {
	ds, err := p.discussions(ctx)
	if err != nil {
		return err
	}
	ts, err := threads(ctx, wt, true)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	threadOf := map[string]int64{}
	for _, t := range ts {
		for i, c := range t.Comments {
			if m := hostLink.FindStringSubmatch(c.Body); m != nil && c.FromAgent {
				seen[m[1]] = true
				if i == 0 {
					threadOf[m[1]] = t.ID
				}
			}
		}
	}
	var added, replies int
	for _, d := range ds {
		id, ok := threadOf[d.Notes[0].URL]
		rest := d.Notes
		if !ok {
			if id, err = p.addThread(ctx, wt, d); err != nil {
				return err
			}
			added++
			rest = rest[1:]
			if d.Resolved {
				if _, err := output(command(ctx, wt, "review", "resolve", strconv.FormatInt(id, 10))); err != nil {
					return err
				}
			}
		}
		for _, n := range rest {
			if seen[n.URL] {
				continue
			}
			cmd := command(ctx, wt, "review", "reply", "-from", author(n), strconv.FormatInt(id, 10))
			cmd.Stdin = strings.NewReader(n.text(p.Kind))
			if _, err := output(cmd); err != nil {
				return err
			}
			replies++
		}
	}
	if added+replies > 0 {
		fmt.Printf("\timported %d discussions and %d replies from %s\n", added, replies, p.Host)
	}
	return nil
}

func author(n note) string {
	if n.Author == "" {
		return "unknown"
	}
	return n.Author
}

// addThread starts the thread of a discussion in review: on its line, or
// on its file when review will not take the line, or on the description
// when the file is not in the change either.
func (p *PR) addThread(ctx context.Context, wt string, d *discussion) (int64, error) {
	body := d.Notes[0].text(p.Kind)
	if d.Quote != "" {
		body = "> " + d.Quote + "\n\n" + body
	}
	if d.Where != "" {
		body = "(" + d.Where + ")\n\n" + body
	}
	var targets []string
	if d.File != "" && d.Line > 0 {
		targets = append(targets, fmt.Sprintf("%s:%d", d.File, d.Line))
	}
	if d.File != "" {
		targets = append(targets, d.File)
	}
	targets = append(targets, "/COMMIT_MSG")
	var err error
	for _, target := range targets {
		b := body
		if target == "/COMMIT_MSG" && d.File != "" {
			b = "`" + d.File + "`\n\n" + body
		}
		cmd := command(ctx, wt, "review", "add", "-from", author(d.Notes[0]), p.changeID(), target)
		cmd.Stdin = strings.NewReader(b)
		var out string
		if out, err = output(cmd); err == nil {
			var id int64
			_, err = fmt.Sscanf(out, "added thread %d", &id)
			return id, err
		}
	}
	return 0, err
}
