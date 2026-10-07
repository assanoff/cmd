// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package prompt renders the prompt templates a subcommand embeds.
//
// Prompts live in files, not in string literals: they are edited far more
// often than the code around them, and a diff of a prompt should read like a
// diff of prose. Each subcommand keeps its own prompts next to its own code
// and embeds them itself; what they share, and what lives here, is the
// rendering and the language table the templates are filled from.
package prompt

import (
	"fmt"
	"io/fs"
	"strings"
	"text/template"
)

// Render reads name.md out of fsys and fills it in with data.
//
// The template decides where each value goes, which is what keeps a prompt
// editable as prose: a sentence the caller wants to add under some condition
// belongs in the file next to every other sentence, not concatenated onto the
// result in Go.
func Render(fsys fs.FS, name string, data any) (string, error) {
	b, err := fs.ReadFile(fsys, name+".md")
	if err != nil {
		return "", fmt.Errorf("prompt %q: %w", name, err)
	}

	t, err := template.New(name).Parse(string(b))
	if err != nil {
		return "", fmt.Errorf("prompt %q: %w", name, err)
	}

	var sb strings.Builder
	if err := t.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("prompt %q: %w", name, err)
	}
	return strings.TrimSpace(sb.String()), nil
}

// Name turns a short language code into the name a model recognizes. Models
// follow "Write in Russian" far more reliably than "Write in ru". An unknown
// code is passed through: naming a language the table does not know still
// beats silently writing in English.
//
// It lives here because a template is the only thing that consumes it, and
// because a pipeline that transcribes, translates and summarizes must agree
// with itself about what "ru" means.
func Name(code string) string {
	if name, ok := names[strings.ToLower(strings.TrimSpace(code))]; ok {
		return name
	}
	return code
}

var names = map[string]string{
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
