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
- `dump_dir` (optional): Directory used by Proxmox to store dump archives for restore uploads (defaults to `/var/lib/vz/dump`). Backup streams do not write dump files to this directory.
- `node` (optional): Proxmox node to target for restore/upload operations (required if your cluster has multiple nodes)
- `cleanup` (optional): When `true`, delete the vzdump file from Proxmox storage after restore (defaults to `false`). Backup streams do not create a dump file to remove.

Restores always stop the target VM/CT first and use force-restore to overwrite an existing VM/CT with the same ID.

## Backup selection options

Backup selection is passed via the generic `-o` flag of `plakar backup` and is forwarded to the importer as key/value options.
You should set exactly one of the following:

- `vmid=<id>`: backup a single VM/CT
- `pool=<name>`: backup all VMs/CTs in a pool
- `all` or `all=true`: backup everything

## Examples

```bash
# Configure a Proxmox local source
$ plakar source add myProxmoxHypervisorLocal proxmox://10.0.0.10 mode=local

# Configure a Proxmox remote source (with password auth)
$ plakar source add myProxmoxHypervisorRemote proxmox://10.0.0.10 mode=remote conn_username=root conn_password=aSecureAndStrongPass conn_method=password

# Configure a Proxmox remote source (with identity auth)
$ plakar source add myProxmoxHypervisorRemote proxmox://10.0.0.10 mode=remote conn_username=root conn_identity_file=/path/to/somewhere/pmx_id conn_method=identity

# Backup VM / CT
$ plakar at /tmp/example backup -o vmid=101 @myProxmoxHypervisorSrc
$ plakar at /tmp/example backup -o pool=prod @myProxmoxHypervisorSrc
$ plakar at /tmp/example backup -o all @myProxmoxHypervisorSrc 
$ plakar at /tmp/example backup -o vmid=101 -o cleanup=true @myProxmoxHypervisorSrc 

# Configure a Proxmox local destination
$ plakar destination add myProxmoxHypervisorLocal proxmox://10.0.0.10 mode=local

# Configure a Proxmox remote destination (with password auth)
$ plakar destination add myProxmoxHypervisorRemote proxmox://10.0.0.10 mode=remote conn_username=root conn_password=aSecureAndStrongPass  conn_method=password

# Configure a Proxmox remote destination (with identity auth)
$ plakar destination add myProxmoxHypervisorRemote proxmox://10.0.0.10 mode=remote conn_username=root conn_identity_file=/path/to/something/pmx_id conn_method=identity

# Restore backup to destination
$ plakar at /tmp/example restore -to @myProxmoxHypervisorRemote <snapid>
``` 

## Proxmox tools / commands used

This integration relies on Proxmox CLI tooling (`pvesh`, `vzdump`, `qmrestore`, `pct`).

Commands are executed locally when `mode=local`, and via SSH when `mode=remote`.

Backup (importer) commands:
- `pvesh get /version --output-format json`
- `pvesh get /cluster/resources --type vm --output-format json` (when `all`)
- `pvesh get /pools/<pool> --output-format json` (when `pool=...`)
- `vzdump <vmid> --stdout --mode <snapshot|suspend|stop> --compress <0|1|lzo|gzip|zstd> [--node <node>]` (stream archive to Plakar)

Restore (exporter) commands:
- `cat > <dump_dir>/<archive>` (write archive to Proxmox storage)
- `qm stop <vmid>` (QEMU)
- `pct stop <vmid>` (LXC)
- `qmrestore <dump_dir>/<archive> <vmid> --force` (QEMU)
- `pct restore <vmid> <dump_dir>/<archive> --force` (LXC)
- `rm -f -- <dump_dir>/<archive>` (when `cleanup=true`)
