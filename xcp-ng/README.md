# XCP-ng

Plakar inventory for XCP-ng, through the Xen Orchestra REST API.

Backup and restore are not implemented yet.

## Inventory

The inventory talks to one Xen Orchestra instance and covers every pool it
manages.

### Configuration

| Key | Description |
|-----|-------------|
| `xo_url` | Xen Orchestra base URL, e.g. `https://xo.example.com` |
| `xo_token` | Xen Orchestra authentication token |
| `tls_insecure_no_verify` | Disable TLS certificate verification (default `false`); for testing only |

Create the token in Xen Orchestra under *User → Authentication tokens*, for a
user that can see the pools to inventory.

### Resources

One entry per VM, across all pools:

| Field | Value |
|-------|-------|
| URN | `urn:xcp-ng:pool:<pool-uuid>:vm:<vm-uuid>` |
| Name | VM name |
| Region | Pool name |
| Tags | XO tags |
| Endpoints | VM UUID, then the IPv4 and IPv6 addresses reported by the guest tools, without link-local ones |

VMs without guest tools have no IP endpoints.

### Usage

Build and install the package:

```sh
make package
plakar pkg add ./xcp-ng_v0.0.1_*.ptar
```

## Trademarks

XCP-ng and the XCP-ng logo (`assets/icon.svg`) are trademarks of Vates, used to
identify the platform this integration works with. They are not covered by this
repository's license.
