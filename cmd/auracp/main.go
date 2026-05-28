// Command auracp is the CLI client for auraCP (parity target for clpctl).
// For now it's a thin client over the daemon's JSON API; subcommands grow
// alongside the API.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

const defaultAPI = "http://localhost:8443"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println("auracp 0.2.48")
	case "sites":
		if err := listSites(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "doctor":
		// v0.2.48: fleet drift check. Walks /etc/nginx/sites-enabled +
		// /etc/php/*/fpm/pool.d, asserts the vhost↔pool single-source-
		// of-truth property per site. Exit 0 on green, 1 on drift, 2
		// on hard error (missing /etc/nginx — panel not installed).
		err := runDoctor()
		if err == errDoctorDrift {
			os.Exit(1)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(2)
		}
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `auracp — auraCP control panel CLI

usage:
  auracp version        print version
  auracp sites          list sites (via the local daemon API)
  auracp doctor [-v] [--json]
                        audit every site for vhost↔pool drift (no daemon needed)
                        -v     show details for green sites too
                        --json machine-readable output for cron / monitoring`)
}

func listSites() error {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(defaultAPI + "/api/sites")
	if err != nil {
		return fmt.Errorf("is auracpd running? %w", err)
	}
	defer resp.Body.Close()

	var sites []struct {
		Domain     string `json:"domain"`
		User       string `json:"user"`
		App        string `json:"app"`
		StatusText string `json:"statusText"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sites); err != nil {
		return err
	}
	fmt.Printf("%-26s %-14s %-16s %s\n", "DOMAIN", "USER", "APP", "STATUS")
	for _, s := range sites {
		fmt.Printf("%-26s %-14s %-16s %s\n", s.Domain, s.User, s.App, s.StatusText)
	}
	return nil
}
