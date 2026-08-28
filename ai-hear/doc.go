// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
ai-hear transcribes speech to text using whisper.cpp through the Kronk SDK.

Usage:

	ai-hear [-m model] [-l lang] [-f format] [-p prompt] [-t] [-words]
		[-r] [-ext list] [-o file | -O dir] [-suffix s]
		[-n] [-force] [-q] [file ...]

With no arguments ai-hear reads a media stream from standard input and writes
the transcript to standard output. Progress, decoded segments, and diagnostics
always go to standard error, so ai-hear composes in a pipeline:

	ai-hear lecture.mkv | ai-sum > lecture.md

Given files or directories, ai-hear processes those instead. A directory
contributes the media files directly inside it; add -r to descend into
subdirectories. The whisper model is loaded once for the whole run, so a batch
does not pay the load cost per file.

Inference happens in this process. Nothing needs to be running, and there is no
upload limit or request deadline to work around. On its first run the SDK
downloads a whisper.cpp library bundle for this machine and the requested model
into ~/.kronk, which it shares with the kronk command and the Kronk model
server.

# Models

The -m flag names the model either by role or explicitly. The roles are "asr"
(the default) and "asr-fast"; they resolve through ~/.config/ai/config, so no
model name is hard-coded here. Anything else is passed through as an explicit
whisper model name:

	ai-hear -m asr-fast talk.wav
	ai-hear -m large-v3-turbo talk.wav
	ai-hear -m base.en talk.wav

Models ending in .en are English-only and accept an empty -l or -l en.

# Language and translation

The -l flag is a whisper language hint such as ru, en, or de. Leave it empty to
let whisper detect the language; it defaults to $AI_LANG.

The -t flag turns on whisper's translation, which only ever produces English.
To translate into any other language, transcribe first and pipe the text
through ai-tr:

	ai-hear -l ru talk.mkv | ai-tr -t de

The -p flag seeds the decoder with prior context. Listing the terminology of a
recording measurably improves how those words come out:

	ai-hear -l ru -p 'Kubernetes, ArgoCD, Helm, ingress' talk.mkv

# Output

The -f flag selects the format: text (the default), json, srt, or vtt. Word
level timestamps come from -words and appear in json and vtt.

A single input writes to standard output. With -o it writes to that file
instead. Several inputs each get their own file: next to the source by default,
or below -O with the input layout preserved. In that batch mode standard output
carries the resulting paths, one per line, so the run stays composable:

	ai-hear -r ./media | xargs -n1 ai-sum

The output name is the input base name, plus -suffix, plus an extension chosen
by -f. Existing files are skipped unless -force is given. Use -n to print the
plan — which files were found, which model, where each result goes — and exit
without doing any work.

# Exit status

ai-hear exits 0 on success, 1 on a failure, 2 on a usage error, and 3 when the
whisper libraries or the requested model are not installed. In batch mode one
failing input does not stop the others; the run still ends with status 1.

# Notes

Reading from standard input feeds the stream to ffmpeg, which must be on PATH
for anything other than a plain WAV. Streamable containers (wav, mkv, opus,
flac) work this way, but an mp4 whose moov atom sits at the end of the file
cannot be decoded from a non-seekable pipe. Pass such files as arguments.

Flags must precede the file arguments: this command uses the standard library
flag package, which stops parsing at the first non-flag argument. That package
also treats -flag and --flag alike, so -h, -help, and --help all print this
command's flags.
*/
package main
