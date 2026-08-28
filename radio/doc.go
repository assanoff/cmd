// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
radio publishes a directory of audio as a podcast.

Usage:

	radio gen   [-u url] [-p port] [-prefix path] [-category name] [-n] [-q] [dir]
	radio serve [-a addr] [-p port] [-prefix path] [-q] [dir]

There is no database and no registration. A podcast is a directory: audio
files, a descriptor beside each one, and a cover. Point radio at it and it
writes the feed; point serve at the same directory and a phone can subscribe.

	cd ~/mixes/late-set-12
	radio gen .
	radio serve .

gen prints the paths it wrote and serve prints the addresses to subscribe to,
one per line on standard output; everything else goes to standard error.

# What a directory has to hold

For each episode, an audio file and a .pl descriptor with the same base name:

	Late Set 12.mp3
	Late Set 12.pl

The descriptor is read by position, and the four-line preamble is required:

	line 1   title
	line 2   blank
	line 3   description
	line 4   blank
	line 5+  tracklist, "NN. H:MM:SS Artist — Track [url]"

Skip the blank lines and the first track becomes the description and the
second is lost. The media/mix and media/chapters scripts in dev-env write this
format; so does anything that follows the shape above.

Three optional siblings are picked up when present: "<episode>.jpg" as episode
art, "cover.jpg" or "cover.png" as the channel art, and a transcript named
"<episode>" with a .scr, .txt, .html, .htm or .md extension — which is the name
ai-notes already gives it.

# One podcast or a shelf of them

A directory holding .pl files is one podcast, named after the directory. A
directory whose subdirectories hold them is a collection, and each
subdirectory becomes its own feed. So both of these work:

	radio gen .                 # standing in a finished mix
	radio gen ~/radio           # an archive of a dozen stations

Each feed is written as feed.xml inside its own directory, next to a
"<episode>.chapters.json" per episode. Both are derived files: they are
rewritten on every run and belong in .gitignore, not in a commit.

# Where the links point

Every URL in a feed is absolute, because a podcast client fetches the audio
separately from the feed. The base URL therefore has to be an address the
listening device can reach — which is why the default is this machine's
address on the local network rather than localhost, and why gen prints the
address it chose on every run.

Override it when the collection is served from somewhere else:

	radio gen -u https://radio.example.com .

The path the collection sits under defaults to /radio and is part of the
published contract: change -prefix and every enclosure URL a subscriber
already holds stops resolving. It is a machine-wide setting rather than a
per-directory one, because gen and serve have to agree on it.

# Serving

	radio serve .

serve hands out the directory over HTTP at the addresses gen wrote into the
feeds, so the two have to be run with the same port and prefix. It is a plain
file server with the content types podcast clients need — an RSS type on
feed.xml, the chapters media type on the JSON sidecars — and it answers range
requests, which is what lets a client seek inside an episode instead of
downloading it whole.

Opening the root in a browser lists the feed addresses, which is easier than
typing one off a terminal into a phone.

# Settings

Channel metadata comes from a flat KEY=value file, read from the most general
place to the most specific: ~/.config/radio/config, then radio.conf in the
podcast's own directory, then the environment, then flags. Keys may be written
with or without the RADIO_ prefix in a file; the environment needs it.

	TITLE=Late Sets
	AUTHOR=Rustam Assanov
	CATEGORY=Music
	LANGUAGE=en-us

Recognized keys are TITLE, AUTHOR, EMAIL, DESCRIPTION, CATEGORY, LANGUAGE,
LINK, EXPLICIT, COPYRIGHT, BASE_URL, PREFIX and PORT. Anything unset falls back
to the directory name, which is enough to get a working feed out of a folder
with nothing configured at all.

# Dates

An episode is dated by its audio file, falling back to its descriptor. The
obvious alternative, the moment of generation, re-dates the whole archive on
every run: clients treat every episode as newly published and a feed under
version control produces a diff each time it is rebuilt.

# Chapters

Each episode's tracklist is published twice, because clients disagree about
where to look: as a Podcast Index JSON file that <podcast:chapters> points at,
and inline as Podlove Simple Chapters. Both come from the same .pl.

Chapters inside the audio file are a separate thing and radio does not touch
the media. Writing them is the job of whoever produced the file — the
media/chapters script in dev-env does it with ffmpeg at mix time.
*/
package main
