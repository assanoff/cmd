// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// The hosts prr knows. Which one a URL is comes from its path, not its host,
// so self-hosted GitLab and GitHub Enterprise work too.
const (
	gitlab = "gitlab"
	github = "github"
)

// A PR is a GitLab merge request or a GitHub pull request.
type PR struct {
	Kind string // gitlab or github
	Host string
	Repo string // the path of the project: group/project or owner/repo
	N    int

	// Filled in by load from the API of the host.
	URL      string
	Title    string
	Body     string
	Author   string
	Login    string
	Branch   string // the source branch
	Base     string // the target branch
	Head     string // the head commit
	State    string
	BaseSHA  string // GitLab's diff_refs, which a position on a line needs
	StartSHA string
}

var (
	gitlabPath = regexp.MustCompile(`^/(.+?)/-/merge_requests/(\d+)`)
	githubPath = regexp.MustCompile(`^/([^/]+/[^/]+)/pull/(\d+)`)
)

// parseRef parses the URL of a merge or pull request, or its number in the
// repository of the current directory: 77, !77 or #77.
func parseRef(ctx context.Context, s string) (*PR, error) {
	if n, err := strconv.Atoi(strings.TrimLeft(s, "!#")); err == nil {
		remote, err := git(ctx, "", "remote", "get-url", "origin")
		if err != nil {
			return nil, fmt.Errorf("%s: no repository here to look it up in", s)
		}
		host, repo, err := parseRemote(remote)
		if err != nil {
			return nil, err
		}
		kind := gitlab
		if host == "github.com" {
			kind = github
		}
		return &PR{Kind: kind, Host: host, Repo: repo, N: n}, nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%s: not a merge or pull request", s)
	}
	if m := gitlabPath.FindStringSubmatch(u.Path); m != nil {
		n, _ := strconv.Atoi(m[2])
		return &PR{Kind: gitlab, Host: u.Host, Repo: m[1], N: n}, nil
	}
	if m := githubPath.FindStringSubmatch(u.Path); m != nil {
		n, _ := strconv.Atoi(m[2])
		return &PR{Kind: github, Host: u.Host, Repo: m[1], N: n}, nil
	}
	return nil, fmt.Errorf("%s: not a merge or pull request", s)
}

// parseRemote returns the host and the project path of a git remote:
// git@host:path.git, ssh://git@host/path.git or https://host/path.git.
func parseRemote(s string) (host, repo string, err error) {
	if !strings.Contains(s, "://") {
		user, p, ok := strings.Cut(s, ":")
		if !ok {
			return "", "", fmt.Errorf("%s: not a remote URL", s)
		}
		host = user[strings.LastIndex(user, "@")+1:]
		return host, strings.TrimSuffix(p, ".git"), nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", "", err
	}
	repo = strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	return u.Hostname(), repo, nil
}

// Ref is the short name of the PR: workspace!77, cmd#4.
func (p *PR) Ref() string {
	sep := "!"
	if p.Kind == github {
		sep = "#"
	}
	return fmt.Sprintf("%s%s%d", path.Base(p.Repo), sep, p.N)
}

