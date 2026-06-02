# Verifying kvm-center release artifacts

The Docker Hub image ships signed:

| Artifact | Format | Signature | Public key |
|---|---|---|---|
| Docker Hub image (`thedon21/kvm-center:vX.Y.Z`) | OCI manifest | [Sigstore cosign](https://docs.sigstore.dev/cosign/overview/) | `cosign.pub` |

This page is the canonical reference for verifying it before
running it on your fleet.

## Verifying a Docker Hub image (cosign)

You'll need [cosign](https://docs.sigstore.dev/cosign/installation/)
installed (Homebrew: `brew install cosign`).

```sh
# Pull the image:
docker pull thedon21/kvm-center:v5.49.0

# Verify the signature against the committed public key.
# Substitute the version-tagged image — `:latest` works too if
# you trust whatever the rolling tag points at.
cosign verify \
  --key https://raw.githubusercontent.com/donovanm21/kvm-center/main/docs/security/cosign.pub \
  thedon21/kvm-center:v5.49.0
```

A successful verify prints `Verification for thedon21/kvm-center:v5.49.0 -- The following checks were performed: …` and exits 0. **Any non-zero exit means the image was not signed by the kvm-center maintainers — do not run it.**

You can also pin to the local public key in this repo if you've
cloned it:

```sh
cosign verify --key docs/security/cosign.pub thedon21/kvm-center:v5.49.0
```

## Key fingerprints

For out-of-band verification (e.g. confirming the public key in
this repo wasn't substituted in a malicious PR):

**cosign:** the public key in `docs/security/cosign.pub` is an
ECDSA P-256 key. Hash:

```sh
sha256sum docs/security/cosign.pub
```

## Key rotation

If the cosign private key is compromised:

1. Generate a replacement keypair: `cosign generate-key-pair`.
2. Replace `docs/security/cosign.pub` and rotate the
   `COSIGN_KEY` secret in Gitea Actions.
3. Open a PR with a release-notes entry naming the superseded
   fingerprint so downstream operators know to re-pin.
4. The next release signs with the new key automatically.
