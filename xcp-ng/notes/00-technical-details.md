## XCP-ng scaffolding


### Directory structure
/xcp-ng/
  /instance/
    importer.go
    exporter.go
  /inventory/
    inventory.go
  /xoa/
    client.go
    errors.go
  /xapi/
    client.go
    errors.go
  /transfer/
    transfer.go
  /client/
    client.go
    refs.go
    vm.go
    vdi.go
    ....
    pool.go
    guest_metrics.go


## Psuedo code example
```
struct Client {
  VM: VMService
  VDI: VDIService
  VBD: VBDService
  VIF: VIFService
  SR: SRService
  Pool: PoolService
  GuestMetrics: GuestMetricsService
}

rpc = xapi.New({ url: poolMaster, tls: tlsConfig })
err := rpc.Login(ctx, user, pass)
if err != nil {
  return err
}
defer rpc.Logout(context.WithoutCancel(ctx))

client = client.New(rpc)
xfer = transfer.New(rpc)


// shared by every resource
client.VM.GetAllRecords(ctx)            // tags, allowed_operations, guest_metrics included
client.VM.GetRecord(ctx, vmRef)
client.VM.GetRecordRaw(ctx, vmRef)      // full record as raw JSON, for .vm-config.json
client.VM.GetByUUID(ctx, uuid)
client.VM.AddToOtherConfig(ctx, vmRef, key, value)

// resource specific
client.VM.Snapshot(ctx, vmRef, name)
client.VM.Destroy(ctx, vmRef)
client.VM.CleanShutdown(ctx, vmRef)
client.VM.SetMemoryLimits(ctx, vmRef, limits)
client.VDI.Destroy(ctx, vdiRef)
client.GuestMetrics.GetAllRecords(ctx)

// helpers on records, no extra calls
vm.IsRegularVM()                        // not template, snapshot or dom0
vm.CanSnapshot()                        // IsRegularVM + "snapshot" in AllowedOperations


func New(rpc) {
  return &Client{
    VM:           VMService{objects[VMRecord]{rpc, "VM"}},
    VDI:          VDIService{objects[VDIRecord]{rpc, "VDI"}},
    VBD:          VBDService{objects[VBDRecord]{rpc, "VBD"}},
    VIF:          VIFService{objects[VIFRecord]{rpc, "VIF"}},
    SR:           SRService{objects[SRRecord]{rpc, "SR"}},
    Pool:         PoolService{objects[PoolRecord]{rpc, "pool"}},
    GuestMetrics: GuestMetricsService{objects[GuestMetricsRecord]{rpc, "VM_guest_metrics"}},
  }
}
```

Method names follow XAPI (`get_record` -> `GetRecord`), so the XAPI docs map 1:1 to the code.


### Shared methods

`get_record`, `get_all_records`, `get_by_uuid` and `add_to_other_config` work the same for
every XAPI class. They are written once in a generic `objects[R]` and embedded in each
service. Services only add their own methods.

```
struct objects[R] {
  rpc
  class   // "VM", "VDI", ...
}

func (o objects[R]) GetRecord(ctx, ref Ref[R]) R {
  return o.rpc.Call(ctx, o.class + ".get_record", &record, ref)
}

struct VMService { objects[VMRecord] }

func (s VMService) Snapshot(ctx, vm VMRef, name) VMRef {
  return s.rpc.Async(ctx, "VM.snapshot", &ref, vm, name)
}
```


### Refs

XAPI calls take an `OpaqueRef`. Each class gets its own ref type so a VDI ref can't be
passed to a VM call.

```
type Ref[R] string

VMRef  = Ref[VMRecord]
VDIRef = Ref[VDIRecord]
VBDRef = Ref[VBDRecord]
...

NullRef = "OpaqueRef:NULL"
```

Refs are only used for calls during a session. UUIDs are what we store in `.METADATA.json`
and use to match disks/VIFs on restore.


### Records

Records only hold the fields we use. When we need everything (`.vm-config.json`), use
`GetRecordRaw`, so no field gets lost.

```
struct VMRecord {
  UUID, NameLabel, PowerState
  IsATemplate, IsASnapshot, IsControlDomain
  AllowedOperations []string
  Tags []string
  VBDs []VBDRef
  VIFs []VIFRef
  GuestMetrics GuestMetricsRef
  OtherConfig map[string]string
  ... VCPUs_*, memory_*, HVM_boot_params, platform, NVRAM, bios_strings
}

struct VBDRecord {
  UUID, VM, VDI
  Type          // Disk | CD | Floppy
  Mode          // RW | RO
  UserDevice, Bootable, Unpluggable, Empty
}

struct VDIRecord {
  UUID, SR, VirtualSize, PhysicalUtilisation, Type, Location, SMConfig
}
```

