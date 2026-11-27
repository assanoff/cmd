// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"rsc.io/markdown"
)

//go:embed style.css
var styleCSS string

// A linker returns where a link to a line of a file goes, or "" for none.
// The file is relative to the root of the report, with slashes, and is
// there; line and col are 0 when the report gives none. show links into an
// editor or to the file itself, web to its page of the file.
type linker func(file string, line, col int) string

// page is the template of a page: a title, the style and the body. Its
// script opens the folded sections around the target of a link within the
// page, so that a link to a section shows what is in it.
var page = template.Must(template.New("page").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>{{.Style}}</style>
</head>
<body>
<main>
{{.Body}}
</main>
<script>
function openTarget() {
	let id;
	try {
		id = decodeURIComponent(location.hash.slice(1));
	} catch {
		return;
	}
	const target = id && document.getElementById(id);
	if (!target) {
		return;
	}
	let opened = false;
	for (let d = target.closest("details"); d; d = d.parentElement.closest("details")) {
		opened ||= !d.open;
		d.open = true;
	}
	if (opened) {
		target.scrollIntoView();
	}
}
addEventListener("hashchange", openTarget);
openTarget();
</script>
</body>
</html>
`))

// renderPage returns a page of HTML with the title and the body.
func renderPage(title string, body template.HTML) ([]byte, error) {
	var b bytes.Buffer
	err := page.Execute(&b, struct {
		Title string
		Style template.CSS
		Body  template.HTML
	}{title, template.CSS(styleCSS), body})
	return b.Bytes(), err
}

// renderMarkdown returns the HTML of the Markdown of a report. A position in
// it, file:line[:col] in code or in the text, becomes a link when the file
// is under root; link says where it goes. HTML in the Markdown is shown as
// text, as a report quotes what tools print about the code, and so are
// links other than to the web or within the page. For the same reason the
// text is kept as it is: no smart quotes and dashes, which would make
// --flag –flag, and no emoji.
//
// A heading below the title starts a section that folds, up to the next
// heading of its level or above. A heading without {#id} gets the id GitHub
// gives it, so that a link to #vet-1-finding leads to it here as it does
// there.
func renderMarkdown(md, root string, link linker) template.HTML {
	p := &markdown.Parser{
		HeadingID:     true,
		Strikethrough: true,
		TaskList:      true,
		AutoLinkText:  true,
		Table:         true,
	}
	doc := p.Parse(md)
	headingIDs(doc.Blocks) // before the rewriter makes text of HTML tags
	r := &rewriter{link: link, files: map[string]bool{}}
	if root != "" && link != nil {
		r.root, _ = os.OpenRoot(root) // no root, no links
		if r.root != nil {
			defer func() { _ = r.root.Close() }()
		}
	}
	r.blocks(doc.Blocks)
	// The parser escapes the text, and the HTML of the Markdown is text now.
	var b strings.Builder
	writeSections(&b, doc.Blocks, "top")
	return template.HTML(b.String())
}

// writeSections writes the HTML of the blocks, each heading of level 2 and
// below with what follows it, up to the next heading of its level or above,
// in a details element that is open, the heading being its summary. The
// heading links up to the one its section is in, up to the parent id: to the
// title for a section of the top level, or to the top of the page without
// one ("top" is the top of any page).
func writeSections(b *strings.Builder, bs []markdown.Block, parent string) {
	for i := 0; i < len(bs); {
		h, ok := bs[i].(*markdown.Heading)
		if ok && h.Level < 2 && h.ID != "" {
			parent = h.ID
		}
		if !ok || h.Level < 2 {
			b.WriteString(markdown.ToHTML(bs[i]))
			i++
			continue
		}
		end := i + 1
		for ; end < len(bs); end++ {
			if next, ok := bs[end].(*markdown.Heading); ok && next.Level <= h.Level {
				break
			}
		}
		heading := strings.TrimSuffix(markdown.ToHTML(h), "\n")
		closing := heading[strings.LastIndex(heading, "</"):]
		b.WriteString("<details open>\n<summary>")
		b.WriteString(strings.TrimSuffix(heading, closing))
		fmt.Fprintf(b, ` <a class="up" href="#%s" title="up">↑</a>`, template.HTMLEscapeString(parent))
		b.WriteString(closing + "</summary>\n")
		inner := parent
		if h.ID != "" {
			inner = h.ID
		}
		writeSections(b, bs[i+1:end], inner)
		b.WriteString("</details>\n")
		i = end
	}
}

// headingIDs gives each heading that has no id the one GitHub gives it.
func headingIDs(bs []markdown.Block) {
	ids := slugger{}
	for _, b := range bs {
		if h, ok := b.(*markdown.Heading); ok && h.ID == "" && h.Text != nil {
			h.ID = ids.id(plainText(h.Text.Inline))
		}
	}
}

// A slugger gives headings, in the order they come, the ids GitHub gives
// them: the slug of the text, with -1, -2 and so on after it for the second
// and later headings of the same slug. The report of an audit links to its
// headings with one, as the page of the report gives them their ids with
// another.
type slugger map[string]int

// id returns the id of the next heading with the text, "" for none.
func (s slugger) id(text string) string {
	id := slug(text)
	if id == "" {
		return ""
	}
	n := s[id]
	s[id]++
	if n > 0 {
		return id + "-" + strconv.Itoa(n)
	}
	return id
}

// slug returns the id GitHub makes of the text of a heading: the text in
// lower case, with a hyphen for each space and without what is not a
// letter, a digit, a hyphen or an underscore.
func slug(text string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(strings.TrimSpace(text)) {
		switch {
		case c == ' ':
			b.WriteByte('-')
		case c == '-' || unicode.IsLetter(c) || unicode.IsMark(c) ||
			unicode.IsDigit(c) || unicode.Is(unicode.Pc, c):
			b.WriteRune(c)
		}
	}
	return b.String()
}

// plainText returns the text of inlines as a reader sees it, without the
// marks of Markdown and without HTML tags.
func plainText(in markdown.Inlines) string {
	var b strings.Builder
	var walk func(markdown.Inlines)
	walk = func(in markdown.Inlines) {
		for _, x := range in {
			switch x := x.(type) {
			case *markdown.Plain:
				b.WriteString(x.Text)
			case *markdown.Escaped:
				b.WriteString(x.Text)
			case *markdown.Code:
				b.WriteString(x.Text)
			case *markdown.AutoLink:
				b.WriteString(x.Text)
			case *markdown.Strong:
				walk(x.Inner)
			case *markdown.Emph:
				walk(x.Inner)
			case *markdown.Del:
				walk(x.Inner)
			case *markdown.Link:
				walk(x.Inner)
			case *markdown.Image:
				walk(x.Inner)
			case *markdown.SoftBreak, *markdown.HardBreak:
				b.WriteByte(' ')
			}
		}
	}
	walk(in)
	return b.String()
}

// A rewriter makes links of the positions in a document and takes out what
// is not safe to show as it is.
type rewriter struct {
	link  linker
	root  *os.Root
	files map[string]bool // file → whether it is under root
}

func (r *rewriter) blocks(bs []markdown.Block) {
	for i, b := range bs {
		switch b := b.(type) {
		case *markdown.Paragraph:
			r.text(b.Text)
		case *markdown.Heading:
			r.text(b.Text)
		case *markdown.Text:
			r.text(b)
		case *markdown.List:
			r.blocks(b.Items)
		case *markdown.Item:
			r.blocks(b.Blocks)
		case *markdown.Quote:
			r.blocks(b.Blocks)
		case *markdown.Table:
			for _, t := range b.Header {
				r.text(t)
			}
			for _, row := range b.Rows {
				for _, t := range row {
					r.text(t)
				}
			}
		case *markdown.HTMLBlock:
			bs[i] = &markdown.CodeBlock{Position: b.Position, Fence: "```", Text: b.Text}
		}
	}
}

