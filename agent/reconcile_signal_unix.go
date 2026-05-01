//go:build unix

package main

import "syscall"

// syscallSignalZero returns the POSIX "is alive" probe signal.
func syscallSignalZero() syscall.Signal { return syscall.Signal(0) }
