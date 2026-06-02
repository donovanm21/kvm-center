package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
)

// Signed self-update (C2). The agent self-update path used to download and
// execute an unsigned binary. The server now publishes a signed manifest
// next to each agent binary in the downloads dir; the agent verifies the
// Ed25519 signature (from the grant key it already trusts) before swapping.
//
// We sign at startup so each server build signs exactly the binaries it
// ships. Best-effort: if signing a binary fails the manifest is absent and
// the agent's self-update fails closed, which is the safe outcome.

// signAgentBinaries publishes <binary>.sig manifests for the agent binaries
// found in dir, signed with the grant signer.
func signAgentBinaries(dir string, signer *GrantSigner) {
	if signer == nil || dir == "" {
		return
	}
	for _, arch := range []string{"amd64", "arm64"} {
		bin := filepath.Join(dir, "kvm-center-agent-"+arch)
		digest, err := sha256File(bin)
		if err != nil {
			// arch not shipped in this image, or unreadable — skip quietly.
			continue
		}
		manifest := struct {
			SHA256 string `json:"sha256"`
			Sig    string `json:"sig"`
			Alg    string `json:"alg"`
			KeyID  string `json:"keyid"`
		}{
			SHA256: hex.EncodeToString(digest),
			Sig:    base64.StdEncoding.EncodeToString(signer.SignBlob(digest)),
			Alg:    "ed25519",
			KeyID:  signer.PublicKeyHex()[:16],
		}
		out, _ := json.Marshal(manifest)
		if err := os.WriteFile(bin+".sig", out, 0o644); err != nil {
			log.Printf("agent-sign: write %s.sig: %v", filepath.Base(bin), err)
			continue
		}
		log.Printf("agent-sign: published %s.sig (sha256 %s…, key %s)", filepath.Base(bin), manifest.SHA256[:12], manifest.KeyID)
	}
}

func sha256File(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}
