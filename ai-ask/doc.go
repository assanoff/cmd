// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
ai-ask runs one prompt against a local language model through the Kronk SDK.

Usage:

	ai-ask [-p prompt | -P file] [-m model] [-s system] [-T temp]
		[-max-tokens n] [-think] [-f format]
		[-r] [-ext list] [-o file | -O dir] [-suffix s]
		[-n] [-force] [-q] [file ...]

ai-ask reads text, applies an instruction to it, and writes the answer. With no
file arguments it reads standard input and writes to standard output, so it
takes the tail of a pipeline:

	ai-hear lecture.mkv | ai-ask -p 'list every command mentioned'

The instruction comes from -p, or from a file with -P. Without either, the
input itself is the prompt, which makes the short form work as expected:

	echo 'why is a Go map iteration order random?' | ai-ask

Given files or directories, ai-ask applies the same prompt to each one. The
model is loaded once for the whole run, so a batch does not pay the load cost
per file. A directory contributes the text files directly inside it; add -r to
descend into subdirectories.

Inference happens in this process. On its first run the SDK downloads a
llama.cpp library bundle for this machine and the requested model into
~/.kronk, which it shares with the kronk command and the Kronk model server.

# Models

The -m flag names the model either by role or explicitly. The roles are "fast"
(the default), "smart", and "code"; they resolve through ~/.config/ai/config,
so no model name is hard-coded here. Anything else is passed through as a
canonical provider/modelID:

	ai-ask -m smart -p 'review this design' < design.md
	ai-ask -m unsloth/Qwen3-1.7B-UD-Q8_K_XL -p hello

A bare model id without its provider is rejected by Kronk; use the canonical
form that ai-stack status lists.

# Generation

The -s flag sets a system message. The -T flag sets the sampling temperature
and -max-tokens caps the answer length. On models that support it, -think turns
reasoning on and -no-think turns it off; reasoning text goes to standard error,
never into the answer.

# Output

The -f flag selects the format: text (the default), md, or json. Text and md
differ only in the extension chosen for a batch, which matters when the answer
is destined for a notes directory. The json form carries the full response
including token usage, which is the first thing to look at when a prompt
behaves oddly.

The answer streams to the destination as it is generated, so a long generation
shows progress rather than sitting silent.

A single input writes to standard output. With -o it writes to that file
instead. Several inputs each get their own file: next to the source by default,
or below -O with the input layout preserved. In that batch mode standard output
carries the resulting paths, one per line.

The output name is the input base name, plus -suffix, plus an extension chosen
by -f. Existing files are skipped unless -force is given. Use -n to print the
plan and exit without asking the model anything.

# Exit status

ai-ask exits 0 on success, 1 on a failure, 2 on a usage error, and 3 when the
llama.cpp libraries or the requested model are not installed. In batch mode one
failing input does not stop the others; the run still ends with status 1.

# Notes

Flags must precede the file arguments: this command uses the standard library
flag package, which stops parsing at the first non-flag argument. That package
also treats -flag and --flag alike, so -h, -help, and --help all print this
command's flags.
*/
package main
