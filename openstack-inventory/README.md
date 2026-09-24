# OpenStack Inventory

Plakar inventory for OpenStack. It lists the backup targets of one OpenStack
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

## Configuration

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

## Usage

Build and install the package:

```sh
make package
plakar pkg add ./openstack-inventory_v0.0.1_*.ptar
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
