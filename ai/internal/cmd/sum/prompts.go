// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package sum

import (
	"embed"
	"io/fs"
	"slices"
	"strings"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/prompt"
)

// Prompts live in files, not in string literals: they are edited far more
// often than the code around them, and a diff of a prompt should read like a
// diff of prose.
//
//go:embed prompts/*.md
var promptFS embed.FS

// prompts is the embedded directory, rooted so a name is just "brief".
var prompts, _ = fs.Sub(promptFS, "prompts")

// render fills in one prompt template for the given output language.
func render(name, lang string) (string, error) {
	return prompt.Render(prompts, name, struct{ Lang string }{Lang: prompt.Name(lang)})
}

// styles are the summary shapes sum knows, in the order they read best as
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
	return strings.Join(slices.Sorted(slices.Values(styles)), ", ")
}

func knownStyle(name string) bool {
	return slices.Contains(styles, name)
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
	for name := range strings.SplitSeq(v, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if !knownStyle(name) {
			return nil, cli.Usagef("unknown -s %q: want %s", name, styleList())
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, cli.Usagef("-s is empty: want %s", styleList())
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

// materialPreamble introduces the -c files to the model. It is written in the
// summary language for the same reason the section titles are: a model asked
// to answer in Russian follows Russian framing more consistently.
func materialPreamble(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "ru":
		return "Ниже — вспомогательные материалы к этому тексту (слайды, конспекты). " +
			"Используй их, чтобы правильно писать имена и термины и не упустить главное. " +
			"Не пересказывай их сами по себе: конспект делается по основному тексту."
	default:
		return "Below is supporting material for this text (slides, notes). " +
			"Use it to spell names and terms correctly and to avoid missing the point. " +
			"Do not summarize the material on its own: the summary is of the main text."
	}
}
