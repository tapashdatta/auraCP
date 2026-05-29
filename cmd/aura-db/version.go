package main

import (
	"fmt"
	"runtime"
)

// Build metadata. Overridden by -ldflags at link time.
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func runVersion() {
	fmt.Printf("aura-db %s commit %s built %s go%s\n", version, commit, buildDate, runtime.Version())
}
