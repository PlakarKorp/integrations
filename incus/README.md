# Plakar Incus Integration

Backup and restore Incus containers as browsable file-level snapshots.

## Importer

Import filesystem contents from Incus containers for backup:

```bash
plakar backup incus://web-1
```

## Exporter

Restore backups to new or existing Incus containers:

```bash
plakar restore -to incus://web-1-restored <snap>
```

## Notes

This integration backs up containers only and runs on the Incus host using the local unix socket.

## Validated

2026-07-08, on the target infrastructure (plakar v1.1.3, Incus cluster, LINSTOR pool, Debian containers):

- `plakar backup incus://<instance>` — 353 MiB container imported file-by-file in 18s; snapshot tree browsable down to individual rootfs files.
- Deduplication — second backup of the same instance: **+0 MB** on-disk repository growth (3.2 MiB written), 2x faster (9s).
- `plakar restore -to incus://<new-name> <snap>` — instance recreated natively by Incus in 19s, boots to `systemd running`; spot-check md5 diff of /etc/passwd, /etc/fstab, /usr/bin/env, /etc/os-release against the source: identical.

Known v1 caveats: hardlinks round-trip as true hardlinks incus-to-incus, but are materialized as relative symlinks on non-Incus restore destinations (true hardlink identity is not representable in the connector protocol); extended attributes (e.g. file capabilities) are not preserved through restore.
