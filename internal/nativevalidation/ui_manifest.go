package nativevalidation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

type UIManifest struct {
	Version     int
	Hostname    string
	AgentSHA256 string
	Config      UIWatchConfig
}

func ReadUIManifest(path, digest string) (UIManifest, error) {
	var m UIManifest
	if !ownerPattern.MatchString(digest) {
		return m, errors.New("exact manifest SHA256 required")
	}
	b, e := privateUIFile(path)
	if e != nil {
		return m, e
	}
	if hash(string(b)) != digest {
		return m, errors.New("manifest hash mismatch")
	}
	if e = strictUIJSON(b, &m); e != nil {
		return m, e
	}
	if m.Version != 1 || m.Hostname == "" || !ownerPattern.MatchString(m.AgentSHA256) {
		return m, errors.New("manifest fields invalid")
	}
	_, _, e = uiConfig(m.Config)
	return m, e
}

// VerifyUIEnvironment performs read-only identity/hash checks. The lab must also
// verify shared baseline and deployed task semantics before staging/execution.
func VerifyUIEnvironment(m UIManifest, binaryHash string) error {
	host, e := os.Hostname()
	if e != nil || host != m.Hostname {
		return errors.New("host identity mismatch")
	}
	executable, e := os.Executable()
	if e != nil {
		return e
	}
	if e = verifyUIHash(executable, binaryHash); e != nil {
		return e
	}
	return verifyUIHash("/opt/kvm-center/kvm-center-agent", m.AgentSHA256)
}
func verifyUIHash(path, want string) error {
	if !ownerPattern.MatchString(want) {
		return errors.New("exact binary SHA256 required")
	}
	st, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() || st.Size() > 128<<20 {
		return errors.New("nonregular/oversized binary")
	}
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	actual, e := f.Stat()
	if e != nil || !os.SameFile(st, actual) {
		return errors.New("binary changed")
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(f, 128<<20+1))
	if e != nil || n > 128<<20 {
		return errors.New("binary read failed")
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return errors.New("binary hash mismatch")
	}
	return nil
}
