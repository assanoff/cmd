// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// role binds a name the ai-* commands ask for to the config key that answers
// it and the default used when the config says nothing. ai-stack is the only
// command that needs the whole table: the others each know their own roles,
// while this one installs, checks and rebinds all of them.
type role struct {
	name    string
	key     string
	backend backend
	deflt   string
}

type backend int

const (
	backendLlama backend = iota // GGUF text models, llama.cpp
	backendWhisper
)

func (b backend) String() string {
	if b == backendWhisper {
		return "whisper"
	}
	return "llama"
}

var roles = []role{
	{"fast", "AI_MODEL_FAST", backendLlama, "unsloth/Qwen3-1.7B-UD-Q8_K_XL"},
	{"smart", "AI_MODEL_SMART", backendLlama, ""},
	{"code", "AI_MODEL_CODE", backendLlama, ""},
	{"embed", "AI_MODEL_EMBED", backendLlama, ""},
	{"asr", "AI_ASR_MODEL", backendWhisper, "large-v3-turbo"},
	{"asr-fast", "AI_ASR_MODEL_FAST", backendWhisper, "base"},
}

func lookupRole(name string) (role, bool) {
	for _, r := range roles {
		if r.name == name {
			return r, true
		}
	}
	return role{}, false
}

func roleNames() string {
	names := make([]string, len(roles))
	for i, r := range roles {
		names[i] = r.name
	}
	return strings.Join(names, ", ")
}

// model resolves a role to the model behind it, or "" when the role is unbound
// and has no default. An unbound role is not an error: nobody has to install a
// reranker to transcribe a lecture.
func (r role) model() string {
	if v := configValue(r.key); v != "" {
		return v
	}
	return r.deflt
}

// boundRoles returns the roles that actually resolve to a model.
func boundRoles() []role {
	var out []role
	for _, r := range roles {
		if r.model() != "" {
			out = append(out, r)
		}
	}
	return out
}

// keptModels lists models gc must leave alone even though no role names them.
// Roles are not the only way a model gets used: the Kronk server hands models
// to anything that asks it, and those callers name a model directly rather
// than going through a role. Nothing in the config would otherwise mention
// such a model, so gc would read it as garbage.
//
// The value is a comma- or space-separated list of model ids.
func keptModels() []string {
	return strings.FieldsFunc(configValue("AI_MODELS_KEEP"), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
}

// configValue reads one key. The process environment wins over the file, which
// makes a one-off override just AI_MODEL_FAST=other/model ai-stack status.
func configValue(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	configOnce.Do(loadConfig)
	return configVals[key]
}

var (
	configOnce sync.Once
	configVals map[string]string
	configPath string
)

// loadConfig reads the flat KEY=value file deployed to ~/.config/ai/config.
// A missing file is not an error: every role either has a default or is
// simply unused.
func loadConfig() {
	configVals = make(map[string]string)

	dir := configDir()
	if dir == "" {
		return
	}
	path := filepath.Join(dir, "ai", "config")

	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	configPath = path

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
		configVals[strings.TrimSpace(key)] = val
	}
}

// deployedConfig reports the path of the config actually in use, or "" when
// there is none.
func deployedConfig() string {
	configOnce.Do(loadConfig)
	return configPath
}

// configDir mirrors what the dev-env deploy script uses: $XDG_CONFIG_HOME, or
// ~/.config. os.UserConfigDir is deliberately not used — on macOS it answers
// ~/Library/Application Support, which is not where the config is deployed.
func configDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config")
}

// repoConfig locates the config inside the dev-env repository, which is the
// file worth editing: deploy copies env/.config/* over $XDG_CONFIG_HOME with
// rm -rf first, so a change made to the deployed copy is lost on the next
// deploy. The search list is the one the s dispatcher and the dev-env wrapper
// already use.
func repoConfig() (string, bool) {
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

// logger sends SDK progress to standard error. The SDK ships FmtLogger, but it
// writes to standard output, and status output has to stay parseable.
func logger() func(ctx context.Context, msg string, args ...any) {
	if *flagQuiet {
		return func(context.Context, string, ...any) {}
	}
	return func(_ context.Context, msg string, args ...any) {
		if len(args) == 0 {
			fmt.Fprintf(os.Stderr, "ai-stack: %s\n", msg)
			return
		}
		var b strings.Builder
		b.WriteString("ai-stack: ")
		b.WriteString(msg)
		for i := 0; i+1 < len(args); i += 2 {
			fmt.Fprintf(&b, " %v=%v", args[i], args[i+1])
		}
		if len(args)%2 == 1 {
			fmt.Fprintf(&b, " %v", args[len(args)-1])
		}
		fmt.Fprintln(os.Stderr, b.String())
	}
}

// flagQuiet is shared: every subcommand that can download accepts -q, and the
// logger needs to see it without being handed it.
var flagQuiet = new(bool)

// silent discards SDK chatter. Platform detection reports which runtime it
// picked every time it runs, which is worth seeing during an install and pure
// noise in the middle of a status table.
func silent(context.Context, string, ...any) {}
