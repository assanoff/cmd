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

// Roles keep model names out of the code. A role names the job a model does;
// the config says which model currently does it, so swapping a model is one
// edited line and every ai-* command follows.
const (
	roleASR     = "asr"
	roleASRFast = "asr-fast"
)

// Fallbacks used when the config says nothing. They are deliberately modest so
// a machine with no config still works.
const (
	defaultASR     = "large-v3-turbo"
	defaultASRFast = "base"
)

// resolveModel turns -m into a whisper model name. A known role is looked up
// in the config; anything else is an explicit model name and passes through
// untouched, so both spellings work:
//
//	ai-hear -m asr talk.wav
//	ai-hear -m large-v3-turbo talk.wav
func resolveModel(v string) string {
	switch strings.TrimSpace(v) {
	case "", roleASR:
		return firstNonEmpty(configValue("AI_ASR_MODEL"), defaultASR)
	case roleASRFast:
		return firstNonEmpty(configValue("AI_ASR_MODEL_FAST"), defaultASRFast)
	default:
		return v
	}
}

// language returns the whisper language hint: the flag, then the config, then
// empty for autodetect.
func language() string {
	return firstNonEmpty(*flagLang, configValue("AI_LANG"))
}

// configValue reads one key. The process environment wins over the file, which
// makes a one-off override just:
//
//	AI_ASR_MODEL=base ai-hear talk.wav
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
)

// loadConfig reads the flat KEY=value file deployed to ~/.config/ai/config.
// A missing file is not an error: every value has a default.
func loadConfig() {
	configVals = make(map[string]string)

	dir := configDir()
	if dir == "" {
		return
	}

	f, err := os.Open(filepath.Join(dir, "ai", "config"))
	if err != nil {
		return
	}
	defer f.Close()

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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// logger sends SDK progress to standard error. The SDK ships FmtLogger, but it
// writes to standard output, which would corrupt every pipeline.
func logger() func(ctx context.Context, msg string, args ...any) {
	if *flagQuiet {
		return func(context.Context, string, ...any) {}
	}
	return func(_ context.Context, msg string, args ...any) {
		if len(args) == 0 {
			fmt.Fprintf(os.Stderr, "ai-hear: %s\n", msg)
			return
		}
		var b strings.Builder
		b.WriteString("ai-hear: ")
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
