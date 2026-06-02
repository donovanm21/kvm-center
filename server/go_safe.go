package main

import (
	"log"
	"runtime/debug"
)

// goSafe spawns a long-lived background goroutine that survives panics.
// A bare `go func()` that nil-derefs / OOBs / divides by zero takes the
// whole process down, which is overkill for a worker that can be
// restarted or dropped without operator-visible state loss. Wrap each
// such worker via goSafe so a panic logs cleanly and exits the
// goroutine instead.
//
// `name` is included in the log line so the operator (or a future
// audit) can identify which worker died without grepping a stack
// trace. `fn` is run synchronously inside the launched goroutine; once
// it returns (or panics) the goroutine exits.
//
// Callers responsible for "the worker MUST keep running" semantics
// should call goSafe inside an outer ticker loop, or arrange a
// re-spawn from a supervisor — goSafe deliberately does not loop on
// panic because the panic root cause is usually a bug worth
// investigating, not a transient that should be auto-retried.
func goSafe(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("worker: %q panicked: %v\n%s", name, r, debug.Stack())
			}
		}()
		fn()
	}()
}
