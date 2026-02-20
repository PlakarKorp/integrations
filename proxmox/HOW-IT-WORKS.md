# Proxmox Integration - How It Works

## Prerequisites and Permissions

- Proxmox CLI tools must be available on the target node: `vzdump`, `qmrestore`, `pct`, and `pvesh`.
- The user running the integration must be allowed to execute those commands and to read/write in `dump_dir`.
- In remote mode, the SSH user must have the same permissions on the Proxmox node.

## Architecture Overview

- Importer (backup): uses `vzdump` to produce a dump, then sends it to Plakar (streamed in remote mode, file-based in local mode).
- Exporter (restore): recreates a temporary dump in `dump_dir`, correlates optional sidecar configs, then applies a state-aware restore workflow.
- `internal/proxmox`: shared layer (config, client, local/ssh runner, name parsing, etc.).
- Local/remote runner: same business logic, executed locally on the hypervisor or over SSH.

## Integration Repository Structure

- `importer/importer.go`: backup logic (selection, local/remote dump collection, record emission, sidecar VM config).
- `exporter/exporter.go`: restore logic (dump staging, sidecar matching, VM/CT state handling, restore, cleanup).
- `internal/proxmox/config.go`: config parsing and validation.
- `internal/proxmox/client.go`: Proxmox client (run, open/create/remove, ping).
- `internal/proxmox/runner.go`: shared local/ssh interface.
- `internal/proxmox/runner_local.go`: local command execution.
- `internal/proxmox/runner_ssh.go`: remote execution over SSH and IO via `cat`.
- `internal/proxmox/backup.go`: dump generation/streaming, compression detection, fallback logic.
- `internal/proxmox/resources.go`: VM/CT inventory via `pvesh`, short cache, node filtering.
- `internal/proxmox/dumpname.go`: `vzdump-*` name parsing for validation/restore.
- `plugin/importer/main.go`: SDK entrypoint for the importer.
- `plugin/exporter/main.go`: SDK entrypoint for the exporter.

## Technical Choices (Proxmox Constraints)

- **Proxmox CLI**: `vzdump`, `qmrestore`, `pct`, and `pvesh` are the supported, stable tools on Proxmox nodes, available locally and via SSH. (There is also a dedicated chapter on why there is no API version / usage)
- **Backup transport mode**: in `mode=remote`, `vzdump --stdout` avoids writing a dump on remote storage. In `mode=local`, `vzdump` writes to `dump_dir` and the importer reads the resulting file.
- **Restore requires a local file**: Proxmox has no "streaming restore". `qmrestore` and `pct restore` require a local file, so the exporter must write the dump into `dump_dir` before restoring.
- **Proxmox-compatible dump naming**: dump files follow the Proxmox naming scheme so `qmrestore` / `pct restore` can always detect archive type/compression.
- **Targeted restore from multi-VM backups**: Use `plakar restore <snapid>:<path>` to select a single dump file. No destination config mutation is required.
- **Runner abstraction**: isolates `local` vs `remote` to keep a single workflow. In remote mode, execution goes through SSH and IO uses `cat` (read/write) to avoid a scp/sftp dependency.
- **Node filtering**: a Proxmox cluster can have multiple nodes. The `node` option targets a specific node for inventory and `vzdump`.

## Backup Flow (Importer)

1. Read config and validate options (local/remote mode, SSH auth, compression, backup mode, node, etc.).
2. Resolve VM/CT selection: `vmid`, `pool`, or `all`.
3. Retrieve the list via `pvesh`:
   `pvesh get /cluster/resources --type vm` or `pvesh get /pools/<pool>`.
4. For each VM/CT, detect the type (`qemu` or `lxc`) via Proxmox inventory.
5. For each VM/CT:
   - in `mode=local`: run `vzdump` to generate a dump file in `dump_dir`, then read that file.
   - in `mode=remote`: run `vzdump --stdout`.
