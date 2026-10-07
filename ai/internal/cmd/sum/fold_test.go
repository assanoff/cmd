// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package sum

import "testing"

// TestDemote covers the one thing that makes a multi-style summary readable:
// a style that writes its own headings has to sit under the section title
// rather than beside it.
func TestDemote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"heading", "## Topic\n\n- a point", "### Topic\n\n- a point"},
		{"several levels", "# One\n## Two\n### Three", "## One\n### Two\n#### Three"},
		{"a hash without a space is a word", "no heading here\n#hashtag-ish", "no heading here\n#hashtag-ish"},
		{"bare hashes still count", "##", "###"},
		{"stops at six", "###### Deep", "###### Deep"},
		{"fenced code is left alone", "## T\n\n```sh\n# a comment\n```\n## U",
			"### T\n\n```sh\n# a comment\n```\n### U"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := demote(tt.in); got != tt.want {
				t.Errorf("demote(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
