# Rclone Integration

## Overview

**Rclone** is a command-line program to manage files on cloud storage. It supports a wide range of cloud providers (Google Drive, OneDrive, Dropbox, iCloud Drive, and more) and allows seamless transfer, backup, and synchronization of files.

This integration allows:

- Seamless backup of files from Rclone-supported cloud providers into a Kloset repository
- Direct restoration of snapshots to remote cloud destinations via Rclone
- Compatibility with a wide range of cloud storage services and legacy tools

## Installation

If a pre-built package exists for your system and architecture, you can simply install it using:

```sh
$ plakar pkg add rclone
```

Otherwise, you can first build it:

```sh
$ plakar pkg build rclone
```

This should produce `rclone-vX.Y.Z.ptar` that can be installed with:

```bash
$ plakar pkg add ./rclone-v0.1.0.ptar
```

## Configuration

You must have a valid Rclone remote configured (`rclone config`) for your cloud provider. The remote config lives in `~/.config/rclone/rclone.conf`.

To include Rclone in your Plakar workflow, you can add a source, destination and store using the following commands:

```bash
# for sources
$ rclone config show | plakar source import configname

# for destinations
$ rclone config show | plakar destination import configname

# for stores
$ rclone config show | plakar store import configname
```

> *Note:* The `configname` is the name of the Rclone remote you configured in `rclone config`.

## Supported Providers

Plakar supports the following Rclone providers for backup and restore operations:

- **Google Drive**
- **Google Photo**
- **OneDrive**
- **Dropbox**
- **OpenDrive**
- **Proton Drive**
- **Koofr**
- **iCloud Drive**

> *Note:* iCloud Drive support excludes iCloud Photos at this time.

## Examples

The following commands illustrate how to use the integration Rclone with Plakar for backup, restore and store:

The terme `myCloudProv` are placeholders for your configured Rclone config.

### For Sources

```bash
# Import an Rclone source
$ rclone config show | plakar source import myCloudProv

# Create a Kloset repository at a specific path
$ plakar path/to/kloset create

# Backup the source
$ plakar at path/to/kloset @myCloudProv
```

### For Destinations

```bash
# Import an Rclone destination
$ rclone config show | plakar destination import myCloudProv  

# List available snapshots to see their IDs
$ plakar at path/to/kloset ls

# Restore a snapshot to the destination
$ plakar at path/to/kloset restore -to @myCloudProv <snapshot_id>
```

### For Stores

```bash
# Import an Rclone store
$ rclone config show | plakar store import myCloudProv

# Create a Kloset repository at our store
$ plakar @myCloudProv create
```
>*Note:* This kloset repository can be used to store files, snapshots, and other data directly in the cloud provider, like a classic kloset.
