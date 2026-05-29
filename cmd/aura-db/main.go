// Command aura-db is the standalone Aura DB server: a self-contained
// HTTP API for managing MariaDB / Postgres connections with built-in
// auth, audit, and key management. See docs/aura-db/STANDALONE-DEPLOY.md.
package main

import (
	"fmt"
	"os"
)

func main() {
	args := os.Args[1:]
	// Pull out global flags first; everything before the subcommand.
	globals, rest := parseGlobalFlags(args)

	if globals.help && (len(rest) == 0 || rest[0] == "help") {
		printGlobalHelp(os.Stdout)
		return
	}

	subcommand := "serve"
	if len(rest) > 0 {
		subcommand = rest[0]
		rest = rest[1:]
	}

	var err error
	switch subcommand {
	case "serve":
		err = runServe(globals, rest)
	case "user-create":
		err = runUserCreate(globals, rest)
	case "user-passwd":
		err = runUserPasswd(globals, rest)
	case "kek-rotate":
		err = runKEKRotate(globals, rest)
	case "kek-init":
		err = runKEKInit(globals, rest)
	case "kek":
		// Allow `aura-db kek <init|rotate>` two-word form.
		sub := ""
		if len(rest) > 0 {
			sub = rest[0]
			rest = rest[1:]
		}
		switch sub {
		case "init":
			err = runKEKInit(globals, rest)
		case "rotate":
			err = runKEKRotate(globals, rest)
		default:
			fmt.Fprintf(os.Stderr, "aura-db: unknown kek subcommand %q (want init|rotate)\n", sub)
			os.Exit(2)
		}
	case "audit":
		err = runAudit(globals, rest)
	case "version":
		runVersion()
	case "help":
		if len(rest) > 0 {
			printSubcommandHelp(os.Stdout, rest[0])
		} else {
			printGlobalHelp(os.Stdout)
		}
	default:
		fmt.Fprintf(os.Stderr, "aura-db: unknown subcommand %q\n", subcommand)
		printGlobalHelp(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "aura-db: %v\n", err)
		os.Exit(exitCodeFor(err))
	}
}
