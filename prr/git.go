// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// home is where prr keeps its clones, worktrees and pages: ~/prr, or $PRRHOME.
func home() string {
	if d := os.Getenv("PRRHOME"); d != "" {
		return d
	}
	h, err := os.UserHomeDir()
	if err != nil {
		h = "."
	}
	return filepath.Join(h, "prr")
}

// gitDir is the bare clone of the repository of the PR, shared by all its
// PRs. It is pr's own: nobody works in it, so fetching into it disturbs
// nothing.
func (p *PR) gitDir() string {
	return filepath.Join(home(), p.Host, p.Repo+".git")
}

// worktree is where review sees the PR. review names a repository by the
// last element of its path, so this one is workspace-77.
func (p *PR) worktree() string {
	return filepath.Join(home(), p.Host, fmt.Sprintf("%s-%d", p.Repo, p.N))
}

// headRef is where the head of the PR is fetched to. It is not under
// refs/remotes: review takes what the remote has for landed, and the
// head of a PR has not landed.
func (p *PR) headRef() string {
	return fmt.Sprintf("refs/pr/%d", p.N)
}

// fetch brings the target branch and the head of the PR into the clone,
// making the clone the first time.
func (p *PR) fetch(ctx context.Context) error {
	dir := p.gitDir()
	if _, err := git(ctx, dir, "rev-parse", "--git-dir"); err != nil {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if _, err := git(ctx, dir, "init", "-q", "--bare"); err != nil {
			return err
		}
	}
	if _, err := git(ctx, dir, "remote", "get-url", "origin"); err != nil {
		if _, err := git(ctx, dir, "remote", "add", "origin", p.cloneURL(ctx)); err != nil {
			return err
		}
	}
	src := fmt.Sprintf("refs/merge-requests/%d/head", p.N)
	if p.Kind == github {
		src = fmt.Sprintf("refs/pull/%d/head", p.N)
	}
	fmt.Fprintf(os.Stderr, "prr: fetching %s\n", p.Ref())
	_, err := git(ctx, dir, "fetch", "-q", "--no-tags", "origin",
		"+refs/heads/"+p.Base+":refs/remotes/origin/"+p.Base,
		"+"+src+":"+p.headRef())
	if err != nil {
		return err
	}
	head, err := git(ctx, dir, "rev-parse", p.headRef())
	if err != nil {
		return err
	}
	if head != p.Head {
		return fmt.Errorf("%s: fetched head %.10s, the API says %.10s: try again in a moment",
			p.Ref(), head, p.Head)
	}
	return nil
}

// mergeBase is the commit the PR branched off its target branch at.
func (p *PR) mergeBase(ctx context.Context) (string, error) {
	return git(ctx, p.gitDir(), "merge-base", "refs/remotes/origin/"+p.Base, p.Head)
}

// A change is a file the PR adds (A), modifies (M) or deletes (D).
type change struct {
	Status string
	Path   string
}

// changes lists the files of the PR. A rename is a deletion and an
// addition, which is all a reader of the files needs.
func (p *PR) changes(ctx context.Context) ([]change, error) {
	base, err := p.mergeBase(ctx)
	if err != nil {
		return nil, err
	}
	out, err := git(ctx, p.gitDir(), "diff", "--name-status", "--no-renames", "-z", base, p.Head)
	if err != nil {
		return nil, err
	}
	f := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	var cs []change
	for i := 0; i+1 < len(f); i += 2 {
		cs = append(cs, change{Status: f[i][:1], Path: f[i+1]})
	}
	return cs, nil
}

// message is the commit message of the review commit: the title and the
// description of the PR, and trailers. The Change-Id comes from the URL, so
// every push to the PR is a new snapshot of the same change in review.
func (p *PR) message() string {
	var b strings.Builder
	b.WriteString(p.Title + "\n\n")
	if body := strings.TrimSpace(p.Body); body != "" {
		b.WriteString(body + "\n\n")
	}
	fmt.Fprintf(&b, "PR: %s\nPR-Head: %s\nChange-Id: %s\n", p.URL, p.Head, p.changeID())
	return b.String()
}

func (p *PR) changeID() string {
	return fmt.Sprintf("I%x", sha1.Sum([]byte(p.URL)))
}

// reviewCommit writes the commit review shows: the tree of the head on top
// of the merge base, so that the whole PR is one change however many
// commits it has. Its dates are the head's, so that running again with
// nothing pushed writes the same commit and review sees no new snapshot.
func (p *PR) reviewCommit(ctx context.Context) (string, error) {
	dir := p.gitDir()
	base, err := p.mergeBase(ctx)
	if err != nil {
		return "", err
	}
	date, err := git(ctx, dir, "show", "-s", "--format=%cI", p.Head)
	if err != nil {
		return "", err
	}
	author := p.Author
	if author == "" {
		author = p.Login
	}
	cmd := command(ctx, dir, "git", "commit-tree", p.Head+"^{tree}", "-p", base, "-F", "-")
	cmd.Stdin = strings.NewReader(p.message())
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+author, "GIT_AUTHOR_EMAIL="+p.Login+"@"+p.Host,
		"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date,
		"GIT_COMMITTER_NAME=prr", "GIT_COMMITTER_EMAIL=prr@localhost")
	return output(cmd)
}

