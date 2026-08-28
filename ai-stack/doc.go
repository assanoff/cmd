// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
ai-stack installs and maintains the local model stack the ai-* commands run on.

Usage:

	ai-stack install [-libs-only] [-q]
	ai-stack update [-q]
	ai-stack status
	ai-stack doctor
	ai-stack models [sync|gc] [-y] [-q]
	ai-stack use <role> <model> [-deployed]
	ai-stack version

The other commands — ai-hear, ai-ask, ai-sum, ai-tr — install what they need on
first use, so ai-stack is not a prerequisite for anything. It exists for the
work they should not each be doing: setting a machine up in one go, checking
that the pieces agree with each other, and moving a role from one model to
another.

Run "ai-stack <command> -h" for a command's own flags.

# install

Installs both native library bundles — llama.cpp for text models, whisper.cpp
for speech — and then downloads whatever model each bound role names. Already
installed pieces are left alone, so re-running it is how you check a machine is
complete. With -libs-only it stops after the libraries.

# update

Brings the native libraries to the versions this build of ai-stack was tested
with, and tells you how to move the whole set forward.

There is deliberately no "track the latest llama.cpp" option. Kronk, its yzma
binding, and llama.cpp are a tested set, and upgrading one member on its own is
how model loading breaks. Update the commands first, then the libraries follow:

	gup update ai-hear ai-ask ai-sum ai-tr ai-stack
	ai-stack update
	ai-stack doctor

# status

Prints where the data lives, which library bundles are installed and which one
is active, what each role resolves to and whether that model is on disk, and
what the whole stack costs on disk.

# doctor

Checks everything end to end and reports every problem rather than stopping at
the first: the sibling commands are installed and were built against the same
Kronk, ffmpeg and ffprobe are on PATH, both library bundles are present, the
config is where it is expected, and every bound role has its model.

The Kronk version check is the one worth understanding. All the ai-* commands
share ~/.kronk for native libraries and models, and each is its own Go module
with its own pinned Kronk. Two of them built against different versions will
disagree about which library bundle belongs in that shared directory. doctor
compares what each binary reports through -version and says so.

doctor exits 3 when anything failed, so a script can act on it.

# models

With no argument, lists every installed model with its size and the role that
claims it.

	ai-stack models sync    download what a bound role names and is missing
	ai-stack models gc      remove installed models no role points at

gc asks before deleting, and answers no on a non-interactive standard input: a
model is a multi-gigabyte download and the roles might simply be misconfigured.
Pass -y to skip the question.

# use

Points a role at a different model:

	ai-stack use smart unsloth/gemma-4-E4B-it-Q4_K_M

Roles are the indirection that keeps model names out of every command: a
command asks for "smart" and the config decides what that is, so switching
models is one edited line rather than a change in five programs. The roles are
fast, smart, code and embed for text models, and asr and asr-fast for speech.

By default use edits the config inside the dev-env repository, not the deployed
copy at ~/.config/ai/config. The deploy script copies env/.config/* over
$XDG_CONFIG_HOME and removes the target directory first, so an edit to the
deployed copy disappears the next time anything is deployed. Editing the
repository is what makes the change survive, at the cost of needing a deploy to
take effect. Pass -deployed to edit the live file anyway, for a change meant to
last only until the next deploy.

The file itself is a flat KEY=value list, and only the one line changes:
comments, blank lines and ordering are kept. A one-off override needs no file
at all, since the environment wins over it:

	AI_MODEL_SMART=other/model ai-sum notes.txt

# Exit status

ai-stack exits 0 on success, 1 on a failure, 2 on a usage error, and 3 when
something is not installed — including when doctor found a problem.
*/
package main
