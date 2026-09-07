# cmd

Commands.

Each directory is a standalone program with its own `go.mod`, so installing one
command never drags in another's dependencies.

| Command | What it does |
| ------- | ------------ |
| [ai-hear](ai-hear) | Transcribe speech to text (whisper.cpp via the Kronk SDK) |
| [ai-ask](ai-ask) | Run one prompt against a local language model |
| [ai-sum](ai-sum) | Summarize text, map-reduce over long input |
| [ai-tr](ai-tr) | Translate text with a local language model |
| [ai-stack](ai-stack) | Install and maintain the local model stack |
| [radio](radio) | Publish a directory of audio as a podcast feed |
| [chapters](chapters) | Write a timecoded tracklist into an audio file |

## Install

```sh
go install github.com/assanoff/cmd/ai-hear@latest
```

Every command documents itself: run it with `-h` for the flags, or read its
`doc.go`. Go 1.27 or newer is required; `GOTOOLCHAIN=auto` fetches it for you.

## The ai-* family

The `ai-*` commands run models locally through
[Kronk](https://github.com/ardanlabs/kronk) — llama.cpp for text and whisper.cpp
for speech. There is no server and no Python: the SDK downloads the native
libraries and model files on first use and runs inference in-process.

They read standard input and write standard output when given no arguments, so
they compose:

```sh
ai-hear lecture.mkv | ai-tr -t en | ai-sum -s brief
```

Times travel through the pipeline as subtitles. `ai-hear -f srt` writes them,
`ai-tr` translates the words and leaves the timings alone, and `ai-sum` reads
subtitles as timestamped prose — which is what makes a chapter list say where
each chapter starts:

```sh
ai-hear -f srt lecture.mkv | ai-sum -s brief,terms,chapters,facts,tips -f md
```

Given file or directory arguments they process those instead, loading the model
once for the whole batch. Roles (`fast`, `smart`, `asr`, ...) map to concrete
model names through `~/.config/ai/config`, so no command hard-codes a model.

### Keeping the Kronk version in step

The `ai-*` commands share `~/.kronk` for native libraries and models, and Kronk,
its yzma binding, and llama.cpp form a tested set. Bump them together:

```sh
for d in ai-*; do (cd "$d" && go get github.com/ardanlabs/kronk@vX.Y.Z && go mod tidy); done
```

`ai-hear -version` prints the Kronk version each binary was built against, and
`ai-stack doctor` compares them across the installed commands.

## radio and chapters

These two are the odd ones out: no models, no Go dependencies at all — only
ffmpeg on the path. Together they turn a folder of audio into something a
phone can subscribe to.

```sh
cd ~/mixes/late-set-12
chapters .           # tracklists into the files, as real chapters
radio gen .          # writes feed.xml and the chapter sidecars
radio serve .        # hands the directory out, prints what to subscribe to
```

A podcast is a directory — audio, a `.pl` descriptor beside each file, a cover
— and the feed is written into it. There is no database and nothing to
register. Feed URLs default to this machine's address on the local network,
because the device that subscribes is never this one.

The split is by where the chapters go. `chapters` writes them **into** the
audio, so any player shows them; `radio` publishes them **around** it, in the
feed and in a JSON sidecar. Same tracklist, two destinations, one file format
between them.

`radio <command> -h` and `chapters -h` list the flags; each `doc.go` explains
the rest.

## Versioning

Every command carries its own semver line, because they are released
independently: a fix in `radio` should not spend a version of `ai-hear`. This is
a multi-module repository, so the tags carry the module directory as a prefix —
the only form `go install github.com/assanoff/cmd/radio@v0.1.0` can resolve:

```sh
git tag radio/v0.1.0
```

The `Makefile` does the tagging, and checks the version before it does — the
targets below are thin wrappers over `scripts/release.sh`, which is where the
version arithmetic and the checks live. Each command reports the version it was
built from through `-version`, read out of the build info Go stamps into a
binary installed from a tag.

```sh
make versions                       # what every command is at
make changes MOD=radio              # commits touching radio since its last tag
make release-suggest MOD=radio      # the step those commit messages call for
make release-patch MOD=radio        # tag and push it
make release MOD=radio VERSION=v0.2.0
```

A release only goes out if the working tree is clean, the version is exactly one
patch, minor, or major step above that command's latest tag, something in that
directory actually changed since it (`FORCE=1` overrides), and the module builds
and tests. `release-suggest` reads the conventional-commit subjects — `feat:` is
a minor step, a `!` marker or `BREAKING CHANGE` is major, anything else a patch —
and below v1 a breaking change becomes a minor step instead. `release-auto`
tags the version it names.

Going to v2 needs the module path to end in `/v2` first; `make release-major`
says so rather than cutting a tag Go cannot resolve.

Development targets take the same `MOD=` selector and run over every command
without it. `make check` is the gate: `fmt-check`, `vet`, `lint`, `test`.

```sh
make check              # all commands
make test MOD=radio     # just this one
make help               # the rest
```
