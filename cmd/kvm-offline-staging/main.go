// kvm-offline-staging works only with synthetic offline BSON snapshots. It has
// no database connection or activation path. See docs/offline-staging.md.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"kvm-center/internal/offlinestaging"
)

func main() {
	mode := flag.String("mode", "verify", "convert, verify, history, legacy-history, legacy-history-state, legacy-tag-state, or legacy-affinity-state")
	source := flag.String("source", "", "synthetic offline snapshot directory (convert only)")
	output := flag.String("bundle", "", "new staging directory, or existing bundle to verify/read")
	raw := flag.Bool("raw-history", false, "write original concatenated history BSON to stdout, instead of metadata")
	flag.Parse()
	if flag.NArg() != 0 || *output == "" || (*mode != "convert" && *source != "") || (*raw && *mode != "history") {
		fmt.Fprintln(os.Stderr, "invalid arguments")
		os.Exit(2)
	}
	var err error
	switch *mode {
	case "convert":
		var m *offlinestaging.Manifest
		m, err = offlinestaging.Convert(*source, *output)
		if err == nil {
			fmt.Println(offlinestaging.Summary(m))
		}
	case "verify":
		var m *offlinestaging.Manifest
		m, err = offlinestaging.Verify(*output)
		if err == nil {
			fmt.Println(offlinestaging.Summary(m))
		}
	case "legacy-affinity-state":
		state, exportErr := offlinestaging.ExportLegacyAffinityState(*output)
		err = exportErr
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(state)
		}
	case "legacy-tag-state":
		state, exportErr := offlinestaging.ExportLegacyTagState(*output)
		err = exportErr
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(state)
		}
	case "legacy-history-state":
		var state *offlinestaging.LegacyHistoryState
		state, err = offlinestaging.ExportLegacyHistoryState(*output)
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(state)
		}
	case "legacy-history":
		var views []offlinestaging.LegacyHistoryView
		views, err = offlinestaging.LegacyHistory(*output)
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(views)
		}
	case "history":
		var records []offlinestaging.HistoryRecord
		records, err = offlinestaging.History(*output)
		if err == nil {
			for _, r := range records {
				if *raw {
					_, err = os.Stdout.Write(r.Raw)
				} else {
					err = json.NewEncoder(os.Stdout).Encode(r)
				}
				if err != nil {
					break
				}
			}
		}
	default:
		err = fmt.Errorf("unknown mode")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "offline staging refused:", err)
		os.Exit(1)
	}
}
