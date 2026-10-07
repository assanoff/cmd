// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
ai runs language and speech models locally, through the Kronk SDK.

Usage:

	ai <command> [options] [file ...]

The commands are:

	hear    transcribe speech to text
	ask     run one prompt against a local language model
	sum     summarize text, map-reduce over long input
	tr      translate text
	stack   install and maintain the local model stack

Run "ai <command> -h" for a command's own flags, and "ai stack <command> -h"
for one of the stack's.

Inference happens in this process. On its first run the SDK downloads a
llama.cpp or whisper.cpp bundle for this machine and the requested model into
~/.kronk, which it shares with the kronk command and the Kronk model server.

# Composing

Every command reads standard input and writes standard output when given no
file arguments, so they compose:

	ai hear lecture.mkv | ai tr -t en | ai sum -s brief

Times travel through the pipeline as subtitles. "ai hear -f srt" writes them,
"ai tr --keep-format" translates the words and leaves the timings alone, and
"ai sum" reads subtitles as timestamped prose — which is what makes a chapter
list say where each chapter starts:

	ai hear -f srt lecture.mkv | ai sum -s brief,terms,chapters,facts,tips -f md

Given file or directory arguments the commands process those instead. The
model is loaded once for the whole run, so a batch does not pay the load cost
per file. A directory contributes the files directly inside it that match
--ext; add -r to descend into subdirectories.

A single input writes to standard output, or to -o. Several inputs each get
their own file: next to the source by default, or below -O with the input
layout preserved. In that batch mode standard output carries the resulting
paths, one per line. Existing files are skipped unless --force is given, so
re-running an interrupted batch only does what is left. Use -n to print the
plan and exit without asking a model anything.

# Models

Models are named by role rather than by name, so no command hard-codes one.
The roles are "fast", "smart", "code" and "embed" for text and "asr" and
"asr-fast" for speech; they resolve through ~/.config/ai/config, a flat
KEY=value file, and the process environment overrides it. Anything that is not
a role is passed through as a canonical provider/modelID:

	ai ask -m smart -p 'review this design' < design.md
	ai ask -m unsloth/Qwen3-1.7B-UD-Q8_K_XL -p hello

A bare model id without its provider is rejected by Kronk; use the canonical
form that "ai stack status" lists.

	ai stack install        install the libraries and every model a role names
	ai stack status         what is installed, what each role resolves to
	ai stack doctor         check the whole thing end to end
	ai stack use smart X    point a role at a different model

# Exit status

ai exits 0 on success, 1 on a failure, 2 on a usage error, and 3 when the
native libraries or the requested model are not installed. In batch mode one
failing input does not stop the others; the run still ends with status 1.

# Flags

Flags are GNU style: -m or --model, --max-tokens with two dashes, and a value
may be attached with = or given as the next argument. Flags may follow the
file arguments as well as precede them.

# Layout

The implementation lives under internal/, split so that the parts that talk to
the SDK are in one place each:

	internal/cli            exit codes, progress, input and output
	internal/core/llm       the Kronk engine: every text model goes through it
	internal/core/asr       the Bucky engine: every speech model does
	internal/core/config    ~/.config/ai/config and the role table
	internal/core/chunk     token-aware splitting
	internal/core/subs      SubRip and WebVTT
	internal/core/walk      arguments to jobs, and where each result goes
	internal/core/prompt    prompt templates
	internal/cmd/*          one package per subcommand: flags and control flow

This was five separate commands and five separate modules before, each with its
own copy of the config reader, the model loader and the directory walker. The
copies had already drifted — the same role resolved to different models
depending on which command you asked — which is what the single core is for.
*/
package main
