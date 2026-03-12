# Proxmox Integration - How It Works

## Prerequisites and Permissions

- Proxmox CLI tools must be available on the target node: `vzdump`, `qmrestore`, `pct`, and `pvesh`.
- The user running the integration must be allowed to execute those commands and to read/write in `dump_dir`.
- In remote mode, the SSH user must have the same permissions on the Proxmox node.

## Architecture Overview

- Importer (backup): uses `vzdump` to produce a dump file in `dump_dir`, then sends it to Plakar (local read in `mode=local`, SSH read in `mode=remote`).
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
- **Backup transport mode**: in both `mode=local` and `mode=remote`, `vzdump` writes to `dump_dir`. The importer then reads the dump file (remote reads happen over SSH).
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
5. For each VM/CT, run `vzdump` to generate a dump file in `dump_dir`.
6. Read the dump file and send it to Plakar under `/backup/<type>/<vmid>_<vmname>/` (VM name is sanitized for path safety).
7. For QEMU and LXC, also export VM config files as sidecars:
   - QEMU: `/etc/pve/qemu-server/<vmid>.conf` as `/backup/qemu/<vmid>_<vmname>/<dump>_qemu.conf`
   - LXC: `/etc/pve/lxc/<vmid>.conf` as `/backup/lxc/<vmid>_<vmname>/<dump>_lxc.conf`
8. If VM/CT belongs to a pool, export pool membership as `/backup/<type>/<vmid>_<vmname>/<dump>_pool.conf` (content is the pool name).
9. `cleanup` option: generated dump file is removed from `dump_dir` after transfer (enabled by default).

## Restore Flow (Exporter)

1. Read snapshot files (dumps and optional sidecars).
2. Collect sidecar configs (`_qemu.conf`, `_lxc.conf`) and map them to their dump names.
3. For each dump file, parse the restore target from the filename (type + vmid), then write the dump into `dump_dir`.
4. Check target existence and runtime state using `qm/pct status`.
5. If VM/CT exists:
   - if running: restore is refused unless `-o force_vm_restore=true`, in which case the VM/CT is stopped first.
   - if stopped: restore dump in place.
6. If VM/CT does not exist, restore dump directly.
7. Restore options from `plakar restore -o` are applied:
   - `start_on_restore=true|false` (`false` by default): start VM/CT after successful restore.
   - `force_vm_restore=true|false` (`false` by default): if VM/CT is running, stop it before restore; if VM/CT exists, it is restored in place (overwrite).
   - `storage=<name>`: force restore storage,
   - `pool=<name>`: force restore pool (validated on target),
   - `newid=<id>`: restore to another VMID.
8. Storage/pool precedence:
   - user-specified `storage` and `pool` override sidecar-derived hints when present.
   - if target VMID does not exist and no override is set, storage and pool are read from matching sidecars when available.
9. `cleanup` option: remove the temporary dump from `dump_dir`.

## Snapshot File Structure

Each backed-up VM/CT produces a dump object under `/backup/<type>/<vmid>_<vmname>/`:
- `/backup/<type>/<vmid>_<vmname>/vzdump-<type>-<vmid>-<timestamp>.<ext>[.gz|.zst|.lzo]`

For VM configs, sidecar files are also added:
- `/backup/<type>/<vmid>_<vmname>/vzdump-<type>-<vmid>-<timestamp>.<ext>[.gz|.zst|.lzo]_qemu.conf`
- `/backup/<type>/<vmid>_<vmname>/vzdump-<type>-<vmid>-<timestamp>.<ext>[.gz|.zst|.lzo]_lxc.conf`
- `/backup/<type>/<vmid>_<vmname>/vzdump-<type>-<vmid>-<timestamp>.<ext>[.gz|.zst|.lzo]_pool.conf`

## Snapshot Example

Example for a QEMU VM with `vmid=101` named `myvm` compressed with zstd:

```text
/backup/qemu/101_myvm/vzdump-qemu-101-2026_02_10-02_00_00.vma.zst
```

## Why We Use Canonical Proxmox Names

`qmrestore`/`pct restore` are strict about vzdump file naming. Using canonical names (`vzdump-qemu|lxc-<vmid>-<timestamp>...`) avoids archive detection failures during restore.

During restore, the exporter also stages files with a canonical Proxmox name in `dump_dir`, even when the snapshot entry came from an older custom naming scheme.

## Remote Mode and SSH Notes

Remote mode exists to avoid installing extra binaries on the hypervisor and to centralize multiple Proxmox backups from a single "backup relay".

Security (TODO ?) note: the SSH implementation currently disables host key verification (`InsecureIgnoreHostKey`). This keeps setup simple but trades away strict host identity checks. If you require stricter security, add host key verification before using remote mode in production.

## Cleanup Behavior

- Backup in both modes writes a dump in `dump_dir` before transfer.
- `cleanup` defaults to `true` and removes generated dumps from `dump_dir` after backup transfer.
- Restore always stages a dump into `dump_dir`. When `cleanup=true`, the staged file is deleted after restore (or after a failure).

## Misc.

Restore state handling is implemented in the exporter: running VM/CTs are rejected by default, running VM/CTs can be stopped and restored (overwriting) when `-o force_vm_restore=true`, and post-restore start is controlled by `-o start_on_restore=true`.
