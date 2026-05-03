# Verifying kvm-center release artifacts

Two artifact families ship signed:

| Artifact | Format | Signature | Public key |
|---|---|---|---|
| Docker Hub image (`thedon21/kvm-center:vX.Y.Z`) | OCI manifest | [Sigstore cosign](https://docs.sigstore.dev/cosign/overview/) | `cosign.pub` |
| KVM appliance qcow2 (GitHub Releases) | `SHA256SUMS` + `SHA256SUMS.asc` | OpenPGP detached signature | `maintainer.pub.asc` |

This page is the canonical reference for verifying either before
running it on your fleet.

## Verifying a Docker Hub image (cosign)

You'll need [cosign](https://docs.sigstore.dev/cosign/installation/)
installed (Homebrew: `brew install cosign`).

```sh
# Pull the image (requires `docker login` while the repo is private):
docker pull thedon21/kvm-center:v5.49.0

# Verify the signature against the committed public key.
# Substitute the version-tagged image — `:latest` works too if
# you trust whatever the rolling tag points at.
cosign verify \
  --key https://raw.githubusercontent.com/donovanm21/kvmcenter/main/docs/security/cosign.pub \
  thedon21/kvm-center:v5.49.0
```

A successful verify prints `Verification for thedon21/kvm-center:v5.49.0 -- The following checks were performed: …` and exits 0. **Any non-zero exit means the image was not signed by the kvm-center maintainers — do not run it.**

You can also pin to the local public key in this repo if you've
cloned it:

```sh
cosign verify --key docs/security/cosign.pub thedon21/kvm-center:v5.49.0
```

## Verifying a KVM appliance qcow2 (GPG)

The appliance build attaches three artifacts to each GitHub Release:

- `kvm-center-appliance-vX.Y.Z.qcow2` — the appliance disk image
- `SHA256SUMS` — checksum of the qcow2
- `SHA256SUMS.asc` — detached GPG signature over `SHA256SUMS`

```sh
# Download all three from the Release page (or via gh):
gh release download v5.49.1 \
  --repo donovanm21/kvmcenter \
  --pattern 'kvm-center-appliance-*.qcow2' \
  --pattern 'SHA256SUMS*'

# Import the maintainer's public key (one-time):
curl -fsSL https://raw.githubusercontent.com/donovanm21/kvmcenter/main/docs/security/maintainer.pub.asc \
  | gpg --import

# Verify the signature was made by the maintainer:
gpg --verify SHA256SUMS.asc SHA256SUMS
# expect: "Good signature from \"kvm-center release signing <releases@kvm-center.example.lan>\""

# Verify the qcow2 matches the signed checksum:
sha256sum -c SHA256SUMS
# expect: "kvm-center-appliance-v5.49.1.qcow2: OK"
```

Once both checks pass the qcow2 is safe to import:

```sh
sudo cp kvm-center-appliance-v5.49.1.qcow2 /var/lib/libvirt/images/
sudo virt-install --name kvm-center --import \
  --memory 2048 --vcpus 2 --os-variant rocky9 \
  --disk path=/var/lib/libvirt/images/kvm-center-appliance-v5.49.1.qcow2,bus=virtio \
  --network bridge=virbr0 --noautoconsole
sudo virsh console kvm-center   # see auto-generated admin password
```

## Key fingerprints

For out-of-band verification (e.g. confirming the public key in
this repo wasn't substituted in a malicious PR):

- **cosign:** the public key in `docs/security/cosign.pub` is an
  ECDSA P-256 key. Hash:

  ```sh
  sha256sum docs/security/cosign.pub
  ```

- **GPG:** the public key in `docs/security/maintainer.pub.asc`
  is an ed25519 (signing) + cv25519 (encryption) keypair.
  Fingerprint:

  ```sh
  gpg --show-keys docs/security/maintainer.pub.asc
  ```

  Compare the printed fingerprint against
  `715449307DDD01F930B7AF799C057A8711799FE5`.

## Key rotation

If a private key is compromised:

1. Generate replacement keypair (`cosign generate-key-pair`,
   `gpg --gen-key`).
2. Replace the public key in `docs/security/` and the
   corresponding Gitea Actions secret (`COSIGN_KEY` /
   `APPLIANCE_SIGNING_KEY`).
3. Open a PR with a release-notes entry naming the
   superseded fingerprint so downstream operators know to
   re-import.
4. The next release signs with the new key automatically.
