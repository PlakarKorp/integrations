# Filesystem (FS) Integration

## What is FS?

**FS (Filesystem)** integration allows Plakar to interact directly with local or mounted filesystems. This enables seamless backup and restoration of files and directories from your local environment or any accessible filesystem.

This integration allows:

- Seamless backup of files and directories from local or mounted filesystems into a Kloset repository
- Direct restoration of snapshots to local or mounted filesystem destinations
- Compatibility with a wide range of filesystems supported by your OS

## Installation

If a pre-built package exists for your system and architecture,
you can simply install it using:

```sh
$ plakar pkg add fis
```

Otherwise,
you can first build it:

```sh
$ plakar pkg build fis
```

This should produce `fis-vX.Y.Z.ptar` that can be installed with:

```bash
$ plakar pkg add ./fis-v0.1.0.ptar
```

## Configuration

The configuration parameters are as follows:

- `location` (required): The path to the directory or mount point (e.g., `/home/user/data`)

## Example Usage

```bash
# configure an FS source
$ plakar source add myFSsrc fis:///home/user/documents

# backup the source
$ plakar backup @myFSsrc

# configure an FS destination
$ plakar destination add myFSdst fis:///mnt/backup

# restore the snapshot to the destination
$ plakar restore -to @myFSdst <snapid>
```

[plakar]: https://github.com/PlakarKorp/plakar