// checkout puts commit into the worktree of the PR, making the worktree
// the first time.
func (p *PR) checkout(ctx context.Context, commit string) error {
	wt := p.worktree()
	if _, err := os.Stat(wt); err != nil {
		_, err := git(ctx, p.gitDir(), "worktree", "add", "-q", "--detach", wt, commit)
		return err
	}
	_, err := git(ctx, wt, "checkout", "-q", "--detach", commit)
	return err
}

func cmdReview(ctx context.Context, refs []string, noOpen bool) error {
	prs, err := loadAll(ctx, refs)
	if err != nil {
		return err
	}
	for _, p := range prs {
		commit, err := p.reviewCommit(ctx)
		if err != nil {
			return err
		}
		if err := p.checkout(ctx, commit); err != nil {
			return err
		}
		fmt.Printf("%s %s\n\tworktree %s\n\tchange   %s\n", p.Ref(), p.Title, p.worktree(), p.changeID())
		// The discussions go on the newest snapshot, so take it first.
		if _, err := output(command(ctx, p.worktree(), "review", "snapshot")); err != nil {
			return err
		}
		if err := p.importDiscussions(ctx, p.worktree()); err != nil {
			return fmt.Errorf("importing the discussions of %s: %w", p.Ref(), err)
		}
		cmd := command(ctx, p.worktree(), "review")
		if noOpen {
			cmd.Args = append(cmd.Args, "-n")
		}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("review: %w", err)
		}
	}
	return nil
}

// cmdRm removes the worktrees of the requests and, with forgetReview, what
// the review database keeps of them. It refuses to forget a review whose
// remarks prr post has yet to send.
func cmdRm(ctx context.Context, refs []string, forgetReview bool) error {
	var db string
	if forgetReview {
		var err error
		if db, err = reviewDB(); err != nil {
			return err
		}
	}
	for _, ref := range refs {
		p, err := parseRef(ctx, ref)
		if err != nil {
			return err
		}
		if forgetReview {
			if err := checkPosted(ctx, p); err != nil {
				return err
			}
		}
		dir := p.gitDir()
		if _, err := os.Stat(p.worktree()); err == nil {
			if _, err := git(ctx, dir, "worktree", "remove", "--force", p.worktree()); err != nil {
				return err
			}
		}
		if _, err := os.Stat(dir); err == nil {
			if _, err := git(ctx, dir, "update-ref", "-d", p.headRef()); err != nil {
				return err
			}
		}
		fmt.Printf("removed %s\n", p.worktree())
		if forgetReview {
			found, err := forget(ctx, db, p.worktree())
			if err != nil {
				return err
			}
			if found {
				fmt.Printf("forgot %s in review\n", p.worktree())
			}
		}
	}
	return nil
}

// checkPosted fails if the review of p holds remarks prr post has yet to
// send. Without the worktree there is nothing to ask review, so it passes.
func checkPosted(ctx context.Context, p *PR) error {
	if _, err := os.Stat(p.worktree()); err != nil {
		return nil
	}
	notes, err := unposted(ctx, p.worktree())
	if err != nil {
		return err
	}
	if len(notes) > 0 {
		return fmt.Errorf("%s: %d threads hold remarks not posted yet: "+
			"prr post them, or resolve them in review, before -forget", p.Ref(), len(notes))
	}
	return nil
}

// locate returns the PR and the worktree of a review: the one named, or
// with "" the one in the current directory, found by the PR trailer of
// its commit.
func locate(ctx context.Context, ref string) (*PR, string, error) {
	if ref == "" {
		ref = trailer(ctx, ".", "PR")
		if ref == "" {
			return nil, "", fmt.Errorf("not in a review of prr: name the merge or pull request")
		}
	}
	p, err := parseRef(ctx, ref)
	if err != nil {
		return nil, "", err
	}
	wt := p.worktree()
	if _, err := os.Stat(wt); err != nil {
		return nil, "", fmt.Errorf("no review of %s: run prr review %s first", p.Ref(), ref)
	}
	return p, wt, nil
}

// trailer returns the last value of a trailer in the message of HEAD in
// dir, or "".
func trailer(ctx context.Context, dir, key string) string {
	msg, err := git(ctx, dir, "log", "-1", "--format=%B")
	if err != nil {
		return ""
	}
	var v string
	for line := range strings.Lines(msg) {
		if s, ok := strings.CutPrefix(strings.TrimSpace(line), key+": "); ok {
			v = s
		}
	}
	return v
}
