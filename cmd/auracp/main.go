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
		fmt.Println("auracp 0.1.0")
	case "sites":
		if err := listSites(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
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
  auracp sites          list sites (via the local daemon API)`)
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
