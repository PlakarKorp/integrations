# NFS Integration

## Overview

**NFS (Network File System)** is a distributed file system protocol that lets a
client access files on a remote server as if they were local. It is ubiquitous
in datacenter and enterprise environments for shared storage, home directories
and appliance exports (NAS).

This integration speaks **NFSv3 directly over ONC-RPC** — it does not rely on a
kernel mount and needs no root privileges or `mount` capability on the host. It
uses the [vmware/go-nfs-client](https://github.com/vmware/go-nfs-client) library.

This integration allows:

* **Backup of files hosted on NFS exports into a Kloset repository:**
  Walk an exported directory tree and capture files and directories with their
  size, mode, ownership (uid/gid) and modification time.

* **Restoration of snapshots to NFS destinations:**
  Restore previously backed-up snapshots directly onto an NFS export,
  recreating the directory hierarchy and file contents.

---

## Configuration

The supported configuration options are:

* `location` (**required**): the NFS target as `nfs://<host>[:<port>]/<export>[/<subpath>]`.
  Everything in the path is the server-side export to mount; an optional deeper
  subpath (or the `root` option) narrows the walk within that export.
* `root`: path to walk/restore, relative to the mounted export (default: the
  whole export).
* `port`: NFS/MOUNT service port. By default the service is resolved through the
  server's portmapper (port 111).
* `uid` / `gid`: numeric credentials presented to the server as the `AUTH_UNIX`
  identity used for authorization (default: `0`).

---

## Examples

```sh
# back up an NFS export
$ plakar backup nfs://nas.example.com/exports/data

# back up only a subtree of an export, as a specific uid/gid
$ plakar backup -o uid=1000 -o gid=1000 nfs://nas.example.com/exports/home/alice

# restore the snapshot "abc" onto an NFS export
$ plakar restore -to nfs://nas.example.com/exports/restore abc
```

---

## Limitations

* Only **NFSv3** is supported. NFSv4, Kerberos (`sec=krb5`) and NFS-over-UDP are
  not.
* Authorization uses `AUTH_UNIX` (numeric uid/gid). The export must permit the
  presented credentials (e.g. an `*(rw,no_root_squash)`-style export, or a
  matching uid).
* On restore, only **regular files and directories** are reconstructed. Symlinks,
  device nodes, sockets and FIFOs are reported as errors, because the NFSv3
  client library exposes neither `SYMLINK` nor `MKNOD`. File modes are applied at
  creation time; ownership and timestamps are not restored (no `SETATTR`).
