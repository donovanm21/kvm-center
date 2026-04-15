---
name: Bug report
about: Something isn't working the way the docs say it should
title: ''
labels: bug
---

## What happened

<!-- A clear, concise description of the problem. -->

## What you expected

<!-- What should have happened instead. -->

## Steps to reproduce

1.
2.
3.

## Environment

- **kvm-center version:** <!-- `/api/version` or the tag you built from -->
- **Deployment:** <!-- docker compose / bare metal / other -->
- **Host OS:** <!-- `cat /etc/os-release | head -2` -->
- **Docker version:** <!-- `docker --version` + `docker compose version` -->
- **Hypervisor OS:** <!-- RHEL 9 / Rocky 9 / Ubuntu 22.04 / etc. -->
- **libvirt version:** <!-- `virsh --version` on the hypervisor -->

## Logs / screenshots

<!--
Paste relevant log lines. For the server:
  docker compose logs kvm-center-server --tail 200

Redact anything sensitive.
-->

```
```

## Additional context

<!-- Anything else that seems relevant — recent config changes, related issues, workarounds tried. -->
