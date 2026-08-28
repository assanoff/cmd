// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// durationSec asks ffprobe how long the file is. The last chapter has to end
// somewhere, and guessing from the file size is not good enough for that.
func durationSec(path string) (float64, error) {
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe %s: %w", filepath.Base(path), err)
	}
	sec, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || sec <= 0 {
		return 0, fmt.Errorf("cannot read the duration of %s", filepath.Base(path))
	}
	return sec, nil
}

// ffmetadata renders the chapter list in ffmpeg's own metadata format.
//
// Each chapter ends a millisecond before the next one starts and the last runs
// to the end of the file. That is the arithmetic players expect; an overlap
// makes some of them show the wrong chapter at a boundary.
//
// Only chapters go in here. Putting the file's own tags in as well would mean
// handing ffmpeg the whole metadata block, and everything not written out
// would be dropped from the result.
func ffmetadata(chs []chapter, totalMS int64) []byte {
	var b bytes.Buffer
	b.WriteString(";FFMETADATA1\n")

	for i, c := range chs {
		startMS := c.start * 1000
		endMS := totalMS
		if i+1 < len(chs) {
			endMS = chs[i+1].start*1000 - 1
		}
		if endMS < startMS {
			endMS = startMS
		}
		fmt.Fprintf(&b, "\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n",
			startMS, endMS, escapeMeta(c.title))
	}
	return b.Bytes()
}

// escapeMeta quotes the four characters ffmetadata treats as syntax.
func escapeMeta(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`=`, `\=`,
		`;`, `\;`,
		`#`, `\#`,
	).Replace(s)
}

// embed writes the chapters, and the cover when one is given, into a copy of
// the audio and puts it at dest. The audio itself is stream-copied: nothing is
// re-encoded, so the result is bit-identical sound.
//
// Everything the file already carries survives. That matters most on the
// second run: correcting a timestamp and running again should change the
// chapters and nothing else, not quietly cost the file its artist, its album
// and the cover from the first run.
func embed(src, dest, cover, title string, meta []byte) error {
	metaFile, err := os.CreateTemp("", "chapters-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(metaFile.Name())
	if _, err := metaFile.Write(meta); err != nil {
		metaFile.Close()
		return err
	}
	if err := metaFile.Close(); err != nil {
		return err
	}

	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-i", src, "-i", metaFile.Name()}
	if cover != "" {
		args = append(args, "-i", cover)
	}

	// Tags come from the audio and only the chapters from the metadata file.
	args = append(args, "-map", "0:a", "-map_metadata", "0", "-map_chapters", "1", "-c:a", "copy")

	// attached_pic is what makes a player treat the image as cover art rather
	// than as a one-frame video stream.
	if cover != "" {
		args = append(args, "-map", "2:v", "-c:v", "copy", "-disposition:v", "attached_pic",
			"-metadata:s:v", "title=Album cover", "-metadata:s:v", "comment=Cover (front)")
	} else {
		// Carry the existing cover across, if there is one. The trailing "?"
		// makes the mapping optional, so a file without art is not an error.
		args = append(args, "-map", "0:v?", "-c:v", "copy")
	}

	// The title is set only when asked for. Defaulting it to the file name
	// would overwrite a real title with something worse on every run.
	if title != "" {
		args = append(args, "-metadata", "title="+title)
	}
	if strings.EqualFold(filepath.Ext(src), ".mp3") {
		// The version Apple and most players read.
		args = append(args, "-id3v2_version", "3")
	}
	args = append(args, dest)

	cmd := exec.Command("ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return fmt.Errorf("ffmpeg: %w", err)
		}
		return fmt.Errorf("ffmpeg: %s", msg)
	}
	return nil
}

// writeInPlace runs embed through a temporary file beside the target, because
// ffmpeg cannot read and write the same path and a half-written file is worse
// than an unchanged one. The temporary lives in the same directory so the
// final step is a rename on one filesystem rather than a copy across two.
func writeInPlace(path, cover, title string, meta []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".chapters-*"+filepath.Ext(path))
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)

	if err := embed(path, tmpName, cover, title, meta); err != nil {
		return err
	}

	// Keep the mode the original had: a rename would otherwise leave the file
	// with the temporary's 0600.
	if info, err := os.Stat(path); err == nil {
		os.Chmod(tmpName, info.Mode().Perm())
	}
	return os.Rename(tmpName, path)
}