// load fills in the PR from the API of its host.
func (p *PR) load(ctx context.Context) error {
	if p.Kind == github {
		var pr struct {
			HTMLURL string `json:"html_url"`
			Title   string
			Body    string
			State   string
			User    struct{ Login string }
			Head    struct{ Ref, SHA string }
			Base    struct{ Ref string }
		}
		if err := p.api(ctx, "GET", p.apiPath(), nil, &pr); err != nil {
			return err
		}
		p.URL, p.Title, p.State = pr.HTMLURL, pr.Title, pr.State
		p.Body = strings.ReplaceAll(pr.Body, "\r\n", "\n")
		p.Author, p.Login = pr.User.Login, pr.User.Login
		p.Branch, p.Head, p.Base = pr.Head.Ref, pr.Head.SHA, pr.Base.Ref
		return nil
	}
	var mr struct {
		WebURL       string `json:"web_url"`
		Title        string
		Description  string
		State        string
		SHA          string `json:"sha"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		Author       struct{ Name, Username string }
		DiffRefs     struct {
			BaseSHA  string `json:"base_sha"`
			StartSHA string `json:"start_sha"`
		} `json:"diff_refs"`
	}
	if err := p.api(ctx, "GET", p.apiPath(), nil, &mr); err != nil {
		return err
	}
	p.URL, p.Title, p.Body, p.State = mr.WebURL, mr.Title, mr.Description, mr.State
	p.Author, p.Login = mr.Author.Name, mr.Author.Username
	p.Branch, p.Head, p.Base = mr.SourceBranch, mr.SHA, mr.TargetBranch
	p.BaseSHA, p.StartSHA = mr.DiffRefs.BaseSHA, mr.DiffRefs.StartSHA
	return nil
}

// apiPath is the API path of the PR, to which the paths of its parts are
// added: /draft_notes, /reviews.
func (p *PR) apiPath() string {
	if p.Kind == github {
		return fmt.Sprintf("repos/%s/pulls/%d", p.Repo, p.N)
	}
	return fmt.Sprintf("projects/%s/merge_requests/%d", url.PathEscape(p.Repo), p.N)
}

// api makes one call to the REST API of the host and decodes the reply
// into out unless it is nil.
func (p *PR) api(ctx context.Context, method, apiPath string, body, out any) error {
	reply, err := p.call(ctx, method, apiPath, body)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal([]byte(reply), out)
}

// call makes one call to the REST API of the host through its command-line
// client, glab or gh, which keeps the credentials, and returns the reply.
func (p *PR) call(ctx context.Context, method, apiPath string, body any, flags ...string) (string, error) {
	tool := "glab"
	if p.Kind == github {
		tool = "gh"
	}
	cmd := command(ctx, "", tool, "api", "--hostname", p.Host, "-X", method, apiPath)
	cmd.Args = append(cmd.Args, flags...)
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		cmd.Args = append(cmd.Args, "--input", "-")
		cmd.Stdin = bytes.NewReader(data)
	}
	reply, err := output(cmd)
	if err != nil && p.Kind == github && method == "GET" && p.Host == "github.com" {
		// gh without a login: public repositories still answer anonymously.
		reply, err = httpGet(ctx, "https://api.github.com/"+apiPath)
	}
	return reply, err
}

// list gets every page of a list from the API of the host. glab joins the
// pages into one array and gh writes them one after another, so it reads
// arrays until there are no more.
func list[T any](ctx context.Context, p *PR, apiPath string) ([]T, error) {
	reply, err := p.call(ctx, "GET", apiPath+"?per_page=100", nil, "--paginate")
	if err != nil {
		return nil, err
	}
	var all []T
	dec := json.NewDecoder(strings.NewReader(reply))
	for dec.More() {
		var page []T
		if err := dec.Decode(&page); err != nil {
			return nil, err
		}
		all = append(all, page...)
	}
	return all, nil
}

func httpGet(ctx context.Context, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s (gh auth login for private repositories)", u, resp.Status)
	}
	return string(data), nil
}

// cloneURL is where to clone the repository from, over the protocol the
// client of the host is set to: glab says ssh unless told otherwise, gh
// https.
func (p *PR) cloneURL(ctx context.Context) string {
	proto := "ssh"
	cmd := command(ctx, "", "glab", "config", "get", "git_protocol", "--host", p.Host)
	if p.Kind == github {
		proto = "https"
		cmd = command(ctx, "", "gh", "config", "get", "git_protocol", "-h", p.Host)
	}
	if out, err := output(cmd); err == nil && out != "" {
		proto = out
	}
	if proto == "https" {
		return "https://" + p.Host + "/" + p.Repo + ".git"
	}
	return "git@" + p.Host + ":" + p.Repo + ".git"
}

// loadAll parses the PRs, loads them from their hosts and fetches each into
// its clone.
func loadAll(ctx context.Context, refs []string) ([]*PR, error) {
	if len(refs) == 0 {
		return nil, fmt.Errorf("no merge or pull request named")
	}
	var prs []*PR
	for _, ref := range refs {
		p, err := parseRef(ctx, ref)
		if err != nil {
			return nil, err
		}
		if err := p.load(ctx); err != nil {
			return nil, err
		}
		if err := p.fetch(ctx); err != nil {
			return nil, err
		}
		prs = append(prs, p)
	}
	return prs, nil
}
