// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
ai-tr translates text with a local language model through the Kronk SDK.

Usage:

	ai-tr [-t lang] [-F lang] [-m model] [-keep-format] [-map-tokens n]
		[-T temp] [-max-tokens n] [-think]
		[-r] [-ext list] [-o file | -O dir] [-suffix s]
		[-n] [-force] [-q] [file ...]

With no file arguments ai-tr reads standard input and writes the translation to
standard output, so it sits in the middle of a pipeline:

	ai-hear -l ru talk.mkv | ai-tr -t en | ai-sum -s brief

Given files or directories, ai-tr translates each one. The model is loaded once
for the whole run, so a batch does not pay the load cost per file. A directory
contributes the text files directly inside it; add -r to descend into
subdirectories.

# Direction

The -t flag is the language to translate into, by short code; it defaults to
en. The -F flag names the source language, which is worth setting when the
input is short enough to be ambiguous. Both accept any code, and the common
ones are turned into the language name a model recognizes.

ai-tr translates in any direction. This is what distinguishes it from
ai-hear -t, which is whisper's own translation and only ever produces English.
To get Russian speech into German, transcribe first and translate after:

	ai-hear -l ru talk.mkv | ai-tr -t de

# Long documents

Input longer than one call is split at paragraph boundaries and translated
piece by piece, then joined back together. There is no folding step the way
ai-sum has one: a translation is as long as its original, so there is nothing
to compress.

Chunk sizes come from the model's own tokenizer, not from a bytes-per-token
guess, which is what makes naive chunkers overflow on Cyrillic. Set the size
with -map-tokens; it defaults to $AI_MAP_TOKENS, then to 2000. That is smaller
than ai-sum's default on purpose: here the answer is as long as the input, and
both have to fit in the context window at once.

# Subtitles

With -keep-format, SubRip and WebVTT input has only its spoken text translated;
every index and timing line is carried through untouched. Timings are data, not
prose, and a model asked to translate them will eventually round one.

Cues are sent in batches, numbered, and must come back numbered. A model that
drops, merges, or invents an item would shift every later subtitle against its
timing, so a batch whose numbering does not survive the round trip is redone one
cue at a time rather than trusted.

Subtitles are recognized by content rather than by file name, so a piped stream
works too. Given -keep-format on input that is not subtitles, ai-tr says so on
standard error and translates it as ordinary text.

# Models

The -m flag names the model either by role or explicitly. The roles are "fast"
(the default), "smart", and "code"; they resolve through ~/.config/ai/config,
so no model name is hard-coded here. Anything else is passed through as a
canonical provider/modelID.

Translating is a mechanical job, so reasoning is off by default: it triples the
token spend and none of it reaches the translation. Turn it on with -think if a
particular model needs it.

# Output

A single input writes to standard output. With -o it writes to that file
instead. Several inputs each get their own file: next to the source by default,
or below -O with the input layout preserved. In that batch mode standard output
carries the resulting paths, one per line.

The output keeps the input's own extension, because translating an .srt has to
produce an .srt. The base name gets -suffix, which defaults to the target
language, so the same input translated twice lands in two different files.
Existing files are skipped unless -force is given. Use -n to print the plan and
exit without translating anything.

# Exit status

ai-tr exits 0 on success, 1 on a failure, 2 on a usage error, and 3 when the
llama.cpp libraries or the requested model are not installed. In batch mode one
failing input does not stop the others; the run still ends with status 1.

# Notes

Flags must precede the file arguments: this command uses the standard library
flag package, which stops parsing at the first non-flag argument. That package
also treats -flag and --flag alike, so -h, -help, and --help all print this
command's flags.
*/
package main
