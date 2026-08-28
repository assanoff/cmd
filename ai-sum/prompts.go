// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"embed"
	"fmt"
	"sort"
	"strings"
	"text/template"
)

// Prompts live in files, not in string literals: they are edited far more
// often than the code around them, and a diff of a prompt should read like a
// diff of prose.
//
//go:embed prompts/*.md
var promptFS embed.FS

// styles are the summary shapes ai-sum knows, in the order they read best as
// sections of one document: the point first, then the vocabulary needed to
// follow it, then the body, then the details worth keeping. The map style is
// not one of them: it compresses a chunk on the way to a summary and is never
// asked for directly.
var styles = []string{"brief", "terms", "chapters", "facts", "tips", "actions"}

// sectionTitles name the sections when several styles are asked for at once.
// They are written in the summary language, and unlike the prompts they cannot
// be left in English: a Russian document with English headings reads as a bug.
var sectionTitles = map[string]map[string]string{
	"brief":    {"en": "In short", "ru": "Главное"},
	"terms":    {"en": "Terms", "ru": "Термины"},
	"chapters": {"en": "By topic", "ru": "По темам"},
	"facts":    {"en": "Facts", "ru": "Факты"},
	"tips":     {"en": "Tips and tricks", "ru": "Приёмы и трюки"},
	"actions":  {"en": "Action items", "ru": "Действия"},
}

func styleList() string {
	s := append([]string(nil), styles...)
	sort.Strings(s)
	return strings.Join(s, ", ")
}

func knownStyle(name string) bool {
	for _, s := range styles {
		if s == name {
			return true
		}
	}
	return false
}

// parseStyles reads the -s value. One name is the ordinary case; a comma-
// separated list asks for one document with a section per style, which is what
// a lecture summary is. Duplicates are dropped rather than rejected, so
// pasting a list together from two places cannot produce the same section
// twice.
func parseStyles(v string) ([]string, error) {
	var (
		out  []string
		seen = make(map[string]bool)
	)
	for _, name := range strings.Split(v, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if !knownStyle(name) {
			return nil, usagef("unknown -s %q: want %s", name, styleList())
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, usagef("-s is empty: want %s", styleList())
	}
	return out, nil
}

// sectionTitle names one section. An untranslated title falls back to English
// rather than to the style name: "In short" is at least a sentence, "brief" is
// a flag value leaking into the document.
func sectionTitle(style, lang string) string {
	titles := sectionTitles[style]
	if titles == nil {
		return style
	}
	code := strings.ToLower(strings.TrimSpace(lang))
	if t, ok := titles[code]; ok {
		return t
	}
	return titles["en"]
}

// prompt renders one template. Lang is the language the answer must be written
// in, which is what makes a single English prompt usable for any output
// language.
func prompt(name string, lang string) (string, error) {
	b, err := promptFS.ReadFile("prompts/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("prompt %q: %w", name, err)
	}

	t, err := template.New(name).Parse(string(b))
	if err != nil {
		return "", fmt.Errorf("prompt %q: %w", name, err)
	}

	var sb strings.Builder
	data := struct{ Lang string }{Lang: languageName(lang)}
	if err := t.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("prompt %q: %w", name, err)
	}
	return strings.TrimSpace(sb.String()), nil
}

// languageName turns a short code into the name a model recognizes. Models
// follow "Write in Russian" far more reliably than "Write in ru". An unknown
// code is passed through: naming a language the table does not know still
// beats silently writing in English.
func languageName(code string) string {
	if name, ok := languageNames[strings.ToLower(strings.TrimSpace(code))]; ok {
		return name
	}
	return code
}

var languageNames = map[string]string{
	"ru": "Russian",
	"en": "English",
	"de": "German",
	"fr": "French",
	"es": "Spanish",
	"it": "Italian",
	"pt": "Portuguese",
	"pl": "Polish",
	"uk": "Ukrainian",
	"kk": "Kazakh",
	"tr": "Turkish",
	"zh": "Chinese",
	"ja": "Japanese",
	"ko": "Korean",
	"ar": "Arabic",
	"nl": "Dutch",
	"cs": "Czech",
	"sv": "Swedish",
}
