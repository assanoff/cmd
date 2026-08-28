// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
ai-sum summarizes text with a local language model through the Kronk SDK.

Usage:

	ai-sum [-s styles] [-L lang] [-m model] [-map-tokens n] [-no-reduce]
		[-ts dur] [-c file] [-ctx-tokens n]
		[-T temp] [-max-tokens n] [-think] [-f format]
		[-r] [-ext list] [-o file | -O dir] [-suffix s]
		[-n] [-force] [-q] [file ...]

With no file arguments ai-sum reads standard input and writes the summary to
standard output, so it takes the tail of a pipeline:

	ai-hear lecture.mkv | ai-sum > lecture.md

Given files or directories, ai-sum summarizes each one. The model is loaded
once for the whole run, so a batch does not pay the load cost per file. A
directory contributes the text files directly inside it; add -r to descend
into subdirectories.

# Subtitles and time markers

Input is checked for subtitles by its content, not its name, so a piped stream
counts. SubRip and WebVTT are read as prose: the cues are joined back into
paragraphs, and each paragraph opens with the time it starts at, as [HH:MM:SS].

	ai-hear -f srt lecture.mkv > lecture.srt
	ai-sum -s chapters lecture.srt

That is how a time reaches the summary. Nothing else in the pipeline knows
about times: the marker is ordinary text, the map prompt is told to carry it
through unchanged, and the chapters prompt is told to open each section with
it. Because a marker sits at the start of a paragraph and chunks break at
paragraph boundaries, it stays attached to its own text through any number of
fold rounds.

Set the spacing with -ts; it defaults to one minute, and -ts 0 drops the times
and reads the subtitles as plain prose.

# Supporting material

The -c flag names a file of supporting material — lecture slides, a syllabus, an
existing outline — and may be repeated. The material is given to the model at
the point where the summary is worded, and never during compression: what gets
compressed has to stay faithful to what the text actually says, while a name
spelled correctly on a slide is worth a lot when the transcript misheard it.

Supporting material is capped at -ctx-tokens (2000 by default) and cut at a
paragraph boundary, with the loss reported on standard error. The cap is there
because -c is usually filled by a script globbing a directory, and one folder
can hold a book.

# How long input is handled

A document that fits in one chunk goes straight to the style prompt, so a
short text is summarized exactly as written.

Anything longer is folded. Each chunk is compressed on its own — the map phase
— and the compressions are then summarized together — the reduce phase. If the
compressions are still too long, the fold repeats, up to three rounds.

Chunk sizes come from the model's own tokenizer, not from a bytes-per-token
guess. That guess is what makes naive chunkers overflow on Cyrillic: UTF-8
spends two bytes per letter there, so a byte estimate can be off by a factor of
two either way. Set the size with -map-tokens; it defaults to $AI_MAP_TOKENS,
then to 6000.

Chunks break at paragraph boundaries. A paragraph over the limit is broken at
sentences, and a sentence over the limit at words — which only happens on text
with no punctuation at all, such as a raw transcript.

Use -no-reduce to stop after the map phase and get the per-chunk compressions
instead of one summary. That is the right shape when the input is a set of
independent notes rather than one document.

# Styles

The -s flag chooses what the summary looks like:

	brief      a few sentences: what this is and what matters in it
	terms      the vocabulary the text relies on, term and meaning
	chapters   a short overview, then the key points topic by topic
	facts      every number, date, name, version, command and limit stated
	tips       practical advice: techniques, defaults, warnings
	actions    action items and decisions, one per line

Styles are prompt files embedded in the binary, so changing what a style
produces is an edit to prose rather than to code.

Several styles separated by commas produce one document with a heading per
section, in the order given:

	ai-sum -s brief,terms,chapters,facts,tips -f md lecture.srt

The document is folded once and every style works from that same folded
material, so five sections cost about what one costs. The fold is many calls
over the whole text; a style pass is a single call over what the fold left.
This is why ai-notes asks for its sections in one process rather than running
ai-sum once per section.

A single style prints its summary with no heading, exactly as before, so a
one-style run still composes in a pipe.

The -L flag is the language the summary is written in, by short code such as ru
or en. It defaults to $AI_OUT_LANG, then to ru. The prompts themselves are
language-independent: they name the output language rather than being written
in it.

# Models

The -m flag names the model either by role or explicitly. The roles are "smart"
(the default), "fast", and "code"; they resolve through ~/.config/ai/config, so
no model name is hard-coded here. Anything else is passed through as a
canonical provider/modelID.

Summarizing is a compression job, so reasoning is off by default: it triples
the token spend and none of it reaches the summary. Turn it on with -think if a
particular model needs it.

# Output

The -f flag selects text (the default) or md. They differ only in the extension
chosen for a batch, which matters when the summaries are destined for a notes
directory.

A single input writes to standard output. With -o it writes to that file
instead. Several inputs each get their own file: next to the source by default,
or below -O with the input layout preserved. In that batch mode standard output
carries the resulting paths, one per line.

The output name is the input base name, plus -suffix (which defaults to -sum),
plus an extension chosen by -f. Existing files are skipped unless -force is
given. Use -n to print the plan and exit without summarizing anything.

# Exit status

ai-sum exits 0 on success, 1 on a failure, 2 on a usage error, and 3 when the
llama.cpp libraries or the requested model are not installed. In batch mode one
failing input does not stop the others; the run still ends with status 1.

# Notes

Flags must precede the file arguments: this command uses the standard library
flag package, which stops parsing at the first non-flag argument. That package
also treats -flag and --flag alike, so -h, -help, and --help all print this
command's flags.
*/
package main
