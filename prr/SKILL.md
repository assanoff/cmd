---
name: prr
description: Read and review GitLab merge requests and GitHub pull requests locally with the prr command - render their Markdown in a browser (prr read), review them in the review web UI with the discussions already on the host brought in (prr review), answer the reviewer's questions in review threads (prr wait, review reply), and send the reviewer's remarks and replies to GitLab or GitHub as a draft review (prr post). Use when the user gives MR or PR links to read or review locally, asks to answer or watch the threads of a prr review, or to post the review comments of a ~/prr/ worktree.
---

# prr: reading and reviewing someone else's merge request

`prr` keeps bare clones of its own under `~/prr/`, with a worktree per
request. Running `prr`, and `review` in a `~/prr/` worktree, is how they are
changed: both fetch and write refs there, and nothing else touches them. A
worktree is someone else's change: never edit, commit or push in it.

## Reading

    prr read URL...

It is a server: run it in the background. It prints
`prr: serving ... on http://...`; give the user that URL. Related requests of
one repository go into one call: they are laid over the target branch
together, so links between their files work.

## Reviewing

    prr review URL...

Prints for each request its worktree (`~/prr/<host>/<repo>-N`), its change id
(`I…`) and the URL of review, and opens the browser (`-n` not to). It also
brings in the discussions already on the host: a thread for each, written by
their authors (shown as not from the reviewer), ending with a `[GitLab](…)` or
`[GitHub](…)` link, placed on the line of the current head with the same text,
or on the file or the description, with the old place written out, when that
line is gone. Run it again after the author pushes: the new snapshot, compared
with the last one marked reviewed, shows only what changed since, and only new
comments on the host are brought in.

A comment of the reviewer with `@c` or `@claude` is for you; one without it
is for people. A thread is one of three kinds:

- a conversation with you: you started it, or the reviewer started it by
  calling you. All of it stays here.
- a discussion from the host: the reviewer's comments in it without `@c` go
  back to that discussion as replies;
- a remark for the author: every other thread; the reviewer's comments in it
  without `@c` go to the author.

Answer where `prr wait` points: in conversations with you, and in any thread
whose last comment calls you. Nowhere else.

### Answering

1. Run `prr wait URL` in the background. It exits as soon as a thread ends
   with a published comment of the reviewer you have to answer, printing the
   thread numbers.
2. Read them in the worktree: `review comments` there. Published comments
   only: never pass `-drafts`.
3. Answer every one of them on its thread: `review reply -from claude N 'text'`
   (text on standard input when it is long). No `-resolve`: resolving is the
   reviewer's. A thread left unanswered makes `prr wait` exit again at once.
4. Run `prr wait` again, and keep the loop going until the user stops it.

You are the reviewer's assistant, not the author. Before answering, look:
the files of the worktree (the whole request at its head;
`git -C <worktree> diff HEAD~1` is its diff), the rest of the repository, the
other threads (what other reviewers already said), and the code of the
services the change touches, in the user's own checkouts and by the rules of
their CLAUDE.md. Cite `file:line`. Say you do not know rather than guess. Keep
it short, in the language of the question.

### Your own findings

When asked to go through the request first, leave each finding as a thread:
`review add -from claude <change-id> path[:line] 'text'`; `/COMMIT_MSG` is the
description. Check the imported threads first, so as not to repeat what a
reviewer already said. Findings are conversations and never go to the author;
a remark the reviewer agrees with, they write in a thread of their own.

## Posting to GitLab or GitHub

    prr post URL      # dry run: lists the remarks and replies that would go
    prr post -y URL   # writes them as a review yet to be submitted

Always run the dry run, show the user the list, and run `-y` only after their
yes to that list. It writes draft notes (GitLab; replies go into their
discussions) or a pending review (GitHub; replies are quoted in its body) that
nobody sees until the user submits the review on the host: tell them to, with
the link. Posted threads are resolved in review with a reply from `prr`. If
`prr post` says the request has moved since the review, run `prr review` again
and tell the user to look at their comments against the new snapshot.

## Cleaning up

    prr rm URL...     # removes the worktree; the clone stays
