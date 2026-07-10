# Plakar Incus Integration

Backup and restore Incus containers as browsable file-level snapshots.

**Containers only.** Virtual machines are refused at backup time with a
clear error: a VM export is a single monolithic disk image, so per-file
browsing and deduplication — the point of this integration — do not
apply. Use `incus export` for VMs.

## Requirements

- **An Incus daemon to talk to.** By default the plugin connects to the
  local daemon over its unix socket (`/var/lib/incus/unix.socket`);
  alternatively it can reach a remote daemon over HTTPS with a client
  certificate — see [Remote servers](#remote-servers-https--client-certificate).
- **Socket access** (local mode). The user running `plakar` must be able
  to read and write the socket — in practice, membership in the
  `incus-admin` group (or root):

  ```bash
  sudo usermod -aG incus-admin "$USER"
  # log out/in (or `newgrp incus-admin`) for the group to take effect
  ```

  The importer creates and deletes server-side backups, which is why
  `incus-admin` is needed rather than the more limited `incus` group.
- A working [plakar](https://plakar.io) installation with a repository
  (`plakar create` / `plakar server`, see plakar's own docs).

## Installation

```bash
git clone https://github.com/PlakarKorp/integration-incus
cd integration-incus
make install       # builds both connectors, packages them, runs `plakar pkg add`
```

`make install` versions the package from `git describe`; use
`make pkg VERSION=v1.2.3` to override. To remove: `plakar pkg rm incus`.

## Usage end to end

Back up a container named `web-1`:

```bash
plakar backup incus://web-1
```

Every file of the container's root filesystem becomes an individually
browsable, deduplicated entry in the repository:

```bash
plakar ls                          # list snapshots; note the snapshot id
plakar ls <snap>:/etc              # browse the captured rootfs
plakar cat <snap>:/etc/os-release  # read a single file straight from the backup
```

Restore to a **new** instance name (the exporter refuses to overwrite an
existing instance — restore to another name or delete it first):

```bash
plakar restore -to incus://web-1-restored <snap>
incus start web-1-restored
```

The instance is recreated natively by Incus (config, profiles and
device entries included), on the storage pool recorded in the backup
unless overridden with `-o pool=...`.

### Options

Passed with `-o key=value` on the `plakar backup` / `plakar restore`
command line.

Both connectors accept:

| Option | Default | Description |
|---|---|---|
| `socket` | `/var/lib/incus/unix.socket` | Path to the Incus unix socket (mutually exclusive with `url`) |
| `url` | — | Remote Incus server, `https://host:8443`; requires `tls_client_cert` + `tls_client_key` (mutually exclusive with `socket`) |
| `tls_client_cert` | — | Path to the PEM client certificate trusted by the remote server (remote only) |
| `tls_client_key` | — | Path to the PEM client key matching `tls_client_cert` (remote only) |
| `tls_server_cert` | system CA | Path to the remote server's PEM certificate, to pin a self-signed server (remote only) |
| `tls_ca` | — | Path to the PEM CA certificate, for servers running in PKI mode (remote only) |
| `project` | `default` | Incus project to operate in; required for any instance living outside the `default` project |

When `project` is set, the snapshot origin is qualified as
`project/instance` so same-named instances from different projects stay
distinguishable in listings; without it the origin is the bare instance
name.

The importer also accepts (Go durations, e.g. `45m`, `12h`):

| Option | Default | Description |
|---|---|---|
| `backup_ttl` | `6h` | Expiry of the temporary server-side backup. A safety net: if the plugin dies without cleaning up, Incus expires the backup on its own. Raise it if a backup of a very large instance could outlive 6 hours. |
| `cleanup_timeout` | `2m` | Maximum wait for the server-side backup deletion after the export. |

The exporter also accepts:

| Option | Default | Description |
|---|---|---|
| `pool` | pool referenced by the backup | Target storage pool for the restored instance |
| `target` | scheduler placement | Cluster member to create the restored instance on (clusters only) |

Examples:

```bash
# Instance in a non-default project, over a custom socket path
plakar backup -o project=customer-a -o socket=/run/incus/unix.socket incus://web-1

# Big instance on a slow server: give the temporary backup more headroom
plakar backup -o backup_ttl=24h -o cleanup_timeout=10m incus://data-warehouse

# Restore into another project, onto a faster pool
plakar restore -to incus://web-1-restored -o project=customer-a -o pool=fast <snap>
```

### Remote servers (HTTPS + client certificate)

The plugin can back up and restore instances of a **remote** Incus
server, e.g. from a dedicated backup machine, using Incus' native TLS
client-certificate authentication.

One-time setup — expose the server and trust a client certificate:

```bash
# On the Incus server
incus config set core.https_address :8443

# On the backup machine: generate a client certificate…
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:secp384r1 \
  -sha384 -keyout plakar-key.pem -out plakar-cert.pem \
  -nodes -subj "/CN=plakar-backup" -days 3650

# …and trust it on the server (or use `incus config trust add` tokens)
incus config trust add-certificate plakar-cert.pem

# Pin the server's certificate on the backup machine (self-signed by default)
openssl s_client -connect incus.example:8443 </dev/null 2>/dev/null \
  | openssl x509 > incus-server.pem
```

Then point the connectors at the server:

```bash
plakar backup \
  -o url=https://incus.example:8443 \
  -o tls_client_cert=/etc/plakar/plakar-cert.pem \
  -o tls_client_key=/etc/plakar/plakar-key.pem \
  -o tls_server_cert=/etc/plakar/incus-server.pem \
  incus://web-1
```

Notes:

- `tls_server_cert` pins the server's (usually self-signed) certificate.
  Omit it only if the server's certificate is signed by a CA the system
  trusts. For servers running in PKI mode, pass the CA with `tls_ca`.
- The trusted client certificate has full API access; there is no
  insecure "skip verification" option, by design.
- Remote mode replaces the `incus-admin` group requirement — permissions
  are the certificate trust, granted server-side.

## What is NOT backed up

A backup covers the instance's **root filesystem only** (the export is
created with `InstanceOnly: true`). Explicitly excluded:

- **Incus snapshots** of the instance — they are not part of the export.
  A restored instance starts with an empty snapshot list.
- **Custom storage volumes and host mounts** attached to the instance —
  any `disk`-type device other than the root disk (e.g.
  `incus storage volume attach ...` or a `source=/host/path` mount).
  The device *entry* survives in the instance config, but the volume's
  **content is not in the backup**; back those volumes up separately.
  At backup time the importer inspects the instance's devices and
  prints a `WARNING` on stderr for every such device so exclusions are
  visible, not silent.
- Anything outside the instance: profiles' definitions, networks,
  images, other project-level objects.

A restore to Incus recreates the instance config as exported, including
device entries pointing at custom volumes — if those volumes no longer
exist on the target, Incus will refuse to start (or create) the
instance until they are recreated or the device is removed.

## Server-side disk usage

`plakar backup` asks Incus for a temporary export
(`CreateInstanceBackup`): the server **materializes the complete,
uncompressed tarball on its own disk before the plugin can stream
it**. During a backup the Incus host therefore temporarily holds a
second copy of the instance's root filesystem, under the server's
backups path — `/var/lib/incus/backups` by default, or the custom
volume named by the server config key `storage.backups_volume`. The
copy is deleted as soon as the stream completes, and expires after
`backup_ttl` (default 6h) even if the plugin dies first.

Make sure that location has at least the instance's rootfs size free.
If a backup fails with `no space left on device`, free up space there
or move backup staging to a roomier pool:

```bash
incus storage volume create <pool> backups-staging
incus config set storage.backups_volume <pool>/backups-staging
```

## Caveats

- **Incus snapshots and attached volumes are excluded** — see
  [What is NOT backed up](#what-is-not-backed-up) above.
- **Extended attributes / file capabilities: captured, not yet
  restored.** The importer records every tar entry's PAX
  `SCHILY.xattr.*` attributes (including `security.capability`, what
  `getcap` reads) as kloset xattr records, and the exporter is ready to
  reinject them into the rebuilt tar. However the current kloset
  `Snapshot.Export()` does not hand xattr records to exporters, so
  **restored files lose their xattrs and file capabilities** (e.g.
  `ping`'s `cap_net_raw`) until that is fixed upstream — reapply with
  `setcap` after restore where needed. The plugin side is done and will
  light up with the kloset fix, tracked in this repo's TODO.
- **Hardlink identity.** Hardlinks round-trip as true hardlinks
  incus-to-incus. Restoring to a non-Incus destination materializes
  them as relative symlinks instead (true hardlink identity is not
  representable in the connector protocol). Inode numbers themselves
  are never preserved — only link relationships are.
- **Restore never overwrites.** Restoring to an instance name that
  already exists fails fast (before the tarball is uploaded) with
  `instance "X" already exists`; restore to another name or delete the
  instance first.
- **Backup of a running instance is not point-in-time.** The export
  reflects the rootfs while the container keeps running; stop the
  instance (or accept the drift) for a quiescent capture.

## Tested versions

- Built against the official Incus Go client `lxc/incus/v6 v6.23.0`
  (Incus 6.x API); older Incus servers missing the instance-type field
  are treated as container-only.
- Validated end to end with plakar v1.1.3 against an Incus 6.x cluster
  (LINSTOR storage pool, Debian containers) — see below.
- An automated live round-trip test exists under `e2e/`
  (`make test-integration`): real Alpine container → backup → restore
  under another name → boot + setuid/uid-gid/hardlink/symlink/fifo/
  getcap assertions + full rootfs manifest diff. It skips cleanly when
  no local Incus daemon is reachable.

## Validated

2026-07-08, on the target infrastructure (plakar v1.1.3, Incus cluster, LINSTOR pool, Debian containers):

- `plakar backup incus://<instance>` — 353 MiB container imported file-by-file in 18s; snapshot tree browsable down to individual rootfs files.
- Deduplication — second backup of the same instance: **+0 MB** on-disk repository growth (3.2 MiB written), 2x faster (9s).
- `plakar restore -to incus://<new-name> <snap>` — instance recreated natively by Incus in 19s, boots to `systemd running`; spot-check md5 diff of /etc/passwd, /etc/fstab, /usr/bin/env, /etc/os-release against the source: identical.
