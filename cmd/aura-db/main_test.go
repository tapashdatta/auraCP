package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseGlobalFlags(t *testing.T) {
	g, rest := parseGlobalFlags([]string{"--config", "/tmp/x.yaml", "--log-level=debug", "serve", "--listen", ":8080"})
	if g.configPath != "/tmp/x.yaml" {
		t.Fatalf("config: %q", g.configPath)
	}
	if g.logLevel != "debug" {
		t.Fatalf("level: %q", g.logLevel)
	}
	if rest[0] != "serve" {
		t.Fatalf("rest: %v", rest)
	}
}

func TestPrintGlobalHelp(t *testing.T) {
	var buf bytes.Buffer
	printGlobalHelp(&buf)
	want := []string{"aura-db", "serve", "user-create", "kek-rotate", "audit verify"}
	for _, s := range want {
		if !strings.Contains(buf.String(), s) {
			t.Fatalf("help text missing %q", s)
		}
	}
}

func TestPrintSubcommandHelp_Known(t *testing.T) {
	for _, sub := range []string{"serve", "user-create", "user-passwd", "kek-rotate", "kek-init", "audit", "version"} {
		var buf bytes.Buffer
		printSubcommandHelp(&buf, sub)
		if buf.Len() == 0 {
			t.Fatalf("subcommand help empty for %q", sub)
		}
	}
}

// TestKEKInitCommand_FreshFile exercises the runKEKInit handler end-to-end
// (no config file required because --output bypasses the config load).
func TestKEKInitCommand_FreshFile(t *testing.T) {
	dir := t.TempDir()
	out := dir + "/kek.key"
	if err := runKEKInit(globalFlags{configPath: "/dev/null"}, []string{"--output", out}); err != nil {
		t.Fatalf("runKEKInit: %v", err)
	}
	// Second invocation MUST refuse to overwrite — first-run safety.
	if err := runKEKInit(globalFlags{configPath: "/dev/null"}, []string{"--output", out}); err == nil {
		t.Fatal("expected refusal to overwrite existing KEK file")
	}
}
