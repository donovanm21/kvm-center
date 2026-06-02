//go:build !linux

package main

import "os/exec"

// configureProcessAttr is a no-op on non-Linux. The agent only
// runs on Linux hypervisors in production; this stub keeps the
// macOS dev build green.
func configureProcessAttr(_ *exec.Cmd) {}
