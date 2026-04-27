//go:build !windows

package main

// spawnRestartHelper is a no-op on non-Windows builds. swapBinaryWindows
// only invokes it when GOOS == "windows" at runtime, but the symbol must
// still resolve for the cross-platform build.
func spawnRestartHelper(string) error { return nil }
