# OpenStack

Plakar inventory, Cinder volume backup and restore, and Nova server backup and
restore for OpenStack.

## Inventory

The inventory lists the backup targets of one OpenStack
project, in every region the credential can reach:

- Nova servers
- Cinder volumes
- Glance images owned by the project
- Swift containers
- Trove databases

Each entry carries the resource's own metadata and tags, and a URN of the form
`urn:openstack:<project-id>:<service>:<region>:<type>:<id>`, e.g.

```
urn:openstack:f7e1da6e00494d00bb69bbd18f8eb6c5:nova:RegionOne:server:c0ce1a7f-9397-4440-b8a6-3046be0613ce
```

`<service>:<type>` is one of `nova:server`, `cinder:volume`, `glance:image`,
`swift:container` or `trove:instance`; for Swift containers `<id>` is the
container name. Services the credential can't read are skipped.

### Configuration

Authenticate with an application credential (recommended):

| Key | Description |
|-----|-------------|
| `openstack_auth_url` | Keystone v3 URL, e.g. `https://keystone.example.com:5000/v3` |
| `openstack_application_credential_id` | Application credential ID |
| `openstack_application_credential_secret` | Application credential secret |

or with a username and password:

| Key | Description |
|-----|-------------|
| `openstack_auth_url` | Keystone v3 URL |
| `openstack_username`, `openstack_password` | User credentials |
| `openstack_project_id` or `openstack_project_name` | Project to scan |
| `openstack_domain_name` | User and project domain (default `Default`) |

Optional: `openstack_region`, a comma-separated list of regions to scan
(default: all regions in the catalog).

### Usage

Build and install the package:

```sh
make package
plakar pkg add ./openstack_v0.0.1_*.ptar
```

Run it locally with the inventory SDK's runner:

```sh
make build
go run github.com/PlakarKorp/go-inventory-sdk/cmd/run@v1.1.2 \
    -o openstack_auth_url=https://keystone.example.com:5000/v3 \
    -o openstack_application_credential_id=env:OS_APPLICATION_CREDENTIAL_ID \
    -o openstack_application_credential_secret=env:OS_APPLICATION_CREDENTIAL_SECRET \
    ./openstack-inventory
```

It prints one JSON entry per resource found.

## Volume backup

The `openstack-block` importer backs up one Cinder volume. It snapshots the
volume and creates a temporary volume from the snapshot, as no OpenStack API
reads a snapshot's bytes. It then reads the temporary volume one of two ways:

- When plakar runs on a Nova server of the same cloud, project and region, the
  temporary volume is attached to that server and read raw from the block
  device it turns up as (`/dev/disk/by-id/virtio-<volume-id>`, or its
  virtio-scsi equivalent). Nothing goes through Glance. plakar learns which
  server it is on from the metadata service, or from DMI when that is
  disabled. Reading a block device needs root or the `disk` group: run plakar
  as root, or `usermod -aG disk <user>`.
- Otherwise, or when the attach fails (Nova refuses cross-AZ attaches on some
  clouds, say), no device turns up within a minute or it cannot be read, the
  temporary volume is uploaded to Glance and the image streamed into the
  snapshot.

The snapshot, the temporary volume (detached first) and the image, if any, are
deleted once the disk has been read, or when the importer closes if it never
is. Each delete is tried once; one that fails is logged with the resource's
ID, to delete by hand.

Before each delete, cleanup waits up to 15 minutes for Cinder to allow it, for
instance for the temporary volume to finish deleting before the snapshot goes.
This default is not configurable yet; it can become an option once volumes large
enough to need more are backed up.

Through Glance, the volume must fit Glance's `image_size_cap`: 1 TiB by
default, which operators can raise in `glance-api.conf`. Time, and scratch
space on the Cinder host, grow with the volume's size: every backup copies the
whole volume, and Cinder converts it to qcow2 on its own host first. When that
upload fails on Cinder's side, Cinder deletes the image; the backup then fails
naming the volume and the image, and the cause is in cinder-volume's log. Read
from a block device, the backup reads the volume's full nominal size, used or
not; plakar deduplicates the empty parts.

Stopping plakar with Ctrl-C or SIGTERM kills the plugin before it can clean
up. The snapshot, the temporary volume and the image are then left behind:
look for `plakar-backup-*` snapshots and `plakar-tmp-*` volumes and images.

In-use volumes are snapshotted as they are, so the backup is crash-consistent.

The snapshot holds:

- `/<volume-id>.qcow2`: the disk. It is named `.qcow2` whatever its format.
- `/.METADATA.json`: the volume, the Cinder snapshot and, when the disk went
  through one, the Glance image. `disk_format` is the disk's real format:
  `raw` when read from a block device, else the image's.

An encrypted volume's bytes differ by path. Through Glance they are the
ciphertext, and the metadata carries the key they are under, for a restore to
pass on. From a block device they are in the clear, as the hypervisor unlocks
the volume for the server, and a restore lets Cinder encrypt them anew under
the restored volume type.

### Configuration

The authentication keys of the inventory, plus:

| Key | Description |
|-----|-------------|
| `location` | `openstack-block://<volume-id>` |
| `openstack_region` | The volume's region. Exactly one is required. |

The credential needs Cinder snapshot, volume and upload-to-image rights, and
Glance download and delete rights, on the volume's project. To read from a
block device it also needs Nova server read and volume attach and detach
rights.

### Usage

```sh
plakar source add myvol openstack-block://c0ce1a7f-9397-4440-b8a6-3046be0613ce \
    openstack_auth_url=https://keystone.example.com:5000/v3 \
    openstack_application_credential_id=<id> \
    openstack_application_credential_secret=<secret> \
    openstack_region=RegionOne
plakar backup @myvol
```

