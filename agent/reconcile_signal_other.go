//go:build !unix

package main

import "os"

// syscallSignalZero on non-unix targets falls back to os.Interrupt.
// The agent only runs on Linux in production; this stub keeps a
// hypothetical Windows build green.
func syscallSignalZero() os.Signal { return os.Interrupt }
