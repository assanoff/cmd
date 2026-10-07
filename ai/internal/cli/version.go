// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package cli

import (
	"fmt"
	"runtime/debug"
)

// kronkModule is the SDK every model-backed subcommand runs on. It is named
// here rather than at each call site because doctor and -version both report
// it, and they must report the same thing.
const kronkModule = "github.com/ardanlabs/kronk"

// PrintVersion reports what this binary was built from. The Kronk line matters
// as much as the tool's own: the native libraries under ~/.kronk are shared
// with the Kronk command and the model server, and Kronk, its yzma binding and
// llama.cpp are only tested together.
func PrintVersion() {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Println("ai (devel)")
		return
	}
	fmt.Printf("ai    %s\n", bi.Main.Version)
	fmt.Printf("kronk %s\n", moduleVersion(bi, kronkModule))
	fmt.Printf("go    %s\n", bi.GoVersion)
}

// KronkVersion is the SDK version this binary was built against, or "" when
// the build info is unreadable.
func KronkVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return moduleVersion(bi, kronkModule)
}

// moduleVersion finds a dependency's version in the build info.
func moduleVersion(bi *debug.BuildInfo, path string) string {
	for _, dep := range bi.Deps {
		if dep.Path == path {
			if dep.Replace != nil {
				return dep.Replace.Version + " (replaced)"
			}
			return dep.Version
		}
	}
	return "unknown"
}
