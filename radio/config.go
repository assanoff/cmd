// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Defaults for everything a feed needs that the episode files do not say.
const (
	defaultPort     = "8080"
	defaultPrefix   = "/radio"
	defaultCategory = "Music"
	defaultLanguage = "en-us"
	defaultExplicit = "no"
)

// dirConfig is the per-collection settings file. It lives beside the episodes
// because title, author and category describe the collection, not the machine:
// copy the directory to another host and the feed still says the right thing.
const dirConfig = "radio.conf"

// settings is the channel-level metadata for one podcast directory.
type settings struct {
	baseURL   string
	prefix    string
	title     string
	author    string
	email     string
	desc      string
	category  string
	language  string
	link      string
	explicit  string
	copyright string
}

// settingsFor resolves the settings for one podcast directory.
//
// Precedence runs from general to specific: built-in defaults, then
// ~/.config/radio/config, then radio.conf in the directory, then the
// environment, and finally the command's own flags, which the caller applies
// on top of what this returns.
func settingsFor(dir, name string) settings {
	local := loadKV(filepath.Join(dir, dirConfig))

	get := func(key string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		if v := local[key]; v != "" {
			return v
		}
		globalOnce.Do(loadGlobal)
		return globalVals[key]
	}

	title := firstNonEmpty(get("RADIO_TITLE"), name)

	s := settings{
		baseURL: firstNonEmpty(get("RADIO_BASE_URL"), defaultBaseURL(get("RADIO_PORT"))),
		// The prefix is deliberately not read from the directory: gen and serve
		// have to agree on it, and a per-collection override would let one
		// write URLs the other does not answer on.
		prefix:    prefixSetting(),
		title:     title,
		author:    firstNonEmpty(get("RADIO_AUTHOR"), title),
		email:     firstNonEmpty(get("RADIO_EMAIL"), name+"@podcast.local"),
		desc:      firstNonEmpty(get("RADIO_DESCRIPTION"), title),
		category:  firstNonEmpty(get("RADIO_CATEGORY"), defaultCategory),
		language:  firstNonEmpty(get("RADIO_LANGUAGE"), defaultLanguage),
		link:      get("RADIO_LINK"),
		explicit:  firstNonEmpty(get("RADIO_EXPLICIT"), defaultExplicit),
		copyright: get("RADIO_COPYRIGHT"),
	}
	if s.link == "" {
		s.link = strings.TrimRight(s.baseURL, "/")
	}
	return s
}

// port is where serve listens and what the default base URL points at. Keeping
// it one setting is what stops a feed from advertising a port nothing serves.
func port() string {
	return firstNonEmpty(globalValue("RADIO_PORT"), defaultPort)
}

// prefixSetting is the URL path a collection lives under, for both commands.
func prefixSetting() string {
	return normalizePrefix(firstNonEmpty(globalValue("RADIO_PREFIX"), defaultPrefix))
}

// globalValue reads a setting that belongs to the machine rather than to any
// one collection: the environment, then ~/.config/radio/config.
func globalValue(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	globalOnce.Do(loadGlobal)
	return globalVals[key]
}

// defaultBaseURL guesses the address a phone on the same network can reach.
//
// Every consumer of a podcast feed is on another device, so localhost is the
// one answer that is never useful: the enclosure URLs would resolve to the
// listener's own machine. The routed interface address is the best guess
// available without asking, and gen prints it on every run so it is never a
// silent surprise.
func defaultBaseURL(portOverride string) string {
	p := firstNonEmpty(portOverride, port())
	host := lanIP()
	if host == "" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, p)
}

// lanIP reports the address of the interface that carries default traffic.
// A UDP "connection" sends nothing; it only makes the kernel pick a route,
// which is exactly the question being asked.
func lanIP() string {
	c, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	// Nothing was sent and nothing will be read, so there is no buffered
	// state for this close to fail to flush.
	defer func() { _ = c.Close() }()

	addr, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil || addr.IP.IsLoopback() || addr.IP.To4() == nil {
		return ""
	}
	return addr.IP.String()
}

func normalizePrefix(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.TrimRight(p, "/")
}

var (
	globalOnce sync.Once
	globalVals map[string]string
)

func loadGlobal() {
	dir := configDir()
	if dir == "" {
		globalVals = map[string]string{}
		return
	}
	globalVals = loadKV(filepath.Join(dir, "radio", "config"))
}

// loadKV reads a flat KEY=value file. A missing file is not an error: every
// value has a default.
//
// Keys are normalized to the RADIO_ prefix, so a collection file can read as
// prose — TITLE=Vice City — while the same name works as an environment
// variable, where a bare TITLE would collide with anything.
func loadKV(path string) map[string]string {
	vals := make(map[string]string)

	f, err := os.Open(path)
	if err != nil {
		return vals
	}
	defer func() { _ = f.Close() }()

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
		key = strings.ToUpper(strings.TrimSpace(key))
		if !strings.HasPrefix(key, "RADIO_") {
			key = "RADIO_" + key
		}
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		vals[key] = val
	}
	return vals
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

func say(quiet bool, format string, args ...any) {
	if quiet {
		return
	}
	fmt.Fprintf(os.Stderr, "radio: "+format+"\n", args...)
}
