#!/bin/sh
set -e

# The kvm-center server runs as the unprivileged "kvmc" user. When the
# container starts as root (the default), we first repair ownership of the
# data directory and then drop privileges to run the server itself.
#
# Why the chown: /var/lib/kvm-center is a mounted volume. On a FRESH install
# Docker initialises an empty named volume from the image (already kvmc-owned),
# so this is a no-op. On an UPGRADE from a pre-v5.61 image — which ran as root —
# the existing data.json / tls / ca files are root-owned, and an unprivileged
# server can't write them. The chown heals that automatically so operators
# don't have to fix volume permissions by hand.
if [ "$(id -u)" = "0" ]; then
	chown -R kvmc:kvmc /var/lib/kvm-center 2>/dev/null || true
	exec su-exec kvmc:kvmc "$@"
fi

# Already unprivileged (e.g. an explicit `user:` override in compose) — just run.
exec "$@"
