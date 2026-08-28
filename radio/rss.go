// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/xml"
)

// The feed document. Namespace prefixes are written out by hand rather than
// through encoding/xml's namespace support, which renders them as xmlns:_xmlns
// noise that podcast clients reject.

type cdataText struct {
	Text string `xml:",cdata"`
}

type itunesImage struct {
	Href string `xml:"href,attr"`
}

type itunesCategory struct {
	Text string `xml:"text,attr"`
}

type itunesOwner struct {
	Name  string `xml:"itunes:name"`
	Email string `xml:"itunes:email"`
}

// atomLink is the <atom:link rel="self"> the feed points at itself with.
type atomLink struct {
	Type string `xml:"type,attr"`
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
}

// pscChapter is one <psc:chapter> (Podlove Simple Chapters), the inline
// chapter format older players read.
type pscChapter struct {
	XMLName xml.Name `xml:"psc:chapter"`
	Start   int64    `xml:"start,attr"`
	Title   string   `xml:"title,attr"`
	Href    string   `xml:"href,attr,omitempty"`
}

type pscChapters struct {
	XMLName  xml.Name `xml:"psc:chapters"`
	Xmlns    string   `xml:"xmlns,attr"`
	Version  string   `xml:"version,attr"`
	Chapters []pscChapter
}

// podcastChapters points at the JSON chapter file, which is what current
// clients prefer.
type podcastChapters struct {
	XMLName xml.Name `xml:"podcast:chapters"`
	Type    string   `xml:"type,attr"`
	URL     string   `xml:"url,attr"`
}

type podcastTranscript struct {
	XMLName xml.Name `xml:"podcast:transcript"`
	Type    string   `xml:"type,attr"`
	URL     string   `xml:"url,attr"`
}

type rssDoc struct {
	XMLName     xml.Name   `xml:"rss"`
	NsAtom      string     `xml:"xmlns:atom,attr"`
	NsContent   string     `xml:"xmlns:content,attr"`
	NsItunes    string     `xml:"xmlns:itunes,attr"`
	NsPodcast   string     `xml:"xmlns:podcast,attr,omitempty"`
	NsPsc       string     `xml:"xmlns:psc,attr,omitempty"`
	Version     string     `xml:"version,attr"`
	ChannelData channelXML `xml:"channel"`
}

type channelXML struct {
	Title          string         `xml:"title"`
	Copyright      string         `xml:"copyright,omitempty"`
	Link           string         `xml:"link"`
	AtomLinkSelf   atomLink       `xml:"atom:link"`
	Language       string         `xml:"language"`
	Description    string         `xml:"description"`
	ItunesSummary  string         `xml:"itunes:summary,omitempty"`
	ItunesExplicit string         `xml:"itunes:explicit"`
	ItunesImage    *itunesImage   `xml:"itunes:image"`
	ItunesAuthor   string         `xml:"itunes:author"`
	ItunesOwner    itunesOwner    `xml:"itunes:owner"`
	ItunesKeywords string         `xml:"itunes:keywords,omitempty"`
	ItunesCategory itunesCategory `xml:"itunes:category"`
	Items          []itemXML      `xml:"item"`
}

type guidXML struct {
	Value       string `xml:",chardata"`
	IsPermaLink string `xml:"isPermaLink,attr,omitempty"`
}

type itemXML struct {
	Title             string             `xml:"title"`
	GUID              guidXML            `xml:"guid"`
	Link              string             `xml:"link,omitempty"`
	PubDate           string             `xml:"pubDate"`
	Enclosure         enclosure          `xml:"enclosure"`
	Description       string             `xml:"description"`
	ItunesSummary     *cdataText         `xml:"itunes:summary,omitempty"`
	ItunesEpisodeType string             `xml:"itunes:episodeType,omitempty"`
	ItunesImage       *itunesImage       `xml:"itunes:image,omitempty"`
	ItunesDuration    string             `xml:"itunes:duration,omitempty"`
	ItunesExplicit    string             `xml:"itunes:explicit"`
	PodcastChapters   *podcastChapters   `xml:"podcast:chapters,omitempty"`
	PodcastTranscript *podcastTranscript `xml:"podcast:transcript,omitempty"`
	PscChapters       *pscChapters       `xml:"psc:chapters,omitempty"`
	ContentEncoded    cdataText          `xml:"content:encoded"`
}

type enclosure struct {
	Type   string `xml:"type,attr"`
	Length string `xml:"length,attr"`
	URL    string `xml:"url,attr"`
}

func encodeFeed(doc rssDoc) ([]byte, error) {
	buf := &bytes.Buffer{}
	buf.WriteString(xml.Header)

	enc := xml.NewEncoder(buf)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Flush(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
