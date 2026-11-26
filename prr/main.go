// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

const usageText = `usage:
	prr read [-a addr] [-n] pr...   serve the files of the requests, Markdown rendered
	prr review [-n] pr...           hand the requests to review, with their discussions
	prr wait [pr]                   wait until a thread of the review calls the agent
	prr post [-y] [pr]              send the remarks to the host as a draft review
	prr rm pr...                    remove the worktrees of the requests
	prr skill [-install]            print the skill for agents, or install it for Claude Code

A pr is the URL of a GitLab merge request or a GitHub pull request, or its
number (77, !77, #77) in the repository of the current directory; wait and
post default to the review in the current directory.

After installing or updating prr, run "prr skill -install" so that Claude
Code knows it. Run "go doc github.com/assanoff/cmd/prr@latest" for the whole
of it.
`

func usage() {
	fmt.Fprint(os.Stderr, usageText)
	os.Exit(2)
}

func main() {
	log.SetPrefix("prr: ")
	log.SetFlags(0)
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	name, args := os.Args[1], os.Args[2:]
	f := flag.NewFlagSet(name, flag.ExitOnError)
	f.Usage = usage
	var err error
	switch name {
	case "read":
		addr := f.String("a", "localhost:2627", "serve on `addr`")
		noOpen := f.Bool("n", false, "print the URL instead of opening a browser")
		_ = f.Parse(args)
		err = cmdRead(ctx, f.Args(), *addr, !*noOpen)
	case "review":
		noOpen := f.Bool("n", false, "print the URLs instead of opening a browser")
		_ = f.Parse(args)
		err = cmdReview(ctx, f.Args(), *noOpen)
	case "wait":
		_ = f.Parse(args)
		err = cmdWait(ctx, f.Arg(0))
	case "post":
		yes := f.Bool("y", false, "post; without it only list what would be posted")
		_ = f.Parse(args)
		err = cmdPost(ctx, f.Arg(0), *yes)
	case "rm":
		_ = f.Parse(args)
		err = cmdRm(ctx, f.Args())
	case "skill":
		install := f.Bool("install", false, "install it into ~/.claude/skills/prr")
		_ = f.Parse(args)
		err = cmdSkill(*install)
	default:
		usage()
	}
	stop()
	if err != nil {
		log.Fatal(err)
	}
}

// command returns a command that runs in dir.
func command(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd
}

// output runs cmd and returns its standard output without the space around
// it. The error carries the standard error, which is where git and the API
// clients say what went wrong.
func output(cmd *exec.Cmd) (string, error) {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args, " "), err,
			strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	return output(command(ctx, dir, "git", args...))
}
