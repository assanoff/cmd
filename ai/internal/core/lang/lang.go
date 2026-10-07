// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

// Package lang turns a short language code into the name a model recognizes.
//
// sum and tr both carried this table. They were identical, and they had to be:
// a pipeline that transcribes, translates and summarizes must agree with
// itself about what "ru" means.
package lang

import "strings"

// Name turns a short code into the name a model recognizes. Models follow
// "Write in Russian" far more reliably than "Write in ru". An unknown code is
// passed through: naming a language the table does not know still beats
// silently writing in English.
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
