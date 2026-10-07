# cmd

Commands.

Each directory is a standalone program with its own `go.mod`, so installing one
command never drags in another's dependencies.

| Command | What it does |
| ------- | ------------ |
| [ai](ai) | Transcribe, prompt, summarize and translate with local models |
| [radio](radio) | Publish a directory of audio as a podcast feed |
| [chapters](chapters) | Write a timecoded tracklist into an audio file |

## Install

```sh
go install github.com/assanoff/cmd/ai@latest
```

Every command documents itself: run it with `-h` for the flags, or read its
`doc.go`. Go 1.27 or newer is required; `GOTOOLCHAIN=auto` fetches it for you.

One wrinkle on Windows: `ai` parses its flags with
[go-flags](https://github.com/jessevdk/go-flags), which there renders its own
help and errors in DOS style — `/model`, `/h` — while this README and every
`doc.go` use `--model` and `-h`. Both spellings parse, so nothing breaks and no
script has to change; only the generated help disagrees with the prose. The
`forceposix` build tag would align them, at the cost of an install command
nobody would guess and a `gup update` that silently drops it, which is a worse
trade than the mismatch.

## ai

`ai` runs language and speech models locally through
[Kronk](https://github.com/ardanlabs/kronk) — llama.cpp for text and whisper.cpp
for speech. There is no server and no Python: the SDK downloads the native
libraries and model files on first use and runs inference in-process.

```
ai hear    transcribe speech to text
ai ask     run one prompt against a local language model
ai sum     summarize text, map-reduce over long input
ai tr      translate text
ai stack   install and maintain the local model stack
```

Each reads standard input and writes standard output when given no arguments,
so they compose:

```sh
ai hear lecture.mkv | ai tr -t en | ai sum -s brief
```

Times travel through the pipeline as subtitles. `ai hear -f srt` writes them,
`ai tr --keep-format` translates the words and leaves the timings alone, and
`ai sum` reads subtitles as timestamped prose — which is what makes a chapter
list say where each chapter starts:

```sh
ai hear -f srt lecture.mkv | ai sum -s brief,terms,chapters,facts,tips -f md
```

Given file or directory arguments they process those instead, loading the model
once for the whole batch. Roles (`fast`, `smart`, `asr`, ...) map to concrete
model names through `~/.config/ai/config`, so nothing hard-codes a model.

```sh
ai stack install          # libraries plus every model a role names
ai stack status           # what is installed, what each role resolves to
ai stack doctor           # check the whole thing end to end
ai stack use smart X      # point a role at a different model
```

### Layout

One binary, one module, and the implementation under `internal/`:

```
ai/
  main.go                 the parser and the dispatch table
  internal/
    cli/                  exit codes, progress, the batch driver, input and output
    llm/                  the Kronk engine — every text model goes through it
    asr/                  the Bucky engine — every speech model does
    config/               ~/.config/ai/config and the role table
    chunk/  subs/  walk/  prompt/
    cmd/
      ask/  hear/  sum/  tr/  stack/
```

This was five commands in five modules before (`ai-ask`, `ai-hear`, `ai-sum`,
`ai-tr`, `ai-stack`), each carrying its own copy of the config reader, the
model loader and the directory walker. The copies had drifted — the same role
resolved to different models depending on which command you asked — and a Kronk
bump meant editing five `go.mod` files that all installed into the same
`~/.kronk`. Sharing one set of packages is what that was for:
`internal/llm` is the only place this program runs a text model, and
`internal/asr` the only place it runs a speech model.

If the old binaries are still on your `PATH` they will keep pulling the shared
`~/.kronk` bundle back to whatever SDK they were built against. `ai stack
doctor` names them; remove them.

### Keeping the Kronk version in step

Kronk, its yzma binding and llama.cpp are a tested set, so they move together.
With one module that is now one command:

```sh
cd ai && go get github.com/ardanlabs/kronk@vX.Y.Z && go mod tidy
```

`ai --version` prints the Kronk version the binary was built against, and
`ai stack update` brings the native libraries to match it.

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
independently: a fix in `radio` should not spend a version of `ai`. This is a
multi-module repository, so the tags carry the module directory as a prefix —
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
