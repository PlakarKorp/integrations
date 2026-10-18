# rsync Integration

## Overview

This integration backs up a module served by an **rsync daemon**
(`rsync://`, port 873) into a Kloset store.

Each backup lists the whole module in one connection. File content is fetched
only for files whose metadata (size, mode, modification time, owner) differs
from the previous snapshot; unchanged files cost nothing beyond the listing.

The rsync protocol client is a trimmed adaptation of
[gokrazy/rsync](https://github.com/gokrazy/rsync), in `internal/rsync`.

## Configuration

- `location` (required): `rsync://[<user>[:<password>]@]<host>[:<port>][/<module>[/<path>]]`
- `module` (optional): the module to back up, if not given in `location`
- `root` (optional): the directory to back up, appended to the location's
  path. `root=a/b` with `rsync://host/mod` backs up `mod/a/b`.
- `port` (optional): the daemon port, if not given in `location`, 873 by
  default
- `username` (optional): the module user, if not given in `location`
- `password` (optional): the module password, if not given in `location`
- `max_connections` (optional): the maximum number of concurrent connections
  to the daemon, 4 by default. Keep it below the daemon's `max connections`.

The path may name a directory or a single file. Files are stored under
`/<module>/<path>/<root>` in the snapshot.

## Examples

```bash
# configure an rsync source
$ plakar source add myrsync rsync://bob@rsync.example.org/pub password=secret

# back up the source
$ plakar at @mystore backup @myrsync
```

## Limitations

- Only the rsync daemon protocol is supported, not rsync over a remote shell
  (use the sftp integration for that).
- The client speaks protocol 27: modification times have a one second
  resolution, hard links are stored as separate files.
- Devices, named pipes and sockets are reported as errors and not backed up.
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

`go test ./...` runs against an in-process daemon. To test against a real
rsync daemon, including module authentication:

```bash
docker build -t plakar-rsyncd-test internal/rsync/testdata
docker run -d -p 18873:873 plakar-rsyncd-test
RSYNC_TEST_ADDR=127.0.0.1:18873 go test ./internal/rsync -run Interop
```