TODO: check how int64 fields (`virtual_size`, `memory_*`) come back over JSON-RPC on the
oldest XCP-ng version we support. Over XML-RPC they are strings; if JSON-RPC does the
same, add an `xapi.Int64` type with a custom `UnmarshalJSON`.


# XAPI

A `XAPI` client with a single `Call`, which sends the JSON-RPC request and decodes the result
into `out`. JSON-RPC only for now; the codec stays internal, so XML-RPC can be added later
without touching callers.

The XAPI client owns the session:
- adds the session ref as the first param of every call
- re-logs in once on `SESSION_INVALID`
- switches to the pool master on `HOST_IS_SLAVE`
- logs in with `originator = "plakar"`, and must always log out (XAPI caps sessions per
  user and drops the oldest ones)

Long operations (`VM.snapshot`, `VM.destroy`, `VDI.destroy`, `VM.clean_shutdown`) go
through `Async`: it calls `Async.<method>`, polls the task until done, and cancels the task
when `ctx` is cancelled. A sync call can't be cancelled on the server.

```
rpc = xapi.New(config)
rpc.Login(ctx, user, pass)

err = rpc.Call(ctx, "VM.get_record", &record, vmRef)
err = rpc.Async(ctx, "VM.snapshot", &snapRef, vmRef, name)

rpc.Session()   // used by transfer
rpc.Logout(ctx)
```

Note: `task.result` is an XML-RPC encoded `<value>` string, even over JSON-RPC.

### Errors

XAPI returns errors as `["CODE", "param", ...]`. These are turned into a typed error so
callers can check the code (cleanup retries, restore rollback).

```
struct Error {
  Code   string
  Params []string
}

xapi.IsCode(err, "OPERATION_NOT_ALLOWED")
xapi.IsCode(err, "VM_BAD_POWER_STATE")
xapi.IsCode(err, "HANDLE_INVALID")
```


# Transfer

Disk and metadata transfers are not RPC calls but plain HTTP GET/PUT with `session_id` in
the query string. They live in their own package and reuse the XAPI session and TLS config.

```
xfer = transfer.New(rpc)

rc = xfer.ExportRawVDI(ctx, vdiUUID, transfer.FormatRaw)    // GET /export_raw_vdi
err = xfer.ImportRawVDI(ctx, vdiUUID, transfer.FormatRaw, r) // PUT /import_raw_vdi
rc = xfer.ExportMetadata(ctx, vmUUID)                        // GET /export_metadata
```

- Follows redirects: disks on a local SR are served by the host that owns the SR, not the
  pool master. Query params must be kept on the redirect.
- Passes a `task_id`, so we can check no transfer is still running before cleanup, and
  cancel it when `ctx` is cancelled.
- TLS: XCP-ng hosts use self-signed certificates by default. Support a CA bundle or
  fingerprint pinning, plus an explicit insecure option.
- Formats: `raw` for MVP; `vhd` later, `qcow2` only on newer versions.


# Example: import flow

```
ref = client.VM.GetByUUID(ctx, vmUUID)
vm = client.VM.GetRecord(ctx, ref)
if !vm.CanSnapshot() {
  return error
}

snap = client.VM.Snapshot(ctx, ref, "Plakar-Backup-<timestamp>")
client.VM.AddToOtherConfig(ctx, snap, "plakar-backup", backupID)

raw = client.VM.GetRecordRaw(ctx, ref)      // -> .vm-config.json

snapRecord = client.VM.GetRecord(ctx, snap)
for vbdRef in snapRecord.VBDs {
  vbd = client.VBD.GetRecord(ctx, vbdRef)
  if vbd.Type != "Disk" || vbd.Empty {
    continue
  }
  vdi = client.VDI.GetRecord(ctx, vbd.VDI)
  rc = xfer.ExportRawVDI(ctx, vdi.UUID, transfer.FormatRaw)
  // stream rc into plakar
}

// cleanup: VDI.Destroy on each snapshot disk, then VM.Destroy on the snapshot
```

The backup/restore flow itself (retries, rollback) lives in `instance/`, not in `client/`.
`instance/` depends on small interfaces it defines itself, so tests can use fakes.
`client/` is tested against an `httptest.Server` replaying recorded JSON-RPC responses.
