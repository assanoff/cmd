// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
Prr reads and reviews GitLab merge requests and GitHub pull requests on the
local machine: read renders their Markdown in a browser, review hands them
to review (rsc.io/cmd/review), and an agent answers questions in the
threads of that review.

Usage:

	prr read [-a addr] [-n] pr...
	prr review [-n] pr...
	prr wait [pr]
	prr post [-y] [pr]
	prr rm pr...
	prr skill [-install]

A pr is the URL of a merge or pull request,

	https://git.example.com/group/project/-/merge_requests/77
	https://github.com/owner/repo/pull/12

or its number, 77, !77 or #77, in the repository of the current directory,
whose origin names the host. Which kind a URL is comes from its path, so
self-hosted GitLab and GitHub Enterprise work too. The APIs are reached
through glab and gh, which keep the credentials; gh without a login still
reads public pull requests on github.com.

# Clones

prr works in clones of its own under ~/prr ($PRRHOME): one bare clone per
repository, at ~/prr/<host>/<path>.git, cloned over the protocol glab or gh
is set to use. Nobody works in them, so prr fetches into them freely and
leaves the repositories you work in alone. Every run fetches the target
branch and the head of each request, refs/merge-requests/N/head on GitLab
and refs/pull/N/head on GitHub, so what prr shows is the request as it is
now.

# Reading

	prr read https://git.example.com/g/workspace/-/merge_requests/77 \
	        https://git.example.com/g/workspace/-/merge_requests/78

writes the tree of the target branch of each repository into ~/prr/read with
the files of the requests on top, which is what the repository will look
like once they all land, and serves it on localhost:2627, or on any free
port when that one is busy, opening a browser unless -n is given. Related
requests are best read together: links between their files work, and so do
links into the rest of the repository.

The first page lists the requests with their descriptions; the side lists
the files each one adds (A), modifies (M) or deletes (D). Markdown is
rendered and other text shown as it is. The files are other people's: their
HTML is rendered, as on GitLab and GitHub, but a content security policy
keeps any script in it from running.

# Reviewing

	prr review https://git.example.com/g/workspace/-/merge_requests/77

makes a worktree of the request, ~/prr/<host>/<path>-77, and runs review
there. review shows the commits the remote does not have yet, one change
per commit. That fits a branch of your own, not someone else's request: its
branch is on the remote already, and its commits are the history of the
work rather than parts of what it asks to merge. So the worktree holds one
commit of prr's own, the tree of the head on top of the merge base: the
whole request as one change, with its title and description as the message
and these trailers:

	PR: https://git.example.com/g/workspace/-/merge_requests/77
	PR-Head: <the head commit>
	Change-Id: I<sha1 of the URL>

The Change-Id keeps the change the same across pushes. After the author
pushes again, prr review writes a new commit for the new head, review takes
a new snapshot of the same change, and the diff against the snapshot last
marked reviewed shows only what the author has changed since, a rebase or
a force push included. Run with nothing pushed, it writes the same commit
again and review takes no snapshot. The description is the first file of
the change, /COMMIT_MSG, and takes comments like any other.

prr review brings in the discussions the request already has on the host:
a thread for each, with the comments of their authors, each ending with a
link to it on the host, which is how a later run knows what is there and
adds only what is new. review shows them as not from the reviewer. A
discussion written on a line of an older head goes on the line of the
current head with the same text, the nearest one when there are several;
when the text is gone, it goes on the file, with the place and the text it
was written on. Discussions on the request as a whole go on the
description, and those resolved on the host are resolved here.

# Talking to the agent

Comments are written in the web interface of review and published to be
seen. A comment of the reviewer that calls the agent with @c or @claude is
for the agent and never goes to the author. A thread the agent started, or
the reviewer started by calling it, is a conversation with the agent, all
of which stays here. In every other thread, a remark of the reviewer or a
discussion brought in from the host, the reviewer's comments without @c go
to the author, as a remark or a reply to the discussion, and one with @c
asks the agent about it there.

	prr wait [pr]

waits until a thread ends with a published comment of the reviewer that
the agent has to answer, one in a conversation with it or one calling it,
prints the numbers of those threads and exits, so that an
agent can run it in the background, be woken up, answer on the threads with
review reply -from claude and run it again. Without a pr it waits on the
review in the current directory.

# Posting

	prr post [-y] [pr]

lists the remarks for the author and the replies to discussions: the
unresolved threads that are not conversations with the agent, with the
reviewer's comments in each that do not call the agent. With -y it writes them into the request as a
review yet to be submitted, draft notes on GitLab and a pending review on
GitHub, on their lines where the host takes the line and with the place
quoted where it does not. A reply goes into its discussion on GitLab and is
quoted with a link to the discussion on GitHub, whose pending reviews take
no replies. Each thread is then resolved in review with a reply saying
where it went. Nobody
sees them until the review is submitted on the host. prr post refuses to run
when the request has moved since the review, as the lines would no longer
match: run prr review again first.

	prr rm pr...

removes the worktrees; the clones stay for the next time.

# The skill

	prr skill [-install]

prints the skill that teaches an agent all of the above, or with -install
writes it to ~/.claude/skills/prr/SKILL.md for Claude Code, replacing the
copy there. It is built into the binary, so after an update of prr run it
again to bring the skill up to the same version.
*/
package main
