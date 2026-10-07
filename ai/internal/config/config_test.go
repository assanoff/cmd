// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// reset forgets the loaded file so a test can point XDG_CONFIG_HOME somewhere
// else and have it read again. Without it the first test in the package would
// pin the config for all the rest — and, worse, would read the real one off
// the machine running the tests.
func reset() {
	once = sync.Once{}
	vals = nil
	path = ""
}

// withConfig writes a config file into a temp dir and points the package at it.
func withConfig(t *testing.T, body string) {
	t.Helper()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "ai", "config"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("XDG_CONFIG_HOME", dir)
	reset()
	t.Cleanup(reset)
}

// clearEnv keeps a real AI_* variable in the developer's shell from deciding
// the result of a test about the config file.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, r := range Roles {
		t.Setenv(r.Key, "")
	}
}

// The whole package exists because the five commands disagreed about this.
// "code" fell back to "fast" in ai-ask and to "smart" in ai-sum, so an unbound
// role resolved to a different model depending on which command you asked.
func TestResolveFallbackChains(t *testing.T) {
	withConfig(t, "AI_MODEL_FAST=owner/fast\n")
	clearEnv(t)
	t.Setenv("AI_MODEL_FAST", "owner/fast")

	cases := []struct {
		name  string
		v     string
		deflt string
		want  string
	}{
		{"empty takes the subcommand default", "", RoleFast, "owner/fast"},
		{"fast", RoleFast, RoleFast, "owner/fast"},
		{"smart falls back to fast", RoleSmart, RoleFast, "owner/fast"},
		{"code falls back through smart to fast", RoleCode, RoleFast, "owner/fast"},
		{"sum asks for smart and still lands on fast", "", RoleSmart, "owner/fast"},
		{"an explicit id passes through", "other/model", RoleFast, "other/model"},
		{"whitespace is trimmed", "  smart  ", RoleFast, "owner/fast"},
	}

	for _, c := range cases {
		if got := Resolve(c.v, c.deflt); got != c.want {
			t.Errorf("%s: Resolve(%q, %q) = %q, want %q", c.name, c.v, c.deflt, got, c.want)
		}
	}
}

// A bound role wins over any built-in default, including one belonging to an
// earlier link of the chain.
func TestResolvePrefersConfigOverDefaults(t *testing.T) {
	withConfig(t, "AI_MODEL_SMART=owner/smart\n")
	clearEnv(t)
	t.Setenv("AI_MODEL_SMART", "owner/smart")

	if got := Resolve(RoleSmart, RoleFast); got != "owner/smart" {
		t.Errorf("Resolve(smart) = %q, want owner/smart", got)
	}
	// fast is unbound here, so it falls through to its built-in default rather
	// than borrowing the smart binding.
	if got := Resolve(RoleFast, RoleFast); got != DefaultFast {
		t.Errorf("Resolve(fast) = %q, want %q", got, DefaultFast)
	}
}

// Asking for the fast transcriber and silently getting the large one is the
// opposite of the request, so asr-fast deliberately does not fall back to asr.
func TestResolveASRFastDoesNotBorrowASR(t *testing.T) {
	withConfig(t, "AI_ASR_MODEL=large-v3-turbo\n")
	clearEnv(t)
	t.Setenv("AI_ASR_MODEL", "large-v3-turbo")

	if got := Resolve(RoleASR, RoleASR); got != "large-v3-turbo" {
		t.Errorf("Resolve(asr) = %q", got)
	}
	if got := Resolve(RoleASRFast, RoleASR); got != "base" {
		t.Errorf("Resolve(asr-fast) = %q, want its own default base", got)
	}
}

// A machine with no config at all still answers.
func TestResolveWithNoConfigFile(t *testing.T) {
	withConfig(t, "")
	clearEnv(t)

	if got := Resolve("", RoleFast); got != DefaultFast {
		t.Errorf("Resolve on a bare machine = %q, want %q", got, DefaultFast)
	}
	if Path() != "" {
		t.Errorf("Path() = %q, want empty when there is no file", Path())
	}
}

