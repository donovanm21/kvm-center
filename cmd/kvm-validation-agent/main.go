// kvm-validation-agent is a standalone opt-in harness, never a production agent.
package main

import (
	"context"
	"flag"
	"kvm-center/internal/nativevalidation"
	"log"
	"os/signal"
	"syscall"
)

func main() {
	var c nativevalidation.Config
	var v nativevalidation.Virsh
	flag.BoolVar(&c.OptIn, "allow-disposable-libvirt", false, "explicitly allow only this manifest's disposable domain mutations")
	flag.BoolVar(&c.CleanupOnly, "cleanup", false, "cleanup saved owned domain only; never define or start")
	flag.StringVar(&c.Identity.UUID, "uuid", "", "explicit disposable UUIDv4")
	flag.StringVar(&c.Identity.Name, "name", "", "explicit kvmc-validation-* name")
	flag.StringVar(&c.Identity.Owner, "owner", "", "unique 64-character lowercase hex ownership marker")
	flag.StringVar(&c.StateDir, "state-dir", "", "fresh existing private directory; journal retained after cleanup")
	flag.StringVar(&c.SecretFile, "secret-file", "", "existing private synthetic control-plane signing secret file (raw bytes)")
	flag.StringVar(&c.Listen, "listen", "127.0.0.1:0", "separate loopback listener")
	flag.DurationVar(&c.Runtime, "runtime", nativevalidation.MaxRuntime, "total runtime including definition, max45s; cleanup max15s")
	flag.StringVar(&v.Binary, "virsh", "", "absolute existing virsh executable")
	flag.StringVar(&v.Socket, "libvirt-socket", "", "existing authorized local system libvirt Unix socket")
	flag.Parse()
	v.Home = c.StateDir
	if err := v.Validate(); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := nativevalidation.Run(ctx, c, v, func(addr string) { log.Printf("validation-only host=%s listen=%s", c.Identity.Name, addr) }); err != nil {
		log.Fatal(err)
	}
}
