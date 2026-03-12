# Proxmox Integration

## Overview

**Proxmox** (PVE) is a complete open-source platform for enterprise virtualization. With the built-in web interface you can easily manage VMs and containers, software-defined storage and networking, high-availability clustering, and multiple out-of-the-box tools using a single solution.

This integration allows:

* **Backup of virtual machines and containers:**
  Backup and store vzdump coming from Proxmox virtual machines and containers.

* **Virtual machines and containers restoration:**
  Restore previously backed-up virtual machines and containers dumps directly into a proxmox instance.

## Configuration

The configuration parameters are as follows:
- `mode` (required): Define how backup will be done, can be either `local` or `remote` : 
    - `local` : Plakar is installed directly on the proxmox instance
    - `remote`: Plakar is installed on a remote instance and need to connect in order to perform the backup
- `conn_method` (required if mode : `remote`): Set how user will connect to the remote server : 
    - `password` : Plakar will use standard ssh username / password combo to login
    - `identity` : Plakar will use a private key to connect with the set username
- `conn_username` (required if mode : `remote`): Proxmox user that will be used to connect and perform backup
- `conn_password` (required if conn_method : `password` ): Password that will be used to connect remotely and perform the backup
- `conn_identity_file` (required if conn_method : `identity` ): Identitfy key file path used to connect
- `backup_compression` (optional): Backup compression mode used by proxmox when dumping the VM / CT (defaults to `0`) :
    - `0` : No compression applied
    - `1` : Proxmox default compression
    - `lzo` : LZO compression applied 
    - `gzip` : GZIP compression applied
    - `zstd` : ZSTD compression applied 
- `backup_mode` (optional): Backup mode used, will impact how VM / CT behave during backup (defaults to `snapshot`) : 
    - `snapshot` : Use a snapshot mode without stopping or suspending VM / CT
    - `suspend` : VM or CT will be suspended during the backup
    - `stop` : Proxmox will stop the VM / CT in order to perform the backup
- `dump_dir` (optional): Directory used by Proxmox to store dump archives (defaults to `/var/lib/vz/dump`). It is used for restore uploads and for backup generation in both modes.
- `node` (optional): Proxmox node to target for restore/upload operations (required if your cluster has multiple nodes)
- `cleanup` (optional): When `true`, delete temporary vzdump files from Proxmox storage after restore and after backups (defaults to `true`).

## Restore behavior and options

During restore, the exporter checks whether the target VM/CT exists and its runtime state:

- **If it exists and is running**: restore is refused unless `-o force_vm_restore=true`, in which case the VM/CT is stopped before restore.
- **If it exists and is stopped**: restore is performed in place.
- **If it does not exist**: restore is performed from the dump. When a matching sidecar config file (`_qemu.conf` or `_lxc.conf`) is available, it may be used as a storage hint for restore. When a matching pool sidecar (`_pool.conf`) is available, the exporter checks that the pool still exists and then passes `--pool <pool>`.
- **After a successful restore**: the VM/CT is started when `-o start_on_restore=true`.
- **Storage / pool override**:
  - `-o storage=<name>` forces the storage target used by restore, overriding the sidecar hint.
  - `-o pool=<name>` forces the pool used by restore, overriding the sidecar hint.

Restore options are passed via the generic `-o` flag of `plakar restore`:

- `start_on_restore=true|false` (`false` by default): start restored VM/CT after success.
- `force_vm_restore=true|false` (`false` by default): if target VM/CT is running it is stopped; restore overwrites existing VM/CT when set.
- `storage=<name>`: force target storage for restore.
- `pool=<name>`: force target pool for restore.
- `newid=<id>`: restore under another VMID than the one contained in the source dump.

## Backup selection options

Backup selection is passed via the generic `-o` flag of `plakar backup` and is forwarded to the importer as key/value options.
You should set exactly one of the following:

- `vmid=<id>`: backup a single VM/CT
- `pool=<name>`: backup all VMs/CTs in a pool
- `all` or `all=true`: backup everything

## Backup File Structure

Each backed-up VM/CT produces a dump object under `/backup/<type>/<vmid>_<vmname>/`:
- `/backup/<type>/<vmid>_<vmname>/vzdump-<type>-<vmid>-<timestamp>.<ext>[.gz|.zst|.lzo]`

For VM configs, sidecar files are also added:
- `/backup/<type>/<vmid>_<vmname>/vzdump-<type>-<vmid>-<timestamp>.<ext>[.gz|.zst|.lzo]_qemu.conf`
- `/backup/<type>/<vmid>_<vmname>/vzdump-<type>-<vmid>-<timestamp>.<ext>[.gz|.zst|.lzo]_lxc.conf`
- `/backup/<type>/<vmid>_<vmname>/vzdump-<type>-<vmid>-<timestamp>.<ext>[.gz|.zst|.lzo]_pool.conf`

## Backup Example