## Volume restore

The `openstack-block` exporter restores a volume backup into a new Cinder
volume. It uploads the disk to a temporary Glance image, with the disk format
recorded in `.METADATA.json`, creates the volume from it, then deletes the
image. The original volume is never touched, and the new one is left detached.

The new volume takes the original's size, type, availability zone, description
and metadata, and its name, or `plakar-restore-<volume-id>` if it had none. The
type and availability zone must exist where it is restored. A volume Cinder
puts in `error` is deleted and created again, in up to three attempts.

Image properties, such as `hw_*`, are not restored.

### Known gaps

- Encryption is a property of the target cloud's volume type, not something
  this exporter sets directly: Cinder has no `encrypted` flag on volume
  create. The original type's *name* is restored verbatim; whether that name
  exists on the target cloud, and whether it means the same thing there
  (encrypted or not), is never checked. A type name reused for something
  unencrypted restores silently unencrypted.

### Configuration

The authentication keys of the inventory, plus:

| Key | Description |
|-----|-------------|
| `location` | `openstack-block://` or `openstack-block://spawn`, both restoring into a new, detached volume. Naming an existing volume or server is not supported yet. |
| `openstack_region` | The region to restore into. Exactly one is required. |

The credential needs Glance image create, upload and delete rights, and Cinder
volume create and delete rights, on the target project.

### Usage

```sh
plakar destination add myvoldst openstack-block://spawn \
    openstack_auth_url=https://keystone.example.com:5000/v3 \
    openstack_application_credential_id=<id> \
    openstack_application_credential_secret=<secret> \
    openstack_region=RegionOne
plakar restore -to @myvoldst <snapshot-id>
```

## VM backup

The `openstack-instance` importer backs up one Nova server: its metadata
(flavor and networks), its root disk, and each attached Cinder volume. The root
disk and the volumes are captured at different moments, not atomically.
Boot-from-volume servers are refused: their root disk is a Cinder volume, out
of scope for this connector.

The root disk is snapshotted into a Glance image directly through Nova, then
deleted once it has been read, or when the importer closes if it never is; it
exists in no other form, so it always goes through Glance. Each attached
volume is read as the `openstack-block` importer reads one, from a block
device when plakar runs on a Nova server of the cloud and through Glance
otherwise, with its own cleanup.

The snapshot holds:

- `/.METADATA.json`: the server, its flavor and its networks.
- `/block-storage/glance-<image-id>/disk.qcow2` and `.METADATA.json`: the root
  disk.
- `/block-storage/cinder-<volume-id>/disk.qcow2` and `.METADATA.json`, one per
  attached volume.

### Configuration

The authentication keys of the inventory, plus:

| Key | Description |
|-----|-------------|
| `location` | `openstack-instance://<server-id>` |
| `openstack_region` | The server's region. Exactly one is required. |

The credential needs Nova server read and snapshot rights, Glance image
download and delete rights, Neutron network read rights, and the Cinder and
Glance rights the volume backup needs, on the server's project.

### Usage

```sh
plakar source add myvm openstack-instance://c0ce1a7f-9397-4440-b8a6-3046be0613ce \
    openstack_auth_url=https://keystone.example.com:5000/v3 \
    openstack_application_credential_id=<id> \
    openstack_application_credential_secret=<secret> \
    openstack_region=RegionOne
plakar backup @myvm
```

## VM restore

The `openstack-instance` exporter restores a VM backup one of two ways,
depending on `location`:

- Bare or `spawn`: a new Nova server, with a new root disk and a new Cinder
  volume recreated and reattached for each volume the backup holds. The
  original server is never touched.
- An existing server ID: every backed-up disk, root included, is restored as
  a new Cinder volume and attached to that server. No server is created;
  flavor, network and security-group resolution are skipped entirely. The
  root disk never had a real Cinder volume behind it, so its volume is sized
  from the backed-up flavor's declared disk size.

In spawn mode, the server's flavor and networks are resolved by ID first,
falling back to name if the ID no longer exists on the target cloud (a
different project or region than the one backed up); restore fails if
neither resolves. Security groups are attached by name, as saved, with no
resolution. Each volume is reattached to its original device (e.g.
`/dev/vdb`) when the backup recorded one, or left for Nova to pick otherwise.

Each disk's restore image goes through the same temporary Glance upload as
the volume restore, and is deleted once the server or the volume is created
from it, or when the exporter closes if a later disk's restore fails first.

### Configuration

The authentication keys of the inventory, plus:

| Key | Description |
|-----|-------------|
| `location` | `openstack-instance://` or `openstack-instance://spawn` to restore into a new server, or `openstack-instance://<server-id>` to attach every restored disk to that existing server. |
| `openstack_region` | The region to restore into. Exactly one is required. |

Spawn mode needs Nova server create and volume-attach rights, Glance image
create, upload and delete rights, Neutron network read rights, and the Cinder
rights the volume restore needs, on the target project. Existing-server mode
needs fewer: no Nova server-create, flavor-read, or network-read rights —
only Nova volume-attach, Glance image create, upload and delete, and Cinder
volume create and delete.

### Usage

```sh
plakar destination add myvmdst openstack-instance:// \
    openstack_auth_url=https://keystone.example.com:5000/v3 \
    openstack_application_credential_id=<id> \
    openstack_application_credential_secret=<secret> \
    openstack_region=RegionOne
plakar restore -to @myvmdst <snapshot-id>
```
