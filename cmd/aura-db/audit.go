package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin/standalone"
)

func runAudit(g globalFlags, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stdout, helpAudit)
		return nil
	}
	sub := args[0]
	switch sub {
	case "verify":
		return runAuditVerify(g, args[1:])
	case "tail":
		return runAuditTail(g, args[1:])
	case "--help", "-h", "help":
		fmt.Fprint(os.Stdout, helpAudit)
		return nil
	default:
		return userErr("unknown audit subcommand: " + sub)
	}
}

func runAuditVerify(g globalFlags, args []string) error {
	fs := newFlagSet("audit verify", os.Stderr)
	from := fs.Int("from", 0, "start at line N (1-based)")
	path := fs.String("path", "", "override audit log path")
	help := fs.Bool("help", false, "show help")
	if err := fs.Parse(args); err != nil {
		return userErr(err.Error())
	}
	if *help {
		fmt.Fprint(os.Stdout, helpAudit)
		return nil
	}

	logPath := *path
	if logPath == "" {
		cfg, err := standalone.LoadConfig(g.configPath)
		if err != nil {
			return err
		}
		logPath = cfg.Storage.AuditLogPath
	}
	res, err := standalone.VerifyAuditLogFrom(logPath, *from)
	if err != nil {
		return err
	}
	if res.OK {
		fmt.Fprintf(os.Stdout, "audit chain OK; events=%d heads=%d\n", res.EventsScanned, res.HeadsScanned)
		return nil
	}
	msg := fmt.Sprintf("chain break at line %d (event %s); expected prev=%s computed=%s",
		res.BreakLine, res.BreakEventID, res.ExpectedPrev, res.ComputedPrev)
	fmt.Fprintln(os.Stderr, msg)
	return &errAuditChainBroken{msg: msg}
}

func runAuditTail(g globalFlags, args []string) error {
	fs := newFlagSet("audit tail", os.Stderr)
	follow := fs.Bool("follow", false, "tail -f mode")
	since := fs.Duration("since", 0, "skip events older than now-duration")
	filterAction := fs.String("filter-action", "", "match Action substring")
	path := fs.String("path", "", "override audit log path")
	help := fs.Bool("help", false, "show help")
	if err := fs.Parse(args); err != nil {
		return userErr(err.Error())
	}
	if *help {
		fmt.Fprint(os.Stdout, helpAudit)
		return nil
	}
	logPath := *path
	if logPath == "" {
		cfg, err := standalone.LoadConfig(g.configPath)
		if err != nil {
			return err
		}
		logPath = cfg.Storage.AuditLogPath
	}

	f, err := os.Open(logPath)
	if err != nil {
		return err
	}
	defer f.Close()

	cutoff := time.Time{}
	if *since > 0 {
		cutoff = time.Now().Add(-*since)
	}

	if err := tailFile(f, *follow, cutoff, *filterAction, os.Stdout); err != nil {
		if !errors.Is(err, io.EOF) {
			return err
		}
	}
	return nil
}

func tailFile(f *os.File, follow bool, cutoff time.Time, filterAction string, out io.Writer) error {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for {
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var ev map[string]any
			if err := json.Unmarshal(line, &ev); err != nil {
				continue
			}
			if !cutoff.IsZero() {
				if ts, ok := ev["timestamp"].(string); ok {
					t, err := time.Parse(time.RFC3339Nano, ts)
					if err == nil && t.Before(cutoff) {
						continue
					}
				}
			}
			if filterAction != "" {
				if act, ok := ev["action"].(string); !ok || !strings.Contains(act, filterAction) {
					continue
				}
			}
			pretty, _ := json.MarshalIndent(ev, "", "  ")
			fmt.Fprintln(out, string(pretty))
		}
		if !follow {
			return sc.Err()
		}
		time.Sleep(500 * time.Millisecond)
	}
}
