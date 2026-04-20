// kvmctl — minimal CLI for kvm-center, authenticated via API token.
// Config at $XDG_CONFIG_HOME/kvm-center/config.json (default
// ~/.config/kvm-center/config.json), created by `kvmctl login`.
// Output is human-readable; pass --json for machine output.
// Run `kvmctl --help` for the command list.
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cliVersion is injected at build time via -ldflags "-X main.cliVersion=...".
// Default ("dev") is what you get from `go build` without the release
// makefile/Dockerfile, which centralise the injection on server/VERSION.
var cliVersion = "dev"

type Config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "login":
		os.Exit(cmdLogin(args))
	case "version", "--version", "-v":
		fmt.Println("kvmctl", cliVersion)
	case "hosts":
		os.Exit(cmdHosts(args))
	case "vms":
		os.Exit(cmdVMs(args))
	case "vm":
		os.Exit(cmdVM(args))
	case "snapshot":
		os.Exit(cmdSnapshot(args))
	case "whoami":
		os.Exit(cmdWhoami(args))
	case "config":
		os.Exit(cmdConfig(args))
	case "logout":
		os.Exit(cmdLogout(args))
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", cmd)
		usage()
		os.Exit(1)
	}
}

// ANSI helpers. Colors are enabled when stderr is a terminal AND
// NO_COLOR isn't set; otherwise everything falls back to plain text so
// pipes/scripts stay clean.
var (
	colorEnabled = func() bool {
		if os.Getenv("NO_COLOR") != "" {
			return false
		}
		fi, err := os.Stderr.Stat()
		return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
	}()
	cReset  = ansi("\x1b[0m")
	cBold   = ansi("\x1b[1m")
	cDim    = ansi("\x1b[2m")
	cAccent = ansi("\x1b[38;5;208m") // orange — matches kvm-center brand
	cCmd    = ansi("\x1b[36m")       // cyan for command names
	cArg    = ansi("\x1b[33m")       // yellow for args
)

func ansi(s string) string {
	if colorEnabled {
		return s
	}
	return ""
}

func usage() {
	w := os.Stderr
	fmt.Fprintf(w, "\n  %skvmctl%s %s— kvm-center CLI%s  %s%s%s\n", cBold+cAccent, cReset, cBold, cReset, cDim, cliVersion, cReset)
	fmt.Fprintf(w, "  %sScriptable access to the kvm-center API.%s\n\n", cDim, cReset)

	section := func(title string) { fmt.Fprintf(w, "  %s%s%s\n", cBold, title, cReset) }
	row := func(cmd, desc string) {
		fmt.Fprintf(w, "    %s%-42s%s  %s%s%s\n", cCmd, cmd, cReset, cDim, desc, cReset)
	}

	section("AUTH")
	row("login --server <url> --token <token>", "save server + token to ~/.config/kvm-center")

	section("\n  INVENTORY")
	row("hosts [--json]", "list hypervisors with status, CPU%, mem%, VM count")
	row("vms   [--host <id>] [--json]", "list VMs, optionally scoped to one host")

	section("\n  VM LIFECYCLE")
	row("vm start     <uuid>", "power on")
	row("vm stop      <uuid>", "graceful shutdown")
	row("vm restart   <uuid>", "reboot")
	row("vm forcestop <uuid>", "power off (destroy domain)")
	row("vm migrate   <uuid> --to <host> [--mode live|offline]", "move to another host")

	section("\n  SNAPSHOTS")
	row("snapshot list   <uuid>", "list snapshots for a VM")
	row("snapshot create <uuid> [--name <name>]", "create; name auto-generated when omitted")

	section("\n  MISC")
	row("whoami", "show the user this token authenticates as")
	row("config", "show saved config path + server (token is masked)")
	row("logout", "remove the saved config")
	row("version", "print kvmctl version")
	row("help", "show this help")

	fmt.Fprintf(w, "\n  %sFlags:%s %s--json%s emits raw JSON · %sNO_COLOR=1%s disables colors\n", cBold, cReset, cArg, cReset, cArg, cReset)
	fmt.Fprintf(w, "\n  %sExamples%s\n", cBold, cReset)
	fmt.Fprintf(w, "    %skvmctl login%s --server %shttps://kvm.example.com:8190%s --token %skvmc_…%s\n", cCmd, cReset, cArg, cReset, cArg, cReset)
	fmt.Fprintf(w, "    %skvmctl hosts%s\n", cCmd, cReset)
	fmt.Fprintf(w, "    %skvmctl vm migrate%s %s<uuid>%s --to %shvnode02%s --mode %slive%s\n", cCmd, cReset, cArg, cReset, cArg, cReset, cArg, cReset)
	fmt.Fprintf(w, "    %skvmctl snapshot create%s %s<uuid>%s --name %spre-patch%s\n\n", cCmd, cReset, cArg, cReset, cArg, cReset)
}

