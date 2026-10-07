// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package prompt renders the prompt templates a subcommand embeds.
//
// Prompts live in files, not in string literals: they are edited far more
// often than the code around them, and a diff of a prompt should read like a
// diff of prose. Each subcommand keeps its own prompts next to its own code
// and embeds them itself; what they share, and what lives here, is the
// rendering.
package prompt

import (
	"fmt"
	"io/fs"
	"strings"
	"text/template"

	"github.com/assanoff/cmd/ai/internal/core/lang"
)

// Render reads name.md out of fsys and fills it in.
//
// Lang is the language the answer must be written in, which is what makes a
// single English prompt usable for any output language: the template says
// "Write in {{.Lang}}" and the table in package lang turns "ru" into the word
// a model actually follows.
func Render(fsys fs.FS, name, code string) (string, error) {
	b, err := fs.ReadFile(fsys, name+".md")
	if err != nil {
		return "", fmt.Errorf("prompt %q: %w", name, err)
	}

	t, err := template.New(name).Parse(string(b))
	if err != nil {
		return "", fmt.Errorf("prompt %q: %w", name, err)
	}

	var sb strings.Builder
	data := struct{ Lang string }{Lang: lang.Name(code)}
	if err := t.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("prompt %q: %w", name, err)
	}
	return strings.TrimSpace(sb.String()), nil
}
