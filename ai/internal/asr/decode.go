// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package asr

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	bmodel "github.com/ardanlabs/kronk/sdk/bucky/model"

	"github.com/assanoff/cmd/ai/internal/cli"
)

// sampleRate is what whisper.cpp consumes, and the rate ffmpeg is asked to
// resample to. It is not configurable because whisper has no other setting.
const sampleRate = 16000

// sniffBytes is the prefix read to recognize a container. Twelve is what the
// SDK's own sniffer looks at, and enough for every magic below.
const sniffBytes = 12

// TranscribePath decodes and transcribes a file on disk.
//
// It exists because the SDK's reader-based path cannot read an ordinary video
// file. For WAV, MP3 and FLAC the SDK decodes in process and all is well;
// anything else it pipes to ffmpeg's standard input, which is the right design
// for the browser uploads it was written for and the wrong one for a file on
// disk. An MP4 or MOV written without -movflags +faststart keeps its moov atom
// after the payload, and ffmpeg cannot seek backwards in a pipe to reach it.
// It then decodes nothing and exits 0, so the failure arrives as whisper's
// "empty samples" rather than as an error from ffmpeg — which is a long way
// from "your video is not streamable".
//
// Handing ffmpeg the path costs nothing and lets it seek. The formats the SDK
// decodes natively still take the SDK's own route, so ffmpeg stays optional
// for audio and required only for video, which is what ai stack doctor says.
func (e *Engine) TranscribePath(ctx context.Context, path string, o Options) (Transcription, error) {
	f, err := os.Open(path)
	if err != nil {
		return Transcription{}, err
	}
	defer func() { _ = f.Close() }()

	head := make([]byte, sniffBytes)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return Transcription{}, err
	}

	var samples []float32
	if native(head[:n]) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return Transcription{}, err
		}
		samples, err = bmodel.Decode(ctx, f)
	} else {
		samples, err = decode(ctx, path)
	}
	if err != nil {
		return Transcription{}, err
	}
	return e.transcribe(ctx, samples, o)
}

// native reports whether the SDK decodes this container itself. It mirrors the
// SDK's unexported sniffer: RIFF/WAVE, FLAC, and MP3 with or without an ID3
// header. Getting it wrong is not fatal in either direction — a false positive
// surfaces as the SDK's own unsupported-format error, a false negative only
// sends a file through ffmpeg that need not have gone.
func native(head []byte) bool {
	switch {
	case len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WAVE":
		return true
	case len(head) >= 4 && string(head[0:4]) == "fLaC":
		return true
	case len(head) >= 3 && string(head[0:3]) == "ID3":
		return true
	case len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0:
		return true
	}
	return false
}

// decode runs ffmpeg over the file and returns the samples whisper wants.
//
// The PCM is converted as it arrives rather than buffered whole and converted
// after: an hour of audio is 115MB of 16-bit PCM and 230MB of float32, and
// holding both at once doubles the peak for no reason.
func decode(ctx context.Context, path string) ([]float32, error) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, cli.NotSetupf("%s needs ffmpeg to decode: %w", filepath.Base(path), err)
	}

	cmd := exec.CommandContext(ctx, bin,
		"-nostdin",
		"-loglevel", "error",
		"-i", path,
		"-vn",
		"-f", "s16le",
		"-ac", "1",
		"-ar", fmt.Sprint(sampleRate),
		"-",
	)

	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	samples, readErr := pcm16(out)

	// Wait after the read, never before: ffmpeg blocks writing to a pipe
	// nobody is draining, and waiting first would deadlock on any input
	// larger than the pipe buffer.
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("ffmpeg %s: %s", filepath.Base(path), lastLine(msg))
		}
		return nil, fmt.Errorf("ffmpeg %s: %w", filepath.Base(path), err)
	}
	if readErr != nil {
		return nil, readErr
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("%s: no audio to transcribe", filepath.Base(path))
	}
	return samples, nil
}

// pcm16 converts a stream of signed 16-bit little-endian samples to the
// float32 in [-1, 1] that whisper.cpp takes.
func pcm16(r io.Reader) ([]float32, error) {
	br := bufio.NewReaderSize(r, 1<<16)
	buf := make([]byte, 1<<16)

	var samples []float32
	for {
		n, err := io.ReadFull(br, buf)
		for i := 0; i+1 < n; i += 2 {
			samples = append(samples, float32(int16(binary.LittleEndian.Uint16(buf[i:])))/32768)
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return samples, nil
			}
			return nil, err
		}
	}
}

// lastLine is ffmpeg's actual complaint. Its stderr opens with banner noise
// even at -loglevel error, and the final line is the one that names the
// problem.
func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}
