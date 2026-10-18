//go:build !unix

package nativevalidation

import (
	"errors"
	"os"
)

var powerScopeBase, powerEndpoint string

func powerScope(Virsh, Identity) (string, error) {
	return "", errors.New("native power scope requires Unix")
}
func powerLock(string) (*os.File, error) { return nil, errors.New("native power lock requires Unix") }
