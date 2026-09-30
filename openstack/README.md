# OpenStack

Plakar inventory and Cinder volume backup for OpenStack.

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
