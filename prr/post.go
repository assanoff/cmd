// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A thread is a comment thread as review comments -json prints it: the
// published comments of an unresolved thread.
type thread struct {
	ID          int64
	File        string
	Side        string // "new", the head, or "old", the merge base
	Line        int
	CurrentLine int `json:"current_line"`
	Stale       bool
	Anchor      string
	Comments    []comment
}

type comment struct {
	Author    string
	FromAgent bool `json:"from_agent"`
	Body      string
}

// agentMark addresses a comment to the agent rather than to the author.
var agentMark = regexp.MustCompile(`(^|\s)@(c|claude)\b`)

// toAgent reports whether a comment of the reviewer calls the agent.
func (c comment) toAgent() bool {
	return !c.FromAgent && agentMark.MatchString(c.Body)
}

// forAgent reports whether a thread is a conversation with the agent, all
// of which stays here: one the agent started, or one the reviewer started
// by calling it. In any other thread only the comments that call the agent
// are for it.
func (t *thread) forAgent() bool {
	if len(t.Comments) == 0 {
		return false
	}
	first := t.Comments[0]
	return first.toAgent() || first.FromAgent && t.imported() == ""
}

// waiting reports whether a thread waits for the agent to answer: its last
// comment is the reviewer's, in a conversation with the agent or calling it.
func (t *thread) waiting() bool {
	n := len(t.Comments)
	if n == 0 || t.Comments[n-1].FromAgent {
		return false
	}
	return t.forAgent() || t.Comments[n-1].toAgent()
}

// imported returns the link to the discussion on the host a thread was
// imported from, or "" for a thread started here.
func (t *thread) imported() string {
	if len(t.Comments) == 0 || !t.Comments[0].FromAgent {
		return ""
	}
	if m := hostLink.FindStringSubmatch(t.Comments[0].Body); m != nil {
		return m[1]
	}
	return ""
}

func isImported(t *thread) bool {
	return t.imported() != ""
}

// note is what of a thread goes to the author: the comments of the
// reviewer that do not call the agent, or "" for a conversation with it.
func (t *thread) note() string {
	if t.forAgent() {
		return ""
	}
	var parts []string
	for _, c := range t.Comments {
		if !c.FromAgent && !c.toAgent() {
			parts = append(parts, strings.TrimSpace(c.Body))
		}
	}
	return strings.Join(parts, "\n\n")
}

// position returns the line the thread is on, in the head or, if old, in
// the merge base; ok is false for one on no line of a file: on the
// description, on a whole file, or on a line since gone.
func (t *thread) position() (line int, old, ok bool) {
	if t.File == "/COMMIT_MSG" || t.Stale || t.Line <= 0 {
		return 0, false, false
	}
	if t.Side == "old" {
		return t.Line, true, true
	}
	return t.CurrentLine, false, t.CurrentLine > 0
}

// where names the place of the thread: file:line, file or description.
func (t *thread) where() string {
	if u := t.imported(); u != "" {
		return "reply to " + u
	}
	if t.File == "/COMMIT_MSG" {
		return "description"
	}
	if line, _, ok := t.position(); ok {
		return fmt.Sprintf("%s:%d", t.File, line)
	}
	return t.File
}

// quoted is the note with its place written out, for a host that will not
// take the note on its line or in its discussion.
func (t *thread) quoted() string {
	if u := t.imported(); u != "" {
		return "Re " + u + "\n\n" + t.note()
	}
	s := "`" + t.where() + "`\n"
	if t.Anchor != "" {
		s += "> " + t.Anchor + "\n"
	}
	return s + "\n" + t.note()
}

// threads returns the threads of the review in wt with their published
// comments: the unresolved ones, or with all the resolved ones too.
func threads(ctx context.Context, wt string, all bool) ([]*thread, error) {
	cmd := command(ctx, wt, "review", "comments", "-json")
	if all {
		cmd.Args = append(cmd.Args, "-all")
	}
	out, err := output(cmd)
	if err != nil {
		return nil, err
	}
	var ts []*thread
	return ts, json.Unmarshal([]byte(out), &ts)
}

// resolve answers a thread from prr and resolves it.
func resolve(ctx context.Context, wt string, t *thread, text string) error {
	id := strconv.FormatInt(t.ID, 10)
	_, err := output(command(ctx, wt, "review", "reply", "-from", "prr", "-resolve", id, text))
	return err
}

