// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
chapters writes a timecoded tracklist into an audio file as real chapters.

Usage:

	chapters [-t title] [-i cover] [-l list] [-p out.pl] [-o out] [-n] [-q] <audio> [tracklist]
	chapters [options] <dir>

A tracklist is nearly worthless in a text file beside the audio and useful
inside it: players show the chapter names on the scrubber, seek between them,
and offer a table of contents.

	chapters set.mp3 set.pl                        # the .pl beside it
	chapters set.mp3                               # same, found automatically
	yt-dlp --get-description URL | chapters set.mp3
	chapters ./mixes                               # each file with its own .pl

The audio is stream-copied, so nothing is re-encoded and the sound is
bit-identical. Read the result back with:

	ffprobe -show_chapters -of default=nw=1 file.mp3

# What counts as a tracklist

Any line carrying a timestamp becomes a chapter and every other line is
ignored, which is what makes it safe to paste a whole video description:

	0:00 Artist - Track
	1. 04:31 Artist — Track
	[1:02:14] Artist – Track
	Artist - Track 7:45

The time may lead or trail, the index is optional, hours are optional,
brackets are allowed, and a trailing link is dropped from the title. That
tolerance is deliberate: a DJ set on a video site almost always carries its
tracklist in the description, in one of those shapes, surrounded by prose.

A file named .pl is read as the descriptor format radio uses, whose first four
lines are a title, a blank, a description and a blank. Those four are skipped
rather than scanned, because a description like "Recorded live at 21:30" would
otherwise parse as a chapter.

Chapters are sorted, and any that does not move forward in time is dropped
with a note on standard error. A repeated timestamp makes a zero-length
chapter, which players render as a glitch rather than as an error.

# Where the tracklist comes from

A named file, then standard input, then the .pl beside the audio.

The pipe outranks the sibling deliberately: piping a description in is a thing
someone chose to do, while a .pl next to the file is often left over from an
earlier run, and preferring it would quietly write the old tracklist and
report success. An empty pipe does not count as a choice, so the sibling is
still found when standard input is closed — inside a script, or under cron.

# Writing

Without -o the file is rewritten in place, through a temporary in the same
directory, so an interrupted run cannot leave a half-written mix where the
original was. With -o the original is untouched.

Running it again is the normal case, not an accident: a wrong timestamp is
something you notice after listening. Fix the tracklist, run it again, and the
chapters are replaced rather than added to — and nothing else about the file
changes. The tags it already carries are kept, the cover from an earlier run
survives a run without -i, and the title is only touched when -t says so.
Defaulting the title to the file name would quietly replace a real one with
something worse, once per run.

The -i flag attaches a cover, marked as cover art rather than as a one-frame
video stream, and -p writes a normalized .pl beside the result — which is how
a tracklist pasted from a video description ends up in a podcast feed.

Given a directory, every audio file in it is processed with the .pl lying
beside it, and files without one are left alone.

# Dependencies

ffmpeg and ffprobe. Chapters go in through ffmpeg's own metadata format;
nothing else is needed and no library is linked in.

The alternative was mutagen, the library the Python pipeline this replaces
used. It works, but it asks every machine that publishes a mix to carry a
Python install — and because that dependency was never actually met, the ID3
chapters that pipeline meant to write were never written at all. ffmpeg was
already required by everything around this.
*/
package main
