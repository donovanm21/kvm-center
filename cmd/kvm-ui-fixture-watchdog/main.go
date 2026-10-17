// Dedicated bounded UI fixture observer. Never starts a VM, serves a listener,
// creates credentials, or configures the production agent.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"kvm-center/internal/nativevalidation"
)

func main() {
	mode := flag.String("mode", "", "run, arm, finish, recover")
	path := flag.String("manifest", "", "private reviewed runtime manifest")
	manifestHash := flag.String("manifest-sha256", "", "reviewed manifest hash")
	binaryHash := flag.String("binary-sha256", "", "reviewed helper hash")
	allow := flag.Bool("allow-disposable-libvirt", false, "permit only exact owned fixture mutations")
	flag.Parse()
	if flag.NArg() != 0 || !*allow {
		log.Fatal("explicit bounded fixture opt-in required")
	}
	m, err := nativevalidation.ReadUIManifest(*path, *manifestHash)
	if err != nil {
		log.Fatal(err)
	}
	if err = nativevalidation.VerifyUIEnvironment(m, *binaryHash); err != nil {
		log.Fatal(err)
	}
	switch *mode {
	case "arm", "finish":
		err = nativevalidation.UISignal(m.Config, *mode)
	case "run", "recover":
		v := nativevalidation.Virsh{Binary: "/usr/bin/virsh", Socket: "/run/libvirt/libvirt-sock", Home: m.Config.StateDir}
		if err = v.Validate(); err != nil {
			log.Fatal(err)
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		err = nativevalidation.RunUIWatchdog(ctx, m.Config, v, *mode == "recover", func(j nativevalidation.UIJournal) {
			if e := json.NewEncoder(os.Stdout).Encode(j); e != nil {
				log.Printf("readiness output failed; rely on committed journal: %v", e)
			}
		})
	default:
		log.Fatal("unknown mode")
	}
	if err != nil {
		log.Fatal(err)
	}
}
