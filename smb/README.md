# SMB Integration

## Overview

**SMB (Server Message Block), also known as CIFS,** is the file-sharing protocol
used by Windows networks and by NAS appliances and Samba servers on Unix. It is
the standard way to share folders ("shares") across a LAN.

This integration speaks **SMB2/3 directly over TCP** — it does not rely on a
kernel mount or a system `smbclient`, and needs no root privileges. It uses the
[hirochachacha/go-smb2](https://github.com/hirochachacha/go-smb2) library and
authenticates with NTLMv2.

This integration allows:

* **Backup of files hosted on SMB shares into a Kloset repository:**
  Walk a share (or a subtree of it) and capture files, directories and symlinks
  with their size and modification time.

* **Restoration of snapshots to SMB destinations:**
  Restore previously backed-up snapshots directly onto an SMB share, recreating
  the directory hierarchy, file contents and symlinks.

---

## Configuration

The supported configuration options are:

* `location` (**required**): the SMB target as
  `smb://[<user>[:<password>]@]<host>[:<port>]/<share>[/<subpath>]`. The first
  path segment is the share to mount; an optional deeper subpath (or the `root`
  option) narrows the walk within that share.
* `root`: path to walk/restore, relative to the share (default: the whole share).
* `port`: SMB TCP port (default `445`).
* `username` / `password`: NTLM credentials, if not given in the URL userinfo.
  Anonymous access uses `Guest`.
* `domain`: NTLM authentication domain or workgroup.

---

## Examples

```sh
# back up a share (credentials in the URL)
$ plakar backup smb://alice:secret@nas.example.com/documents

# back up a subtree, credentials passed as options
$ plakar backup -o username=alice -o password=secret \
    smb://nas.example.com/documents/projects

# restore the snapshot "abc" onto a share
$ plakar restore -to smb://alice:secret@nas.example.com/restore abc
```

---

## Limitations

* Authentication is **NTLMv2**; Kerberos is not supported.
* SMB has no native Unix ownership or permission model: imported files carry a
  mode synthesized from DOS attributes (directory / read-only), and **uid/gid,
  inode and link count are not available**. On restore, file contents,
  directories and symlinks are recreated and modification time is preserved on a
  best-effort basis, but Unix permission bits and ownership are not.
* Device nodes, sockets and FIFOs have no SMB representation and are reported as
  errors on export.