6. In remote mode, detect compression by reading the first bytes (gzip, zstd, lzo signatures) to generate a proper filename.
7. Send the dump to Plakar under `/backup/<type>/<vmid>/` using filename `vzdump-<type>-<vmid>-<timestamp>.<ext>[.gz|.zst|.lzo]`.
8. For QEMU and LXC, also export VM config files as sidecars:
   - QEMU: `/etc/pve/qemu-server/<vmid>.conf` as `/backup/qemu/<vmid>/<dump>_qemu.conf`
   - LXC: `/etc/pve/lxc/<vmid>.conf` as `/backup/lxc/<vmid>/<dump>_lxc.conf`
9. If VM/CT belongs to a pool, export pool membership as `/backup/<type>/<vmid>/<dump>_pool.conf` (content is the pool name).
10. `cleanup` option: if a dump was written to disk (local mode), it is removed.

## Restore Flow (Exporter)

1. Read snapshot files (dumps and optional sidecars).
2. Collect sidecar configs (`_qemu.conf`, `_lxc.conf`) and map them to their dump names.
3. For each dump file, parse the restore target from the filename (type + vmid), then write the dump into `dump_dir`.
4. Check target existence and runtime state using `qm/pct status`.
5. If VM/CT exists:
   - remember running/stopped state,
   - stop it,
   - restore dump,
   - restore the previous state.
6. If VM/CT does not exist:
   - restore dump directly,
   - optionally extract storage hint from matching sidecar config,
   - optionally apply pool via `--pool` when matching pool sidecar exists and pool still exists on target,
   - start VM/CT.
7. `cleanup` option: remove the temporary dump from `dump_dir`.

## Snapshot File Structure

Each backed-up VM/CT produces a dump object under `/backup/<type>/<vmid>/`:
- `/backup/<type>/<vmid>/vzdump-<type>-<vmid>-<timestamp>.<ext>[.gz|.zst|.lzo]`

For VM configs, sidecar files are also added:
- `/backup/<type>/<vmid>/vzdump-<type>-<vmid>-<timestamp>.<ext>[.gz|.zst|.lzo]_qemu.conf`
- `/backup/<type>/<vmid>/vzdump-<type>-<vmid>-<timestamp>.<ext>[.gz|.zst|.lzo]_lxc.conf`
- `/backup/<type>/<vmid>/vzdump-<type>-<vmid>-<timestamp>.<ext>[.gz|.zst|.lzo]_pool.conf`

## Snapshot Example

Example for a QEMU VM with `vmid=101` compressed with zstd:

```text
/backup/qemu/101/vzdump-qemu-101-2026_02_10-02_00_00.vma.zst
```

## Why We Use Canonical Proxmox Names

`qmrestore`/`pct restore` are strict about vzdump file naming. Using canonical names (`vzdump-qemu|lxc-<vmid>-<timestamp>...`) avoids archive detection failures during restore.

During restore, the exporter also stages files with a canonical Proxmox name in `dump_dir`, even when the snapshot entry came from an older custom naming scheme.

## Why We Do Not Use the Proxmox API

The Proxmox API does not provide the capabilities needed for this integration:
- It does not allow streaming backup data directly (no equivalent of `vzdump --stdout`) which would lead to data duplication during backup.
- It does not offer a reliable route to retrieve a dump file after it has been generated which would require ssh / file transfer in any case.

Using the CLI (`vzdump`, `qmrestore`, `pct`, `pvesh`) is the only practical way to stream backups and to control the full backup/restore workflow, both locally and over SSH.

## Remote Mode and SSH Notes

Remote mode exists to avoid installing extra binaries on the hypervisor and to centralize multiple Proxmox backups from a single "backup relay".

Security (TODO ?) note: the SSH implementation currently disables host key verification (`InsecureIgnoreHostKey`). This keeps setup simple but trades away strict host identity checks. If you require stricter security, add host key verification before using remote mode in production.

## Cleanup Behavior

- Backup in `mode=remote` is streamed and does not create a dump file on disk.
- Backup in `mode=local` writes a dump in `dump_dir`, and `cleanup=true` removes it after transfer.
- Restore always stages a dump into `dump_dir`. When `cleanup=true`, the staged file is deleted after restore (or after a failure).

## Misc.

Restore state handling is implemented in the exporter: existing VM/CT state is preserved, and newly recreated VM/CTs are started after restore.
