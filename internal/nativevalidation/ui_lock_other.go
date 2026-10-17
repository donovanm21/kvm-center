//go:build !unix

package nativevalidation

import (
	"errors"
	"os"
)

func uiLock(string, bool) (*os.File, error) {
	return nil, errors.New("UI watchdog requires Unix file locking")
}
