# rsync Integration

## Overview

This integration backs up a module served by an **rsync daemon**
(`rsync://`, port 873) into a Kloset store, and restores snapshots into one.

Each backup lists the whole module in one connection. File content is fetched
only for files whose metadata (size, mode, modification time, owner) differs
from the previous snapshot; unchanged files cost nothing beyond the listing.

The rsync protocol client is a trimmed adaptation of
[gokrazy/rsync](https://github.com/gokrazy/rsync), in `internal/rsync`.

## Configuration

The source and the destination take the same parameters:

- `location` (required): `rsync://[<user>[:<password>]@]<host>[:<port>][/<module>[/<path>]]`
- `module` (optional): the module, if not given in `location`
- `root` (optional): a directory appended to the location's path.
  `root=a/b` with `rsync://host/mod` designates `mod/a/b`.
- `port` (optional): the daemon port, if not given in `location`, 873 by
  default
- `username` (optional): the module user, if not given in `location`
- `password` (optional): the module password, if not given in `location`
- `max_connections` (optional): the maximum number of concurrent connections
  to the daemon, 4 by default. Keep it below the daemon's `max connections`.

A setting given both in `location` and as a parameter must have the same
value in both.

As a source, the path may name a directory or a single file. Files are stored
under `/<module>/<path>/<root>` in the snapshot.

As a destination, the path is a directory, created if missing.

## Examples

```bash
# configure an rsync source
$ plakar source add myrsync rsync://bob@rsync.example.org/pub password=secret

# back up the source
$ plakar at @mystore backup @myrsync

# configure an rsync destination and restore a snapshot into it
$ plakar destination add myrsyncdst rsync://bob@rsync.example.org/restore password=secret
$ plakar at @mystore restore -to @myrsyncdst <snapid>
```

## Restoring

Each restored file and symlink costs one connection, directories are created
as they come and get their final permissions and times at the end. Existing
files are overwritten; files absent from the snapshot are left in place.

The destination module needs, in `rsyncd.conf`:

- `read only = no`
- `use chroot = yes`, to restore symlink targets as they are: otherwise the
  daemon strips their leading `../` and `/`, and rewrites them unless
  `munge symlinks = no`.
- to restore owners and groups, a daemon running as root, or
  `fake super = yes`; otherwise files belong to the daemon's user.

## Limitations

- Only the rsync daemon protocol is supported, not rsync over a remote shell
  (use the sftp integration for that).
- The client speaks protocol 27: modification times have a one second
  resolution, hard links are stored as separate files.
- Devices, named pipes and sockets are reported as errors and not backed up,
  nor restored.
- Extended attributes are neither backed up nor restored.
- A changed file is fetched in full: rsync's block-level delta transfer is not
  used yet.
- Each fetched file costs one connection and handshake, which is slow for many
  small changed files.
- The snapshot is not atomic: a file changed between the listing and its fetch
  is stored as fetched.
- Errors reported by the daemon while listing (e.g. an unreadable directory)
  are attached to the backup root, as the protocol does not say which path
  they concern.

## Tests

`go test ./...` runs against an in-process daemon, and against a real rsync
daemon started with Docker from `internal/rsynctest/testdata/Dockerfile`. The
latter is skipped when Docker is not available.