func (r *rewriter) text(t *markdown.Text) {
	if t != nil {
		t.Inline = r.inlines(t.Inline)
	}
}

// inlines returns the inlines with the positions made links. The parser may
// split text into several pieces, so a run of them is read as one.
func (r *rewriter) inlines(in markdown.Inlines) markdown.Inlines {
	var out markdown.Inlines
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			out = append(out, r.plain(text.String())...)
			text.Reset()
		}
	}
	for _, x := range in {
		switch x := x.(type) {
		case *markdown.Plain:
			text.WriteString(x.Text)
			continue
		case *markdown.HTMLTag:
			text.WriteString(x.Text)
			continue
		}
		flush()
		switch x := x.(type) {
		case *markdown.Code:
			out = append(out, r.code(x))
		case *markdown.Strong:
			x.Inner = r.inlines(x.Inner)
			out = append(out, x)
		case *markdown.Emph:
			x.Inner = r.inlines(x.Inner)
			out = append(out, x)
		case *markdown.Del:
			x.Inner = r.inlines(x.Inner)
			out = append(out, x)
		case *markdown.Link:
			if !safeURL(x.URL) {
				out = append(out, x.Inner...)
				continue
			}
			out = append(out, x) // no links within a link
		case *markdown.AutoLink:
			if !safeURL(x.URL) {
				out = append(out, &markdown.Plain{Text: x.Text})
				continue
			}
			out = append(out, x)
		case *markdown.Image:
			if !safeURL(x.URL) {
				out = append(out, x.Inner...)
				continue
			}
			out = append(out, x)
		default:
			out = append(out, x)
		}
	}
	flush()
	return out
}

