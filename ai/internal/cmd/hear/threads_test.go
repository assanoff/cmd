// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package hear

import (
	"testing"

	"github.com/assanoff/cmd/ai/internal/cli"
	"github.com/assanoff/cmd/ai/internal/config"
)

// The environment is the config's override, so these hold whatever the
// deployed config file says.

func TestThreadsFlagWinsOverConfig(t *testing.T) {
	t.Setenv("AI_ASR_THREADS", "3")
	c := &Command{Threads: 5}
	if got := c.threads(cli.NewPrinter(Name, true)); got != 5 {
		t.Errorf("threads = %d, want 5 from --threads", got)
	}
}

func TestThreadsFromConfig(t *testing.T) {
	t.Setenv("AI_ASR_THREADS", "3")
	c := &Command{}
	if got := c.threads(cli.NewPrinter(Name, true)); got != 3 {
		t.Errorf("threads = %d, want 3 from AI_ASR_THREADS", got)
	}
}

func TestThreadsBadConfigFallsBack(t *testing.T) {
	t.Setenv("AI_ASR_THREADS", "all")
	c := &Command{}
	want := config.DefaultASRThreads()
	if got := c.threads(cli.NewPrinter(Name, true)); got != want {
		t.Errorf("threads = %d, want the default %d", got, want)
	}
}
