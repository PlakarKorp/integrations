# XCP Integration plan

## Terminology

- **XCP-ng**: the hypervisor (Xen + management stack). Equivalent to ESXi in VMware
- **Pool**: a group of XCP-ng hosts sharing storage, managed as one unit. Equivalent to a vSphere cluster.
- **Pool master**: the host in a pool that is the owner the API (XAPI) for the whole pool. 
  - We connect to this pool master; one connection covers all hosts and VMs in the pool.
- **XAPI**: the native management API on every XCP-ng host (XML-RPC / JSON-RPC over HTTPS). We use it to connect directly to each *pool master*.
  - inventory, snapshot, metadata, cleanup, restore config are done through XAPI calls on the pool master.
- **SR (Storage Repository)**: where disks live. Equivalent to a VMware datastore, vSAN.
- **VDI (Virtual Disk Image)**: a virtual disk (.vmdk in vmware), cow chain snapshotting where on each snapshot a delta file is created, formats can be vdh, raw (see docs notes on snapshotting) and qcow on newer versions.
- **VBD (Virtual Block Device)**: the link attaching a VDI to a VM, determines device position, bootable (os-disk) flag.
- **Xen Orchestra (XO / XOA)**: the management and orchestration layer for XCP-ng (web UI, multi-pool view, users, backups). XOA is the packaged appliance; XO can also be built from sources.
  - *Purely a client of XAPI*; it doesn't change XCP-ng, and its users are not XCP-ng users.
  - Less interesting for integration (optional, not always deployed), but useful for testing and as a reference.
  - Has its own backup engine: full and incremental (delta/CBT) backups, replication. Overlaps with what we're building.

## Features MVP

### Inventory
- `xcp://`
  - **List VMs active on XCP**: 
    - List all VMs: Call `VM.get_all_records` 
      - filter out templates, snapshots and dom0 (`is_a_template`, `is_a_snapshot`, `is_control_domain` all false).
    - Include VM tags via `VM.tags(vmid)` in order to receive tags to add to an inventory resource
    - Capture network IPv4: XAPI exposes a `guest_metrics` per VM that will expose reachable IPs
  - **Implement Ping**:
    - Call `session.login_with_password(username, password, ...)` on the pool master. If valid, we follow up with `pool.get_all_records` to confirm the session has sufficient privileges and the pool is responsive. 
    - Call `session.logout` when done