// codePos matches a position that is all of a code span: a file, maybe
// with :line, :line:col and the end of a range, as gopls and go-style write
// them: a.go, a.go:12, a.go:12:5, a.go:12-14, a.go:12:5-9.
var codePos = regexp.MustCompile(`^(?:\./)?([^\s:]+)(?::(\d+)(?::(\d+))?(?:-\d+(?::\d+)?)?)?$`)

// code returns the code span, as a link if it is a position.
func (r *rewriter) code(c *markdown.Code) markdown.Inline {
	m := codePos.FindStringSubmatch(c.Text)
	if m == nil || !strings.Contains(path.Base(m[1]), ".") {
		return c
	}
	line, _ := strconv.Atoi(m[2])
	col, _ := strconv.Atoi(m[3])
	if url := r.url(m[1], line, col); url != "" {
		return &markdown.Link{Inner: markdown.Inlines{c}, URL: url}
	}
	return c
}

// textPos matches a position in text: a file with an extension, :line and
// maybe :col.
var textPos = regexp.MustCompile(`(?:\./)?((?:[\w.-]+/)*[\w-][\w.-]*\.\w+):(\d+)(?::(\d+))?`)

// plain returns the text with its positions made links.
func (r *rewriter) plain(s string) markdown.Inlines {
	var out markdown.Inlines
	last := 0
	for _, m := range textPos.FindAllStringSubmatchIndex(s, -1) {
		// A position starts a word: not inside a longer path or name, nor
		// after ../, which leads out of the root.
		if m[0] > 0 && (isWordByte(s[m[0]-1]) || strings.IndexByte(`/.\_-`, s[m[0]-1]) >= 0) {
			continue
		}
		line, _ := strconv.Atoi(s[m[4]:m[5]])
		col := 0
		if m[6] >= 0 {
			col, _ = strconv.Atoi(s[m[6]:m[7]])
		}
		url := r.url(s[m[2]:m[3]], line, col)
		if url == "" {
			continue
		}
		if m[0] > last {
			out = append(out, &markdown.Plain{Text: s[last:m[0]]})
		}
		out = append(out, &markdown.Link{
			Inner: markdown.Inlines{&markdown.Plain{Text: s[m[0]:m[1]]}},
			URL:   url,
		})
		last = m[1]
	}
	if last < len(s) {
		out = append(out, &markdown.Plain{Text: s[last:]})
	}
	return out
}

func isWordByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// url returns where a position links to: nowhere unless its file is a file
// under the root, not outside it, not through a link that leads out.
func (r *rewriter) url(file string, line, col int) string {
	if r.root == nil || !filepath.IsLocal(filepath.FromSlash(file)) {
		return ""
	}
	ok, seen := r.files[file]
	if !seen {
		fi, err := r.root.Stat(filepath.FromSlash(file))
		ok = err == nil && fi.Mode().IsRegular()
		r.files[file] = ok
	}
	if !ok {
		return ""
	}
	return r.link(file, line, col)
}

// safeURL reports whether a link of a report may stay one: to the web, to
// mail, or within the site or the page.
func safeURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "", "http", "https", "mailto":
		return true
	}
	return false
}

// editorLinker returns the linker of show: the editor of the config, with
// {path}, {line} and {col} filled in, or the file itself without one.
func editorLinker(editor, root string) linker {
	return func(file string, line, col int) string {
		abs := filepath.Join(root, filepath.FromSlash(file))
		if editor == "" {
			return fileURL(abs)
		}
		return strings.NewReplacer(
			"{path}", escapePath(abs),
			"{line}", strconv.Itoa(max(line, 1)),
			"{col}", strconv.Itoa(max(col, 1)),
		).Replace(editor)
	}
}

// escapePath returns an absolute path with slashes, escaped for a URL.
func escapePath(abs string) string {
	parts := strings.Split(filepath.ToSlash(abs), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// fileURL returns the file: URL of an absolute path, C:\a as file:///C:/a.
func fileURL(abs string) string {
	p := escapePath(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "file://" + p
}