// The environment is the documented way to override one call.
func TestValuePrefersEnvironment(t *testing.T) {
	withConfig(t, "AI_MODEL_FAST=from/file\n")
	clearEnv(t)
	t.Setenv("AI_MODEL_FAST", "from/env")

	if got := Value("AI_MODEL_FAST"); got != "from/env" {
		t.Errorf("Value = %q, want from/env", got)
	}
}

func TestLoadIgnoresCommentsAndQuotes(t *testing.T) {
	withConfig(t, "# a comment\n\nAI_MODEL_FAST = \"owner/quoted\"\nnot a pair\n")
	clearEnv(t)

	if got := Value("AI_MODEL_FAST"); got != "owner/quoted" {
		t.Errorf("Value = %q, want owner/quoted", got)
	}
	if Path() == "" {
		t.Error("Path() is empty although a config was written")
	}
}

// SetKey edits a file a human maintains, so everything it did not come for has
// to survive: the comments, the blank lines and the order.
func TestSetKeyPreservesStructure(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config")

	const before = `# which model does what

AI_MODEL_FAST=owner/old

# speech
AI_ASR_MODEL=base
`
	if err := os.WriteFile(p, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	was, err := SetKey(p, "AI_MODEL_FAST", "owner/new")
	if err != nil {
		t.Fatal(err)
	}
	if was != "owner/old" {
		t.Errorf("SetKey reported %q as the previous value, want owner/old", was)
	}

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)

	for _, want := range []string{"# which model does what", "AI_MODEL_FAST=owner/new", "# speech", "AI_ASR_MODEL=base"} {
		if !strings.Contains(got, want) {
			t.Errorf("SetKey lost %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "owner/old") {
		t.Errorf("SetKey left the old value behind:\n%s", got)
	}
	// The keys must keep their order, or a hand-grouped file is reshuffled on
	// every edit.
	if strings.Index(got, "AI_MODEL_FAST") > strings.Index(got, "AI_ASR_MODEL") {
		t.Errorf("SetKey reordered the file:\n%s", got)
	}
}

// Rebinding a role is a reasonable way to create the config.
func TestSetKeyCreatesTheFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config")

	was, err := SetKey(p, "AI_MODEL_SMART", "owner/smart")
	if err != nil {
		t.Fatal(err)
	}
	if was != "" {
		t.Errorf("SetKey reported %q as the previous value of a new key", was)
	}

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "AI_MODEL_SMART=owner/smart\n" {
		t.Errorf("wrote %q", b)
	}
}

// A typo in the config must not stop a run, but it must not pass unnoticed.
func TestIntReportsRubbish(t *testing.T) {
	withConfig(t, "AI_MAP_TOKENS=lots\n")
	clearEnv(t)

	var warned string
	got := Int("AI_MAP_TOKENS", 6000, func(format string, args ...any) {
		warned = format
	})
	if got != 6000 {
		t.Errorf("Int = %d, want the default 6000", got)
	}
	if warned == "" {
		t.Error("Int accepted a non-number without a word")
	}
}

func TestIntReadsANumber(t *testing.T) {
	withConfig(t, "AI_MAP_TOKENS=1234\n")
	clearEnv(t)

	got := Int("AI_MAP_TOKENS", 6000, func(string, ...any) {
		t.Error("Int warned about a perfectly good number")
	})
	if got != 1234 {
		t.Errorf("Int = %d, want 1234", got)
	}
}

// Every role in the table must have a fallback chain, or Resolve silently
// treats its name as a model id and Kronk fails much later with a worse
// message.
func TestEveryRoleHasAChain(t *testing.T) {
	for _, r := range Roles {
		if _, ok := fallbacks[r.Name]; !ok {
			t.Errorf("role %q has no fallback chain", r.Name)
		}
	}
	for name := range fallbacks {
		if _, ok := LookupRole(name); !ok {
			t.Errorf("fallback chain %q names no role", name)
		}
	}
}
