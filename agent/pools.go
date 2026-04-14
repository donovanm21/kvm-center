package main

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
)

// Pool discovery (v5.11.7). Surfaces the active libvirt storage
// pools + their capacity so the migrate modal can let operators pick
// a destination datastore. Read-only.

type poolInfo struct {
	Name         string  `json:"name"`
	Path         string  `json:"path"`
	Type         string  `json:"type"`
	State        string  `json:"state"`
	CapacityGB   float64 `json:"capacity_gb"`
	AvailableGB  float64 `json:"available_gb"`
	Autostart    bool    `json:"autostart"`
}

func handlePools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	// Name-only enumeration sidesteps the tabular output's column-
	// alignment pitfalls across libvirt versions. One pool-info +
	// pool-dumpxml pair per pool gets us the rest structured.
	names, err := exec.Command("virsh", "pool-list", "--all", "--name").Output()
	if err != nil {
		http.Error(w, "virsh pool-list: "+err.Error(), http.StatusInternalServerError)
		return
	}
	pools := []poolInfo{}
	for _, line := range strings.Split(string(names), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		p := poolInfo{Name: name}
		if infoOut, err := exec.Command("virsh", "pool-info", name).Output(); err == nil {
			for _, ln := range strings.Split(string(infoOut), "\n") {
				k, v, ok := splitKV(ln)
				if !ok {
					continue
				}
				switch k {
				case "State":
					p.State = v
				case "Autostart":
					p.Autostart = strings.HasPrefix(v, "yes")
				case "Capacity":
					p.CapacityGB = parsePoolSizeStr(v)
				case "Available":
					p.AvailableGB = parsePoolSizeStr(v)
				}
			}
		}
		if xmlOut, err := exec.Command("virsh", "pool-dumpxml", name).Output(); err == nil {
			var px struct {
				XMLName xml.Name `xml:"pool"`
				Type    string   `xml:"type,attr"`
				Target  struct {
					Path string `xml:"path"`
				} `xml:"target"`
			}
			if err := xml.Unmarshal(xmlOut, &px); err == nil {
				p.Path = px.Target.Path
				p.Type = px.Type
			}
		}
		pools = append(pools, p)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(pools)
}

func splitKV(line string) (key, val string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:]), true
}

// parsePoolSizeStr converts "12.34 GiB" / "456 MiB" into GB.
func parsePoolSizeStr(s string) float64 {
	f := strings.Fields(s)
	if len(f) < 2 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	switch f[1] {
	case "TiB":
		return v * 1024
	case "GiB":
		return v
	case "MiB":
		return v / 1024
	case "KiB":
		return v / (1024 * 1024)
	}
	return v
}
