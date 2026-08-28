// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"embed"
	"fmt"
	"strings"
	"text/template"
)

// Prompts live in files, not in string literals: they are edited far more
// often than the code around them, and a diff of a prompt should read like a
// diff of prose.
//
//go:embed prompts/*.md
var promptFS embed.FS

// prompt renders one template. Lang is the language to translate into, which
// is what makes a single English prompt usable for any target language.
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
// follow "Translate into Russian" far more reliably than "Translate into ru".
// An unknown code is passed through: naming a language the table does not know
// still beats silently writing in English.
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
