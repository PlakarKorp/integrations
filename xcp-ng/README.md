# XCP-ng

Plakar inventory for XCP-ng, through the Xen Orchestra REST API.

Work in progress: the connector is not implemented yet.

## Inventory

The inventory talks to one Xen Orchestra instance and covers every pool it
manages.

### Configuration

| Key | Description |
|-----|-------------|
| `xo_url` | Xen Orchestra base URL, e.g. `https://xo.example.com` |
| `xo_token` | Xen Orchestra authentication token |
| `xo_tls_skip_verify` | Disable TLS certificate verification (default `false`); for testing only |

Create the token in Xen Orchestra under *User → Authentication tokens*, for a
user that can see the pools to inventory.

### Usage

Build and install the package:

```sh
make package
plakar pkg add ./xcp-ng_v0.0.1_*.ptar
```