func cmdWait(ctx context.Context, ref string) error {
	_, wt, err := locate(ctx, ref)
	if err != nil {
		return err
	}
	for {
		ts, err := threads(ctx, wt, false)
		if err != nil {
			return err
		}
		var ids []string
		for _, t := range ts {
			if t.waiting() {
				ids = append(ids, strconv.FormatInt(t.ID, 10))
			}
		}
		if len(ids) > 0 {
			fmt.Printf("threads waiting for the agent in %s: %s\n", wt, strings.Join(ids, " "))
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func cmdPost(ctx context.Context, ref string, yes bool) error {
	p, wt, err := locate(ctx, ref)
	if err != nil {
		return err
	}
	if err := p.load(ctx); err != nil {
		return err
	}
	if head := trailer(ctx, wt, "PR-Head"); head != p.Head {
		return fmt.Errorf("%s has moved since the review (%.10s, now %.10s): "+
			"run prr review %s and look at your comments again", p.Ref(), head, p.Head, p.URL)
	}
	ts, err := threads(ctx, wt, false)
	if err != nil {
		return err
	}
	var notes []*thread
	for _, t := range ts {
		if t.note() == "" {
			continue
		}
		notes = append(notes, t)
		first, _, _ := strings.Cut(t.note(), "\n")
		fmt.Printf("[%d] %s\t%s\n", t.ID, t.where(), first)
	}
	if len(notes) == 0 {
		fmt.Println("nothing to post")
		return nil
	}
	if !yes {
		fmt.Printf("\n%d threads; prr post -y writes them into %s as a review you submit there\n",
			len(notes), p.URL)
		return nil
	}
	if p.Kind == github {
		err = p.postGitHub(ctx, wt, notes)
	} else {
		err = p.postGitLab(ctx, wt, notes)
	}
	if err != nil {
		return err
	}
	fmt.Printf("posted as drafts: submit the review at %s\n", p.URL)
	return nil
}

// postGitLab writes each note as a draft note of the merge request: on its
// line where GitLab takes it there, on the merge request otherwise. The
// drafts are seen by no one until the review is submitted on GitLab.
func (p *PR) postGitLab(ctx context.Context, wt string, ts []*thread) error {
	drafts := p.apiPath() + "/draft_notes"
	discussion := map[string]string{} // by the link to its first note
	if slices.ContainsFunc(ts, isImported) {
		ds, err := p.discussions(ctx)
		if err != nil {
			return err
		}
		for _, d := range ds {
			discussion[d.Notes[0].URL] = d.ID
		}
	}
	for _, t := range ts {
		var err error
		if id := discussion[t.imported()]; id != "" {
			err = p.api(ctx, "POST", drafts, map[string]any{
				"note": t.note(), "in_reply_to_discussion_id": id,
			}, nil)
			if err == nil {
				err = resolve(ctx, wt, t, "Sent to GitLab as a draft reply.")
			}
			if err != nil {
				return err
			}
			continue
		}
		if line, old, ok := t.position(); ok && t.imported() == "" {
			key := "new_line"
			if old {
				key = "old_line"
			}
			err = p.api(ctx, "POST", drafts, map[string]any{
				"note": t.note(),
				"position": map[string]any{
					"position_type": "text",
					"base_sha":      p.BaseSHA,
					"start_sha":     p.StartSHA,
					"head_sha":      p.Head,
					"old_path":      t.File,
					"new_path":      t.File,
					key:             line,
				},
			}, nil)
		}
		if _, _, ok := t.position(); !ok || t.imported() != "" || err != nil {
			// GitLab takes a line only where the diff shows it.
			err = p.api(ctx, "POST", drafts, map[string]any{"note": t.quoted()}, nil)
		}
		if err != nil {
			return err
		}
		if err := resolve(ctx, wt, t, "Sent to GitLab as a draft note."); err != nil {
			return err
		}
	}
	return nil
}

// postGitHub writes the notes as one pending review of the pull request,
// seen by no one until it is submitted on GitHub.
func (p *PR) postGitHub(ctx context.Context, wt string, ts []*thread) error {
	var comments []map[string]any
	var body []string
	for _, t := range ts {
		line, old, ok := t.position()
		if !ok || t.imported() != "" {
			body = append(body, t.quoted())
			continue
		}
		side := "RIGHT"
		if old {
			side = "LEFT"
		}
		comments = append(comments, map[string]any{
			"path": t.File, "line": line, "side": side, "body": t.note(),
		})
	}
	reviews := p.apiPath() + "/reviews"
	err := p.api(ctx, "POST", reviews, map[string]any{
		"commit_id": p.Head, "body": strings.Join(body, "\n\n---\n\n"), "comments": comments,
	}, nil)
	if err != nil && len(comments) > 0 {
		// GitHub takes a line only within the hunks of the diff, and
		// refuses the whole review over one that is not.
		var all []string
		for _, t := range ts {
			all = append(all, t.quoted())
		}
		err = p.api(ctx, "POST", reviews, map[string]any{
			"commit_id": p.Head, "body": strings.Join(all, "\n\n---\n\n"),
		}, nil)
	}
	if err != nil {
		return err
	}
	for _, t := range ts {
		if err := resolve(ctx, wt, t, "Sent to GitHub in a pending review."); err != nil {
			return err
		}
	}
	return nil
}