### Importer
- Implement `Import()`
  - **Create Snapshot**: Call `VM.snapshot(vm, name)`, which snapshots every attached disk (VDI) at once.
      - **Check snapshot viability**: 
        - Check whether VM have all fields set to false: `is_a_template`, `is_a_snapshot` and `is_control_domain`
        - Call `VM.get_allowed_operations`, check whether operation `snapshot` is included
        - Optional: Check if all disks are in format raw and fail: There is nothing to back-up
      - **Properly name snapshots**: `Plakar-Backup-<timestamp>`, can be used for future cleanup on unexpected, unresolved snapshot chains (due to e.g. crashes)
  - **Walk Each Disk**: Walk the snapshot’s (VBDs), keep those with type=Disk (skip CD), and take their VDI refs.
      - **Download over XCP http**: Disk transfer will be `raw` for first version
      `GET https://<host>/export_raw_vdi?session_id=…&vdi=<uuid>&format=raw`
      - **Record Disk metadata**: 
        -Read `uuid`, `virtual_size`, `physical_utilisation` and type from `VDI.get_record`. Also read userdevice and bootable from the VBD so you can reattach the disks in the right order.  
        - **Record VBD metadata**:  `type`, `mode` (RW/RO), `unpluggable`, and `other_config` for full device reconstruction.
        - **VDI location/SR reference**: record the VDI's SR UUID and `location` field to know where this disk contents is originating from (last delta in this snapshot chain)
        - **Record VDI disk provisioning**: store whether VDI is thin-provisioned (SR-level) and the `sm_config` (storage manager configuration) if relevant.
        - **Record disk format**: Store information what file format the SR backing file is using (raw, vhd, qcow)
        - **Recored disk backup format**: Whether backup file is raw, qcow or vhd (results only to raw for MVP)
        - **Record disk transport**: What transport was used; for MVP resolves to `http`
  - **Record server metadata** `.METADATA.json`
      - **Record snapshot ID**: store VDI id for future reference
      - **Record BIOS**: Save `HVM_boot_params.firmware` (bios or uefi), `platform.secureboot`, `bios_strings`, and `VM.NVRAM` for UEFI variables.
      - **CPU and memory configuration**: Record `VCPUs_max`, `VCPUs_at_startup`, `memory_static_max`, `memory_dynamic_min`, `memory_dynamic_max`.
      - **Record powerState**: Store `VM.power_state` as it was at snapshot time.
      - **Record consistency (quiesced, crash-consistent)**: Store a value, "consistency" unless guest-freeze step succeeded. For MVP this resolves to `crash-consistent`
        - Note: MVP will only be crash consistent
      - **Record tags**: same as inventory, snapshot `VM.tags` via XAPI
  - **Record full VM Snapshot** `.vm-config.json`
      - Serialize the full `VM.get_record` output (vCPUs, memory, VIFs, VBDs, platform, other_config, etc.) as a dedicated config file separate from `.METADATA.json`
  - **Record VIF details**: MAC address, network reference, device position, MTU.
  - **Record XVA descriptor**: a metadata-only XVA (a tar holding `ova.xml` with the VM, VBD, VIF and VDI records, but no disk data) 
    - Received through `GET https://<host>/export_metadata?session_id=…&uuid=<vm-uuid>`
  - **Cleanup snapshot**
    - **Make sure of no active transport**: by making sure any `export_raw_vdi` operation is finished/closed
    - **Remove all VDI from created snapshot**: Call `VDI.destroy` on each snapshot's included disk, not doing this will keep snapshot files lingering. Retry each destroy operation 3 times
    - **Call `VM.destroy` on the snapshot VM record**: retry 3 times
    - **Dont fail over cleanup**: Warn through logging when destroy operations aren't successful, but dont fail
  

## Exporter (in-place)
- Implement `Export()`
  - **Configure restore options**
    - Auto shutdown VM for restore (default: true), when false it will error when VM is still on
    - Auto start on restore (default: false)
    - Delete replaced disks from datastore (SR)
  - Shut down VM (when auto Shutdown VM is set to true)
  - On each stored Disk:
    - **Match disk topology**: TBD: Match on VDI UUID or use VMware approach, match on controller type, unit number or any equalivant in XCP
      - TODO: Determine match-up of values, VDI ID? Or something else
    - **Create a new VDI for incoming disk**: instead of replacing existing VDIs, this prevents us from losing data when a Restore job fails. We can then still swap back the old VDI and delete the failed new disk
  - - **vCPUs**: `VM.set_VCPUs_max` and `VM.set_VCPUs_at_startup`. Order matters (`VCPUs_at_startup` ≤ `VCPUs_max`): when increasing, set max first; when decreasing, set at_startup first. Also restore `platform["cores-per-socket"]` (CPU topology).
    - **Restore Memory config**: 
      - use the single call `VM.set_memory_limits(static_min, static_max, dynamic_min, dynamic_max)`, not the individual setters. XAPI enforces `static_min ≤ dynamic_min ≤ dynamic_max ≤ static_max`, so setting the values one at a time can fail part-way.
    - **Restore VM Config**: 
      - BIOS Firmware: `HVM_boot_params["firmware"]` (bios/uefi) and `HVM_boot_params["order"]` (e.g. `cd` = disk, then CD) via `VM.set_HVM_boot_params`. 
      - If the firmware differs, say changed from `uefi` to `bios`, don't switch it silently; the restored disk won't boot. Error out or require an explicit option.
      - Secure boot: `platform["secureboot"]` and other relevant `platform` keys (e.g. `viridian`, `nx`, `device-model`) via `VM.add_to_platform` / `VM.remove_from_platform`. 
        - Update key by key instead of overwriting the whole map, changed might have been made
    - **BIOS strings**: `VM.set_bios_strings`. These may be write-once on some versions; skip them if they're identical, and treat a refusal as a warning, not a failure.
    - **NVRAM (UEFI vars)**: It *must* be present before first boot
    - **NICs (VIFs)**: match backup VIFs to current VIFs by `device` position. If MAC, network or MTU differ, call `VIF.destroy` and then `VIF.create` with the saved `device`, `MAC`, `MTU`, `network` and `other_config`. 
      - Resolve the network by UUID; if it no longer exists in the pool, warn and skip that VIF instead of failing the restore. In-place restores usually have no VIF changes, so this should normally be a no-op.
    - **Identity/cosmetic (optional)**: `name_label`, `name_description` and `tags`. Put these behind a restore option; an operator may have renamed or retagged the VM on purpose since the backup.
  - **Finalize Restore**
    - Validate if all VDI disk are properly swapped before booting
  - **Rollback on failure**: When validation fails, swap back previous VDI (still existing as we did not replace them) to the original VDI.
  - **Delete orphan disks**: Optional: Delete old disks which have been replaces (option: `delete_orphaned_disks`?)
