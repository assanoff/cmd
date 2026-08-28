// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// podcast is one directory of episodes.
type podcast struct {
	dir  string
	name string
}

// generate writes feed.xml and one chapters file per episode, and reports
// where the feed went and how many episodes it holds.
func generate(p podcast, s settings, dry, quiet bool) (string, int, error) {
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return "", 0, err
	}

	// Every URL inside this podcast hangs off one base. The prefix is part of
	// the published contract: change it and every enclosure URL in every feed
	// a subscriber already has stops resolving.
	urlBase := strings.TrimRight(s.baseURL, "/") + s.prefix + "/" + pathEscape(p.name)

	var coverURL string
	for _, name := range []string{"cover.jpg", "cover.png"} {
		if info, err := os.Stat(filepath.Join(p.dir, name)); err == nil {
			coverURL = cacheBustedURL(urlBase+"/"+name, info)
			break
		}
	}

	feedPath := filepath.Join(p.dir, "feed.xml")

	var items []itemXML
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".pl") {
			continue
		}

		base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		plPath := filepath.Join(p.dir, e.Name())

		ep, err := parsePL(plPath)
		if err != nil {
			return "", 0, fmt.Errorf("parsing %s: %w", e.Name(), err)
		}

		mp3Name := base + ".mp3"
		mp3Path := filepath.Join(p.dir, mp3Name)
		mp3URL := urlBase + "/" + pathEscape(mp3Name)

		var mp3Size string
		var durationSec int64
		var published time.Time
		if info, err := os.Stat(mp3Path); err == nil {
			mp3Size = strconv.FormatInt(info.Size(), 10)
			durationSec = mp3DurationSec(mp3Path, info.Size())
			published = info.ModTime()
		} else {
			// A descriptor without audio still produces an item, because a
			// collection is often edited on one machine and served from
			// another. It is worth saying out loud: the enclosure will 404.
			say(quiet, "%s: %s is missing, the enclosure will not resolve", p.name, mp3Name)
			mp3Size = "0"
		}
		if published.IsZero() {
			if info, err := os.Stat(plPath); err == nil {
				published = info.ModTime()
			} else {
				published = time.Now()
			}
		}

		// Transcript: the first sibling that looks like one. ai-notes writes
		// $BASE.txt and $BASE.md, which is why those are on the list.
		var transcriptURL string
		var transcript *podcastTranscript
		for _, ext := range []string{".scr", ".txt", ".html", ".htm", ".md"} {
			if _, err := os.Stat(filepath.Join(p.dir, base+ext)); err == nil {
				transcriptURL = urlBase + "/" + pathEscape(base+ext)
				transcript = &podcastTranscript{
					Type: transcriptMIMETypeByExt(ext),
					URL:  transcriptURL,
				}
				break
			}
		}

		var episodeImageURL string
		if info, err := os.Stat(filepath.Join(p.dir, base+".jpg")); err == nil {
			episodeImageURL = cacheBustedURL(urlBase+"/"+pathEscape(base+".jpg"), info)
		}

		chaptersName := base + ".chapters.json"
		if !dry {
			path := filepath.Join(p.dir, chaptersName)
			if err := writeChaptersJSON(path, ep, episodeImageURL); err != nil {
				return "", 0, fmt.Errorf("writing %s: %w", chaptersName, err)
			}
		}

		var pscEntries []pscChapter
		for _, t := range ep.tracks {
			pscEntries = append(pscEntries, pscChapter{
				Start: t.startSec,
				Title: t.label(),
				Href:  t.url,
			})
		}

		description, itunesSummary, contentEncoded := buildEpisodeContent(ep, episodeImageURL, coverURL, mp3URL, transcriptURL)

		var episodeImg *itunesImage
		switch {
		case episodeImageURL != "":
			episodeImg = &itunesImage{Href: episodeImageURL}
		case coverURL != "":
			episodeImg = &itunesImage{Href: coverURL}
		}

		slug := pathEscape(base)
		items = append(items, itemXML{
			Title: ep.title,
			GUID:  guidXML{Value: urlBase + "/" + slug, IsPermaLink: "false"},
			Link:  urlBase + "/" + slug,
			// The file's own date, not the moment of generation. Stamping
			// "now" re-dates the whole archive on every run: clients treat
			// every episode as new and the feed churns in version control.
			PubDate:           published.UTC().Format(time.RFC1123Z),
			Enclosure:         enclosure{Type: "audio/mpeg", Length: mp3Size, URL: mp3URL},
			Description:       description,
			ItunesSummary:     &cdataText{Text: itunesSummary},
			ItunesEpisodeType: "full",
			ItunesImage:       episodeImg,
			ItunesDuration:    formatShortDuration(durationSec),
			ItunesExplicit:    "false",
			PodcastChapters: &podcastChapters{
				Type: "application/json+chapters",
				URL:  urlBase + "/" + pathEscape(chaptersName),
			},
			PscChapters: &pscChapters{
				Xmlns:    "http://podlove.org/simple-chapters",
				Version:  "1.1",
				Chapters: pscEntries,
			},
			PodcastTranscript: transcript,
			ContentEncoded:    cdataText{Text: contentEncoded},
		})
	}

	var img *itunesImage
	if coverURL != "" {
		img = &itunesImage{Href: coverURL}
	}

	doc := rssDoc{
		Version:   "2.0",
		NsAtom:    "http://www.w3.org/2005/Atom",
		NsContent: "http://purl.org/rss/1.0/modules/content/",
		NsItunes:  "http://www.itunes.com/dtds/podcast-1.0.dtd",
		NsPodcast: "https://podcastindex.org/namespace/1.0",
		NsPsc:     "http://podlove.org/simple-chapters",
		ChannelData: channelXML{
			Title:     s.title,
			Copyright: s.copyright,
			Link:      s.link,
			AtomLinkSelf: atomLink{
				Type: "application/rss+xml",
				Rel:  "self",
				Href: urlBase + "/feed.xml",
			},
			Language:       s.language,
			Description:    s.desc,
			ItunesSummary:  s.desc,
			ItunesExplicit: s.explicit,
			ItunesImage:    img,
			ItunesAuthor:   s.author,
			ItunesOwner:    itunesOwner{Name: s.author, Email: s.email},
			ItunesKeywords: strings.ToLower(strings.ReplaceAll(s.title, " ", ",")),
			ItunesCategory: itunesCategory{Text: s.category},
			Items:          items,
		},
	}

	if dry {
		return feedPath, len(items), nil
	}

	xmlBytes, err := encodeFeed(doc)
	if err != nil {
		return "", 0, err
	}
	if err := os.WriteFile(feedPath, xmlBytes, 0o644); err != nil {
		return "", 0, err
	}
	return feedPath, len(items), nil
}

