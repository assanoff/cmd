// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package config reads ~/.config/ai/config, the flat KEY=value file that says
// which model does which job.
//
// Before this package there were five copies of the same reader, one per
// command, and they had already drifted: the same role resolved to different
// models depending on which command you asked. One copy is the point.
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Value reads one key. The process environment wins over the file, which makes
// a one-off override just:
//
//	AI_MODEL_FAST=other/model ai ask -p hello
func Value(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	once.Do(load)
	return vals[key]
}

// Int reads a key that should hold a positive number. A value that is not one
// is reported through warn and the default is used: a typo in the config
// should not stop the run, but it must not pass unnoticed either.
func Int(key string, deflt int, warn func(format string, args ...any)) int {
	v := Value(key)
	if v == "" {
		return deflt
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		warn("ignoring %s=%q: not a positive number", key, v)
		return deflt
	}
	return n
}

var (
	once sync.Once
	vals map[string]string
	path string
)

// load reads the file. A missing file is not an error: every value has a
// default or is simply unused.
func load() {
	vals = make(map[string]string)

	dir := Dir()
	if dir == "" {
		return
	}
	p := filepath.Join(dir, "ai", "config")

	f, err := os.Open(p)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	path = p

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		vals[strings.TrimSpace(key)] = val
	}
}

// Path reports the config actually in use, or "" when there is none.
func Path() string {
	once.Do(load)
	return path
}

// Dir mirrors what the dev-env deploy script uses: $XDG_CONFIG_HOME, or
// ~/.config. os.UserConfigDir is deliberately not used — on macOS it answers
// ~/Library/Application Support, which is not where the config is deployed.
func Dir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config")
}

// RepoPath locates the config inside the dev-env repository, which is the file
// worth editing: deploy copies env/.config/* over $XDG_CONFIG_HOME with rm -rf
// first, so a change made to the deployed copy is lost on the next deploy. The
// search list is the one the s dispatcher and the dev-env wrapper already use.
func RepoPath() (string, bool) {
	var candidates []string
	if root := os.Getenv("DEV_ENV_ROOT"); root != "" {
		candidates = append(candidates, root)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, "dev-env"),
			filepath.Join(home, "personal", "dev-env"),
			filepath.Join(home, "code", "personal", "dev-env"),
		)
	}

	for _, root := range candidates {
		if _, err := os.Stat(filepath.Join(root, "env", ".config")); err == nil {
			return filepath.Join(root, "env", ".config", "ai", "config"), true
		}
	}
	return "", false
}

// SetKey rewrites one KEY=value line in place and reports what was there
// before. Every comment, blank line and the ordering are kept: this is a file a
// human maintains, and rewriting it wholesale would throw away its structure.
func SetKey(p, key, value string) (before string, err error) {
	lines, err := readLines(p)
	if err != nil {
		return "", err
	}

	replaced := false
	for i, line := range lines {
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.TrimSpace(k) != key {
			continue
		}
		before = strings.Trim(strings.TrimSpace(v), `"'`)
		lines[i] = key + "=" + value
		replaced = true
		break
	}
	if !replaced {
		lines = append(lines, key+"="+value)
	}

	if dir := filepath.Dir(p); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return "", err
	}
	return before, nil
}

// readLines returns the file's lines, or an empty slice when it does not exist
// yet: rebinding a role is a reasonable way to create the config.
func readLines(p string) ([]string, error) {
	f, err := os.Open(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	// Drop a single trailing blank so appending does not accumulate them.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, nil
}