- Implement `Ping()`
  - Call `session.login_with_password(username, password, ...)` on the pool master. If valid, we follow up with `pool.get_all_records` to confirm the session has sufficient privileges and the pool is responsive. 
  - Pull VM information via XAPI: validate if VM is able to be backed up

### Exporter (spawn)
- Extend `Export()`
  - Resolve Spawn parameters:
    - Which SR hosts the Disks
    - Which target pool
    - Optionsal: which host affinity.
  - Implement Network adapter restore mode
    - `preserve` useful for production Restore jobs; preserve MAC address is for example used in DHCP IP resolution
    - `disconnected` Apply network configuration, but turn NICs off
    - `remove` remove NICs completely
  - **Implement Network quarantine** option
    - Have an ability to define a replacing NIC, instead of using NICs on file. Useful in restore jobs when VM might be infected.

## Features after MVP

- NBD transport
- HotAdd / Direct attachment
- CBT
- XCP volume backup/restore
- Instant Recovery
- List pools as backup/restore option
- `xcp-pool://` backup/restore
- `xcp-instance://` add: Selection of disk format
  - raw: `format=raw`
  - vhd: `format=vhd`
  - qcow: `format=cqow` **Needs a version check** only new versions support this
- Cleanup unfinished snapshots
  - tag our snapshots (e.g. `other_config["plakar-backup"]=<backup id
  - On every run, go through snapshots, determine if stale and cleanup before new snapshot created


## Documentation

- XAPI likely needs direct `root` account access on XAPI
  - If some would like least privilege, they need AD on the pool and a dedicated account with a role that’s allowed to snapshot, export and destroy snapshots (need to figure out roles). For example a `vm-admin` for inventory and a `backup-operator`, but exact permissions against the XAPI RBAC docs should be checked. (snapshot, export_raw_vdi, VDI.destroy, VM.destroy, import_raw_vdi)
- Disks (VDIs) with a raw format cannot be snapshotted

## Risk / Edge cases

- Queiscing: VMware detects quiesce failures (SOAP fault types) and automatically falls back to crash-consistent. XCP plan mentions guest-freeze but does not define: what happens if `fsfreeze`/VSS fails? Auto-fallback to crash-consistent? Error out? Record the outcome?
- Snapshot Coalescing; XCP has the same approach as VMware has around snapshotting, which in turn brings the same issues as VMware does. The biggest issues around a snapshot chain is coalescing. When a snapshot is created, XCP creates a delta file which the VM uses to continue persisting data. When we are done with a backup, we want to delete this snapshot, because these snapshot chains bring performance issues. This snapshot cleanup does not always work and we do not know the common pitfalls of XCP yet; major tests need to be done on cleaning up snapshots, snapshot chains can get to be an utter mess. Test cases should heavily revolve around concurrency, external backup jobs and the importers' behavior.
