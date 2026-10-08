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
volume, creates a temporary volume from the snapshot, uploads that to Glance and
streams the image into the snapshot. No OpenStack API reads a snapshot's bytes,
so the Glance hop is required. The snapshot, the temporary volume and the image
are deleted once the disk has been read, or when the importer closes if it
never is. Each delete is tried once; one that fails is logged with the
resource's ID, to delete by hand.

Before each delete, cleanup waits up to 15 minutes for Cinder to allow it, for
instance for the temporary volume to finish deleting before the snapshot goes.
This default is not configurable yet; it can become an option once volumes large
enough to need more are backed up.

The image goes through Glance, so the volume must fit Glance's
`image_size_cap`: 1 TiB by default, which operators can raise in
`glance-api.conf`. Time, and scratch space on the Cinder host, grow with the
volume's size: every backup copies the whole volume.

Stopping plakar with Ctrl-C or SIGTERM kills the plugin before it can clean
up. The snapshot, the temporary volume and the image are then left behind:
look for `plakar-backup-*` snapshots and `plakar-tmp-*` volumes and images.

In-use volumes are snapshotted as they are, so the backup is crash-consistent.

The snapshot holds:

- `/<volume-id>.qcow2`: the disk. It is named `.qcow2` whatever its format.
- `/.METADATA.json`: the volume, the Cinder snapshot and the Glance image. The
  image's `disk_format` is the disk's real format.

### Configuration

The authentication keys of the inventory, plus:

| Key | Description |
|-----|-------------|
| `location` | `openstack-block://<volume-id>` |
| `openstack_region` | The volume's region. Exactly one is required. |

The credential needs Cinder snapshot, volume and upload-to-image rights, and
Glance download and delete rights, on the volume's project.

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
| `location` | `openstack-block://` |
| `openstack_region` | The region to restore into. Exactly one is required. |

The credential needs Glance image create, upload and delete rights, and Cinder
volume create and delete rights, on the target project.

### Usage

```sh
plakar destination add myvoldst openstack-block:// \
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
deleted once it has been read, or when the importer closes if it never is.
Each attached volume goes through the same snapshot-to-Glance path as the
`openstack-block` importer, with its own cleanup.

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

The `openstack-instance` exporter restores a VM backup into a new Nova
server: a new root disk, and a new Cinder volume recreated and reattached
for each volume the backup holds. The original server is never touched.

The server's flavor and networks are resolved by ID first, falling back to
name if the ID no longer exists on the target cloud (a different project or
region than the one backed up); restore fails if neither resolves. Security
groups are attached by name, as saved, with no resolution. Each volume is
reattached to its original device (e.g. `/dev/vdb`) when the backup recorded
one, or left for Nova to pick otherwise.

Each disk's restore image goes through the same temporary Glance upload as
the volume restore, and is deleted once the server or the volume is created
from it, or when the exporter closes if a later disk's restore fails first.

### Configuration

The authentication keys of the inventory, plus:

| Key | Description |
|-----|-------------|
| `location` | `openstack-instance://` |
| `openstack_region` | The region to restore into. Exactly one is required. |

The credential needs Nova server create and volume-attach rights, Glance
image create, upload and delete rights, Neutron network read rights, and the
Cinder rights the volume restore needs, on the target project.

### Usage

```sh
plakar destination add myvmdst openstack-instance:// \
    openstack_auth_url=https://keystone.example.com:5000/v3 \
    openstack_application_credential_id=<id> \
    openstack_application_credential_secret=<secret> \
    openstack_region=RegionOne
plakar restore -to @myvmdst <snapshot-id>
```
