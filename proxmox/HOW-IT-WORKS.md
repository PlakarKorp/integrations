# Proxmox Integration - How It Works

## Prerequisites and Permissions

- Proxmox CLI tools must be available on the target node: `vzdump`, `qmrestore`, `pct`, and `pvesh`.
- The user running the integration must be allowed to execute those commands and to read/write in `dump_dir`.
- In remote mode, the SSH user must have the same permissions on the Proxmox node.

## Architecture Overview

- Importer (backup): uses `vzdump` to produce a dump and stream it to Plakar.
- Exporter (restore): recreates a temporary dump in `dump_dir`, then calls `qmrestore` or `pct restore`.
- `internal/proxmox`: shared layer (config, client, local/ssh runner, name parsing, etc.).
- Local/remote runner: same business logic, executed locally on the hypervisor or over SSH.

## Integration Repository Structure

- `importer/importer.go`: backup logic (selection, `vzdump` streaming, record emission, dump naming).
- `exporter/exporter.go`: restore logic (dump staging, VM/CT stop, restore, cleanup).
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
- **Streaming backup**: `vzdump --stdout` avoids writing a temporary file on Proxmox storage. This limits disk usage.
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
5. For each VM/CT, run `vzdump --stdout`.
6. For each VM/CT, detect compression by reading the first bytes (gzip, zstd, lzo signatures) to generate a proper filename.
7. For each VM/CT, stream the dump to Plakar as `vzdump-<type>-<vmid>-<timestamp>.<ext>[.gz|.zst|.lzo]`.
8. `cleanup` option: if a dump was written to disk (non-stream case), it is removed.

## Restore Flow (Exporter)

1. Read snapshot files (dumps).
2. For each dump file, parse the restore target from the filename (type + vmid).
3. Write the dump into `dump_dir` (local or over SSH).
4. Stop the VM/CT if needed (`qm stop` or `pct stop`). "Not running" errors are ignored.
5. Restore by type: QEMU with `qmrestore <dump> <vmid> --force`, LXC with `pct restore <vmid> <dump> --force`.
6. `cleanup` option: remove the temporary dump from `dump_dir`.

## Snapshot File Structure

Each backed-up VM/CT produces a single object at the snapshot root:
- `/vzdump-<type>-<vmid>-<timestamp>.<ext>[.gz|.zst|.lzo]`

## Snapshot Example

Example for a QEMU VM with `vmid=101` compressed with zstd:

```text
/vzdump-qemu-101-2026_02_10-02_00_00.vma.zst
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

- Backup streaming does not create a dump file on disk, so `cleanup` has no effect during backups.
- Restore always stages a dump into `dump_dir`. When `cleanup=true`, the staged file is deleted after restore (or after a failure).

## Misc. 

Dump restore need to stop the VM, which is managed in the exporter, however the restart of the VM is not implemented. Do plaker need to manage that ? 
