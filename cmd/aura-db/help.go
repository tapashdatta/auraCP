package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// globalFlags carries the values common to every subcommand.
type globalFlags struct {
	configPath string
	logLevel   string
	logFormat  string
	help       bool
}

// parseGlobalFlags peels off recognized global flags before the
// subcommand. Returns the parsed globals and the remaining argv.
//
// Recognized: --config, --log-level, --log-format, --help, -h
func parseGlobalFlags(args []string) (globalFlags, []string) {
	g := globalFlags{
		configPath: envOrDefault("AURA_DB_CONFIG", "/etc/aura-db/config.yaml"),
	}
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--help" || a == "-h":
			g.help = true
			i++
		case a == "--config":
			if i+1 >= len(args) {
				return g, args[i:]
			}
			g.configPath = args[i+1]
			i += 2
		case startsWith(a, "--config="):
			g.configPath = a[len("--config="):]
			i++
		case a == "--log-level":
			if i+1 >= len(args) {
				return g, args[i:]
			}
			g.logLevel = args[i+1]
			i += 2
		case startsWith(a, "--log-level="):
			g.logLevel = a[len("--log-level="):]
			i++
		case a == "--log-format":
			if i+1 >= len(args) {
				return g, args[i:]
			}
			g.logFormat = args[i+1]
			i += 2
		case startsWith(a, "--log-format="):
			g.logFormat = a[len("--log-format="):]
			i++
		default:
			return g, args[i:]
		}
	}
	return g, args[i:]
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func envOrDefault(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// newFlagSet builds a per-subcommand FlagSet wired to write usage into w.
func newFlagSet(name string, w io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(w)
	return fs
}

// printGlobalHelp prints the top-level help.
func printGlobalHelp(w io.Writer) {
	fmt.Fprint(w, `aura-db — standalone Aura DB server

Usage:
  aura-db [global flags] <subcommand> [subcommand flags]

Subcommands:
  serve            run the HTTP API server (default)
  user-create      provision a user (interactive password + optional TOTP enroll)
  user-passwd      change a user's password (admin or self)
  kek-init         create a fresh KEK on disk (first-run bootstrap)
  kek-rotate       rotate the Key Encryption Key, re-encrypting all secrets
  audit verify     verify the audit log hash chain
  audit tail       follow the audit log (jq-friendly output)
  version          print version, build commit, build date
  help             show help for a subcommand

Global flags:
  --config <path>          config file (default $AURA_DB_CONFIG or /etc/aura-db/config.yaml)
  --log-level <level>      override config logging.level
  --log-format <fmt>       override config logging.format
  --help                   show this help

Environment:
  AURA_DB_CONFIG           overrides --config default
  AURA_DB_KEK              base64-encoded KEK (overrides KEK file)
  AURA_DB_KEK_FILE         path to KEK file (default /etc/aura-db/kek.key)
`)
}

// printSubcommandHelp dispatches to the subcommand's --help printer.
func printSubcommandHelp(w io.Writer, sub string) {
	switch sub {
	case "serve":
		fmt.Fprint(w, helpServe)
	case "user-create":
		fmt.Fprint(w, helpUserCreate)
	case "user-passwd":
		fmt.Fprint(w, helpUserPasswd)
	case "kek-rotate":
		fmt.Fprint(w, helpKEKRotate)
	case "kek-init":
		fmt.Fprint(w, helpKEKInit)
	case "audit":
		fmt.Fprint(w, helpAudit)
	case "version":
		fmt.Fprint(w, helpVersion)
	default:
		fmt.Fprintf(w, "aura-db: no help available for %q\n", sub)
	}
}

const (
	helpServe = `aura-db serve — run the HTTP API server

Loads config, opens stores, listens, drains in-flight requests on
SIGTERM/SIGINT.

Flags:
  --listen <addr>     override config.listen
  --tls-cert <path>   override config.tls.cert_file
  --tls-key  <path>   override config.tls.key_file
  --dry-run           validate config + open DBs read-only + print routes, then exit 0
  --help              show this help
`
	helpUserCreate = `aura-db user-create — provision a user

Prompts for password (twice) unless --password-stdin. Optionally enrolls
TOTP and prints recovery codes.

Flags:
  --username <name>           required
  --role <role>               optional initial role (viewer|analyst|writer|dba|owner)
  --grant <conn-name>:<role>  may repeat
  --grant-all-owner           grant RoleOwner on every existing connection
  --allow-pwned               skip HIBP check (requires --reason)
  --reason <text>             required when --allow-pwned is set
  --enroll-totp               generate TOTP secret + print provisioning URI
  --password-stdin            read password from stdin
  --print-recovery-codes      print 8 codes to stdout (default true)
  --help                      show this help
`
	helpUserPasswd = `aura-db user-passwd — change a user's password

Without --self, requires the panel-admin role on the executing OS uid
(root or aurabd group).

Flags:
  --username <name>           required
  --self                      changes own password (requires current password)
  --password-stdin            new password from stdin
  --allow-pwned               skip HIBP check
  --reason <text>             required when --allow-pwned is set
  --help                      show this help
`
	helpKEKRotate = `aura-db kek-rotate — rotate the KEK

Refuses to run while the serve PID file exists unless --force. Re-encrypts
every connection credential and MFA secret in a single transaction, swaps
the KEK file atomically, zeros the old key in memory.

Flags:
  --new-key-file <path>   path to write the new KEK (default /etc/aura-db/kek.key)
  --generate              generate a fresh KEK (mutually exclusive with --new-key-from)
  --new-key-from <path>   read new key from path (32 bytes raw)
  --backup-old-to <path>  write the old key to path (required; key destruction is irreversible)
  --force                 bypass the PID-file lock check
  --help                  show this help
`
	helpAudit = `aura-db audit — audit log utilities

Subcommands:
  verify [--from line] [--path path]    walk the chain, report first break
  tail   [--follow] [--since dur]       follow events (JSON pretty-print)

Flags (verify):
  --path <path>    override config.storage.audit_log_path
  --from <line>    start at line number N (default 1)

Flags (tail):
  --path <path>           override config.storage.audit_log_path
  --follow                tail -f mode
  --since <duration>      skip events older than now-duration
  --filter-action <act>   match Action substring
`
	helpVersion = `aura-db version — print version metadata

Prints: aura-db <semver> commit <gitsha> built <iso8601> go<goversion>
`
)

// errUserInput is returned for user-facing input errors (bad flags,
// missing input). exit code 2.
type errUserInput struct{ msg string }

func (e *errUserInput) Error() string { return e.msg }
func userErr(msg string) error        { return &errUserInput{msg: msg} }

// errKEKRotateRefused is returned when the PID lock blocks rotation.
type errKEKRotateRefused struct{ msg string }

func (e *errKEKRotateRefused) Error() string { return e.msg }

// errAuditChainBroken is returned when verify finds a break.
type errAuditChainBroken struct{ msg string }

func (e *errAuditChainBroken) Error() string { return e.msg }

// exitCodeFor maps an error to its CLI exit code per the design.
func exitCodeFor(err error) int {
	var ui *errUserInput
	if errors.As(err, &ui) {
		return 2
	}
	var kr *errKEKRotateRefused
	if errors.As(err, &kr) {
		return 3
	}
	var ab *errAuditChainBroken
	if errors.As(err, &ab) {
		return 4
	}
	return 1
}