Example for a QEMU VM with `vmid=101` named `myvm` compressed with zstd:

```text
/backup/qemu/101_myvm/vzdump-qemu-101-2026_02_10-02_00_00.vma.zst
```

## Examples

```bash
# Configure a Proxmox local source
$ plakar source add myProxmoxHypervisorLocal proxmox+backup://10.0.0.10 mode=local

# Configure a Proxmox remote source (with password auth)
$ plakar source add myProxmoxHypervisorRemote proxmox+backup://10.0.0.10 mode=remote conn_username=root conn_password=aSecureAndStrongPass conn_method=password

# Configure a Proxmox remote source (with identity auth)
$ plakar source add myProxmoxHypervisorRemote proxmox+backup://10.0.0.10 mode=remote conn_username=root conn_identity_file=/path/to/somewhere/pmx_id conn_method=identity

# Backup VM / CT
$ plakar at /tmp/example backup -o vmid=101 @myProxmoxHypervisorSrc
$ plakar at /tmp/example backup -o pool=prod @myProxmoxHypervisorSrc
$ plakar at /tmp/example backup -o all @myProxmoxHypervisorSrc 
$ plakar at /tmp/example backup -o vmid=101 -o cleanup=false @myProxmoxHypervisorSrc 

# Configure a Proxmox local destination
$ plakar destination add myProxmoxHypervisorLocal proxmox+backup://10.0.0.10 mode=local

# Configure a Proxmox remote destination (with password auth)
$ plakar destination add myProxmoxHypervisorRemote proxmox+backup://10.0.0.10 mode=remote conn_username=root conn_password=aSecureAndStrongPass  conn_method=password

# Configure a Proxmox remote destination (with identity auth)
$ plakar destination add myProxmoxHypervisorRemote proxmox+backup://10.0.0.10 mode=remote conn_username=root conn_identity_file=/path/to/something/pmx_id conn_method=identity

# Restore backup to destination
$ plakar at /tmp/example restore -to @myProxmoxHypervisorRemote <snapid>

# Restore one VM from a multi-VM snapshot by selecting its backup directory
$ plakar at /tmp/example restore -to @myProxmoxHypervisorRemote <snapid>:/backup/qemu/101_myvm
# Restore and restart after restore
$ plakar at /tmp/example restore -o start_on_restore=true -to @myProxmoxHypervisorRemote <snapid> 
# Restore existing VM by force (stop first if needed)
$ plakar at /tmp/example restore -o force_vm_restore=true -to @myProxmoxHypervisorRemote <snapid> 
# Restore to a different VMID and storage
$ plakar at /tmp/example restore -o newid=201 -o storage=local-lvm -o pool=sharedpool-to @myProxmoxHypervisorRemote <snapid> 
``` 

## Proxmox tools / commands used

This integration relies on Proxmox CLI tooling (`pvesh`, `vzdump`, `qmrestore`, `pct`).

Commands are executed locally when `mode=local`, and via SSH when `mode=remote`.

Backup (importer) commands:
- `pvesh get /version --output-format json`
- `pvesh get /cluster/resources --type vm --output-format json` (when `all`)
- `pvesh get /pools/<pool> --output-format json` (when `pool=...`)
- `vzdump <vmid> --dumpdir <dump_dir> --mode <snapshot|suspend|stop> --compress <0|1|lzo|gzip|zstd> [--node <node>]` (when `mode=local` and `mode=remote`)
- `cat -- /etc/pve/qemu-server/<vmid>.conf` (for QEMU sidecar config file)
- `cat -- /etc/pve/lxc/<vmid>.conf` (for LXC sidecar config file)

Restore (exporter) commands:
- `cat > <dump_dir>/<archive>` (write archive to Proxmox storage)
- `qm status <vmid>` / `pct status <vmid>` (check existence and running state)
- `pvesh get /pools/<pool> --output-format json` (only when a `_pool.conf` sidecar is present)
- `qmrestore <dump_dir>/<archive> <vmid> --force [--storage <storage>] [--pool <pool>]` (QEMU)
- `pct restore <vmid> <dump_dir>/<archive> --force [--storage <storage>] [--pool <pool>]` (LXC)
- `qm stop <vmid>` / `pct stop <vmid>` (when `-o force_vm_restore=true`)
- `qm start <vmid>` / `pct start <vmid>` (only when `-o start_on_restore=true`)
- `rm -f -- <dump_dir>/<archive>` (when `cleanup=true`)

## Technical / code overview 

### Backup Flow (Importer)

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

### Restore Flow (Exporter)

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

### Remote Mode and SSH Notes

Remote mode exists to avoid installing extra binaries on the hypervisor and to centralize multiple Proxmox backups from a single "backup relay".

Security (TODO ?) note: the SSH implementation currently disables host key verification (`InsecureIgnoreHostKey`). This keeps setup simple but trades away strict host identity checks. If you require stricter security, add host key verification before using remote mode in production.