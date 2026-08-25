# OpenWRT Integration

## Overview

OpenWRT is an open source Linux distribution made for routers and network
embedded devices, It allow to configure finely networking, firewall, Wi-Fi
VPN, DNS, DHCP and many more via a package system.

This integration allows you to back up an OpenWRT Device. It decompress the backup
archive so differences between snapshots can be tracked at the file level.

The exporter live compress the snapshot on the fly into a valid OpenWRT archive
before restoring the device.

### Configuration

- `location` (required): ex: `openwrt://ip-address-or-hostname`, the device URL.
- `login` (required): ex: `root` the username that will perform backups or restores.
- `password` (optional): default: `` user password, it is optional as a fresh
  install would have none.
- `use_ssl` (optional): Default: `true` use https when connecting to the OpenWRT
  device.
- `apply_sysupgrade` (optional): Default: `true`, Run sysupgrade when restoring
  backup.
- `reboot_device` (optional): Default: `true`, reboot device after a
  sysupgrade. It default to `true`. This option is ignored when apply_sysupgrade
  is `false`
- `timeout` (optional): Default: 30, in seconds, when waiting for the device to
  respond. Must be greater than 0.
- `insecure_skip_verify` (optional): Default `false`, Disable TLS certificate
  verification

### How to add a dedicated backup user

- Add a virtual dedicated backup user in `/etc/config/rpcd`:

```config
config login
    option username 'backup'
    option password '<password-hash>'
    list read 'backup-api'
```

- To generate the password-hash use `uhttpd -m 'Sup3rSikretP4ssw0rd`
- Add a custom rpcd ACL file at `/usr/share/rpcd/acl.d/backup-api.json`

```json
{
    "backup-api": {
      "description": "Allow configuration backup download",
      "read": {
            "cgi-io": [
                "backup"
            ]
        }
    }
}
```

- Add the custom ACL file to sysupgrade config so it is backed up, in `/etc/sysupgrade.conf`:

```config
/usr/share/rpcd/acl.d/backup-api.json
```

- Restart rpcd `/etc/init.d/rpcd restart`
- Add a [plakar-source](https://www.plakar.io/docs/community/v1.1.0/references/commands/plakar-source/) pointing at your device.
- Test with `plakar source ping @mydevice`
