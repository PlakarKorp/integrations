# OpenWRT Integration

## Overview

OpenWRT is an open source Linux distribution made for routers and network
embedded devices, It allow to configure finely networking, firewall, Wi-Fi
VPN, DNS, DHCP and many more via a package system.

This integration allows you to backup OpenWRT. It uncompress the backup
archive so two snapshots would show differences at the file level.

The exporter part live recompress the snapshot into a valid archive before
restoring the device.

### Configuration

- `location` (required): ex: `openwrt://ip-address-or-hostname`, the device URL.
- `login` (required): ex: `root` the login that will perform the backup or the restore.
- `password` (optional): default: `` user password, it is optional as a fresh
  install would have none.
- `use_ssl` (optional): Default: `true` use https to make request to the
  OpenWRT device.
- `apply_sysupgrade` (optional): Default: `true`, apply the sysupgrade when
  restoring backup.
- `reboot_device` (optional): Default: `true`, reboot device after a
  sysupgrade. It default to `true` but it will be inhibited by
  apply_sysupgrade set to `false`
- `timeout` (optional): Default: 30 seconds, > 0. Time to wait for device to respond.
- `insecure_skip_verify` (optional): Default `false`, skip ssl certificate verifications.