// buildEpisodeContent writes the three shapes of the same thing a feed has to
// carry: plain text for <description>, HTML for <itunes:summary>, and HTML for
// <content:encoded>. Clients disagree about which one they render.
func buildEpisodeContent(ep episode, episodeImageURL, coverURL, mp3URL, transcriptURL string) (description, itunesSummary, contentEncoded string) {
	imgURL := episodeImageURL
	if imgURL == "" {
		imgURL = coverURL
	}

	description = ep.desc

	var s strings.Builder
	if imgURL != "" {
		s.WriteString("<p><img src=\"" + escapeHTML(imgURL) + "\" alt=\"\" /></p>")
	}
	if len(ep.tracks) > 0 {
		s.WriteString("<ul>")
		for _, t := range ep.tracks {
			s.WriteString("<li>")
			if t.url != "" {
				s.WriteString("<a href=\"" + escapeHTML(t.url) + "\">")
			}
			s.WriteString("<em>" + formatTimestamp(t.startSec) + "</em> &ndash; ")
			s.WriteString(escapeHTML(t.label()))
			if t.url != "" {
				s.WriteString("</a>")
			}
			s.WriteString(".</li>")
		}
		s.WriteString("</ul>")
	}
	if mp3URL != "" {
		s.WriteString("<p><a href=\"" + escapeHTML(mp3URL) + "\">audio</a> " +
			"<audio src=\"" + escapeHTML(mp3URL) + "\" preload=\"none\"></audio></p>")
	}
	if transcriptURL != "" {
		s.WriteString("<p><a href=\"" + escapeHTML(transcriptURL) + "\">transcript</a></p>")
	}
	itunesSummary = s.String()

	var b strings.Builder
	b.WriteString("<p>" + escapeHTML(ep.desc) + "</p>")
	if len(ep.tracks) > 0 {
		b.WriteString("<p>Chapters:</p><ul>")
		for _, t := range ep.tracks {
			b.WriteString("<li>")
			b.WriteString("<em>" + formatTimestamp(t.startSec) + "</em> &ndash; ")
			if t.url != "" {
				b.WriteString("<a href=\"" + escapeHTML(t.url) + "\">")
			}
			b.WriteString(escapeHTML(t.label()))
			if t.url != "" {
				b.WriteString("</a>")
			}
			b.WriteString("</li>")
		}
		b.WriteString("</ul>")
	}
	if imgURL != "" {
		b.WriteString("<p><img src=\"" + escapeHTML(imgURL) + "\" alt=\"\" /></p>")
	}
	if mp3URL != "" {
		b.WriteString("<p><a href=\"" + escapeHTML(mp3URL) + "\">audio</a> " +
			"<audio src=\"" + escapeHTML(mp3URL) + "\" preload=\"none\"></audio></p>")
	}
	if transcriptURL != "" {
		b.WriteString("<p><a href=\"" + escapeHTML(transcriptURL) + "\">transcript</a></p>")
	}
	contentEncoded = b.String()

	return description, itunesSummary, contentEncoded
}

// cacheBustedURL appends the file's modification time, so a replaced cover
// reaches clients that would otherwise serve the old one from cache forever.
func cacheBustedURL(rawURL string, info os.FileInfo) string {
	if rawURL == "" || info == nil {
		return rawURL
	}
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	return rawURL + sep + "v=" + strconv.FormatInt(info.ModTime().Unix(), 10)
}

// pathEscape encodes a path segment like url.PathEscape, and '&' as well, so
// the result is unambiguous both in an HTTP request and in an XML attribute.
func pathEscape(s string) string {
	return strings.ReplaceAll(url.PathEscape(s), "&", "%26")
}

func transcriptMIMETypeByExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".html", ".htm":
		return "text/html"
	case ".md":
		return "text/markdown"
	default:
		return "text/plain"
	}
}

// mp3DurationSec asks ffprobe, and falls back to an estimate from the file
// size so a machine without ffmpeg still produces a feed.
func mp3DurationSec(path string, fileSize int64) int64 {
	out, err := exec.Command("ffprobe",
		"-v", "quiet",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).Output()
	if err == nil {
		if sec, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil && sec > 0 {
			return int64(sec)
		}
	}
	return fileSize * 8 / 192000
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