// ── Commands ──────────────────────────────────────────────────────

func cmdLogin(args []string) int {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	server := fs.String("server", "", "https://kvm-center URL")
	token := fs.String("token", "", "API token (kvmc_…)")
	_ = fs.Parse(args)
	if *server == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "--server and --token required")
		return 1
	}
	cfg := Config{Server: strings.TrimRight(*server, "/"), Token: *token}
	// Verify by hitting /api/auth/me.
	if _, err := apiCall(cfg, "GET", "/api/auth/me", nil); err != nil {
		fmt.Fprintln(os.Stderr, "login failed:", err)
		return 1
	}
	if err := saveConfig(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "save config:", err)
		return 1
	}
	fmt.Println("logged in to", cfg.Server)
	return 0
}

func cmdHosts(args []string) int {
	fs := flag.NewFlagSet("hosts", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	body, err := apiCall(cfg, "GET", "/api/hosts", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		os.Stdout.Write(body)
		return 0
	}
	var hosts []struct {
		ID          string  `json:"id"`
		IP          string  `json:"ip"`
		Status      string  `json:"status"`
		CPUUsage    float64 `json:"cpu_usage"`
		MemUsage    float64 `json:"mem_usage"`
		VMCount     int     `json:"vm_count"`
		Maintenance bool    `json:"maintenance_mode"`
	}
	if err := json.Unmarshal(body, &hosts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("%-20s %-15s %-10s %5s %5s %5s\n", "ID", "IP", "STATUS", "CPU%", "MEM%", "VMS")
	for _, h := range hosts {
		status := h.Status
		if h.Maintenance {
			status += "/maint"
		}
		fmt.Printf("%-20s %-15s %-10s %5.0f %5.0f %5d\n", h.ID, h.IP, status, h.CPUUsage, h.MemUsage, h.VMCount)
	}
	return 0
}

func cmdVMs(args []string) int {
	fs := flag.NewFlagSet("vms", flag.ExitOnError)
	host := fs.String("host", "", "filter by host id")
	asJSON := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	path := "/api/vms"
	if *host != "" {
		path += "?host=" + *host
	}
	body, err := apiCall(cfg, "GET", path, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		os.Stdout.Write(body)
		return 0
	}
	var vms []struct {
		UUID   string `json:"uuid"`
		Name   string `json:"name"`
		HostID string `json:"host_id"`
		State  string `json:"state"`
		CPU    int    `json:"cpu_count"`
		MemMB  uint64 `json:"mem_total_mb"`
	}
	if err := json.Unmarshal(body, &vms); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("%-36s %-25s %-15s %-10s %4s %7s\n", "UUID", "NAME", "HOST", "STATE", "CPU", "MEMMB")
	for _, v := range vms {
		fmt.Printf("%-36s %-25s %-15s %-10s %4d %7d\n", v.UUID, v.Name, v.HostID, v.State, v.CPU, v.MemMB)
	}
	return 0
}

func cmdVM(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: kvmctl vm <action> <uuid> [...]")
		return 1
	}
	action, uuid, rest := args[0], args[1], args[2:]
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	host, name, err := resolveVM(cfg, uuid)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	body := map[string]any{"host_id": host, "vm_uuid": uuid, "vm_name": name}
	switch action {
	case "start":
		body["action"] = "START"
	case "stop":
		body["action"] = "STOP"
	case "restart":
		body["action"] = "RESTART"
	case "forcestop":
		body["action"] = "FORCESTOP"
	case "migrate":
		fs := flag.NewFlagSet("migrate", flag.ExitOnError)
		to := fs.String("to", "", "destination host id")
		mode := fs.String("mode", "live", "live|offline")
		_ = fs.Parse(rest)
		if *to == "" {
			fmt.Fprintln(os.Stderr, "--to required")
			return 1
		}
		destIP, err := lookupHostIP(cfg, *to)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if *mode == "offline" {
			body["action"] = "OFFLINE_MIGRATE"
		} else {
			body["action"] = "MIGRATE"
		}
		body["params"] = map[string]string{"destination": destIP}
	default:
		fmt.Fprintln(os.Stderr, "unknown vm action:", action)
		return 1
	}
	resp, err := apiCall(cfg, "POST", "/api/command", body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	os.Stdout.Write(resp)
	fmt.Println()
	return 0
}

func cmdSnapshot(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: kvmctl snapshot list|create <uuid> [...]")
		return 1
	}
	action, uuid, rest := args[0], args[1], args[2:]
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	host, name, err := resolveVM(cfg, uuid)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	switch action {
	case "list":
		body, err := apiCall(cfg, "GET", "/api/vms/snapshots?host="+host+"&vm="+name, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		os.Stdout.Write(body)
		fmt.Println()
		return 0
	case "create":
		fs := flag.NewFlagSet("create", flag.ExitOnError)
		snapName := fs.String("name", "", "snapshot name")
		_ = fs.Parse(rest)
		if *snapName == "" {
			*snapName = "kvmctl-" + time.Now().UTC().Format("20060102-150405")
		}
		body := map[string]any{
			"host_id": host, "vm_uuid": uuid, "vm_name": name,
			"action": "SNAPSHOT", "params": map[string]string{"name": *snapName},
		}
		resp, err := apiCall(cfg, "POST", "/api/command", body)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		os.Stdout.Write(resp)
		fmt.Println()
		return 0
	}
	fmt.Fprintln(os.Stderr, "unknown snapshot action:", action)
	return 1
}

func cmdWhoami(args []string) int {
	fs := flag.NewFlagSet("whoami", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	body, err := apiCall(cfg, "GET", "/api/auth/me", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		os.Stdout.Write(body)
		return 0
	}
	var me struct {
		User struct {
			ID       string   `json:"id"`
			Username string   `json:"username"`
			Email    string   `json:"email"`
			RoleIDs  []string `json:"role_ids"`
		} `json:"user"`
		Permissions []string `json:"permissions"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		os.Stdout.Write(body)
		return 0
	}
	fmt.Printf("user:        %s (%s)\n", me.User.Username, me.User.ID)
	if me.User.Email != "" {
		fmt.Printf("email:       %s\n", me.User.Email)
	}
	fmt.Printf("server:      %s\n", cfg.Server)
	if len(me.User.RoleIDs) > 0 {
		fmt.Printf("roles:       %s\n", strings.Join(me.User.RoleIDs, ", "))
	}
	if len(me.Permissions) > 0 {
		fmt.Printf("permissions: %s\n", strings.Join(me.Permissions, ", "))
	}
	return 0
}

func cmdConfig(args []string) int {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	showToken := fs.Bool("show-token", false, "print the full token (dangerous)")
	_ = fs.Parse(args)
	path := configPath()
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	tokDisplay := maskToken(cfg.Token)
	if *showToken {
		tokDisplay = cfg.Token
	}
	fmt.Printf("path:   %s\n", path)
	fmt.Printf("server: %s\n", cfg.Server)
	fmt.Printf("token:  %s\n", tokDisplay)
	return 0
}

func cmdLogout(_ []string) int {
	p := configPath()
	if _, err := os.Stat(p); os.IsNotExist(err) {
		fmt.Println("already logged out")
		return 0
	}
	if err := os.Remove(p); err != nil {
		fmt.Fprintln(os.Stderr, "remove config:", err)
		return 1
	}
	fmt.Println("logged out — removed", p)
	return 0
}

func maskToken(t string) string {
	if t == "" {
		return "(none)"
	}
	// kvmc_<8-id>.<48-secret> — show the public ID + a few chars of the secret.
	if len(t) < 16 {
		return "****"
	}
	dot := strings.IndexByte(t, '.')
	if dot <= 0 {
		return t[:6] + "…"
	}
	return t[:dot+1] + "****" + t[len(t)-4:]
}

// ── Helpers ──────────────────────────────────────────────────────

func resolveVM(cfg Config, uuid string) (host, name string, err error) {
	body, err := apiCall(cfg, "GET", "/api/vms", nil)
	if err != nil {
		return "", "", err
	}
	var vms []struct {
		UUID   string `json:"uuid"`
		Name   string `json:"name"`
		HostID string `json:"host_id"`
	}
	if err := json.Unmarshal(body, &vms); err != nil {
		return "", "", err
	}
	for _, v := range vms {
		if strings.EqualFold(v.UUID, uuid) {
			return v.HostID, v.Name, nil
		}
	}
	return "", "", fmt.Errorf("vm %s not found", uuid)
}

func lookupHostIP(cfg Config, id string) (string, error) {
	body, err := apiCall(cfg, "GET", "/api/hosts", nil)
	if err != nil {
		return "", err
	}
	var hosts []struct{ ID, IP string }
	if err := json.Unmarshal(body, &hosts); err != nil {
		return "", err
	}
	for _, h := range hosts {
		if h.ID == id {
			return h.IP, nil
		}
	}
	return "", fmt.Errorf("host %s not found", id)
}

func apiCall(cfg Config, method, path string, body any) ([]byte, error) {
	if cfg.Server == "" || cfg.Token == "" {
		return nil, fmt.Errorf("not logged in (run: kvmctl login --server <url> --token <token>)")
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, cfg.Server+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kvmctl/"+cliVersion)
	client := &http.Client{
		Timeout: 60 * time.Second,
		// Self-signed certs are the homelab default; trust on first use.
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s %s → %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func configPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "kvm-center", "config.json")
}

func loadConfig() (Config, error) {
	var c Config
	b, err := os.ReadFile(configPath())
	if err != nil {
		return c, fmt.Errorf("not logged in: %w", err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, nil
}

func saveConfig(c Config) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(p, b, 0600)
}
