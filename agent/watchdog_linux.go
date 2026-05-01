//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

// configureProcessAttr (Linux): set Pdeathsig=SIGKILL so a child
// process dies with the agent if the agent crashes before the
// watchdog can run. Belt-and-braces for the boot reconcile path.
// New process group too, so SIGKILL to the leader takes the whole
// tree (e.g. virsh forking qemu-img).
func configureProcessAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	cmd.SysProcAttr.Setpgid = true
}
