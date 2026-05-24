# SFTP Integration

## Overview

**SFTP (SSH File Transfer Protocol)** is a secure file transfer protocol that operates over SSH, providing encrypted access to files on remote servers.
It is commonly used in server administration, enterprise storage solutions, and legacy systems for file exchange.

This integration allows:

* **Seamless backup of files hosted on SFTP servers into a Kloset repository:**
  Capture and store entire directory trees or specific remote paths from SFTP servers with full metadata, enabling secure and consistent backups across environments.

* **Direct restoration of snapshots to remote SFTP destinations:**
  Restore previously backed-up snapshots directly to SFTP servers, preserving file hierarchies, permissions, and timestamps.

* **Compatibility with secure remote environments and legacy systems:**
  Supports a wide range of remote servers, from modern cloud-hosted Linux instances to older on-premise infrastructure relying on SSH-based access.

---

## Configuration

The supported configuration options are:

* `location`: remote server hostname or IP

It relies on the `sftp` executable and will use the user-configuration for additional options.

---

## Examples

```sh
# back up a remote path over SFTP
$ plakar backup sftp://user@host/var/www

# restore the snapshot "abc" to an SFTP server
$ plakar restore -to sftp://user@host/var/www abc

# create a kloset repository on a remote SFTP server
$ plakar at sftp://user@host/backups create
```
