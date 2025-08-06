# Filesystem (FS) Integration

## Overview

**FS (Filesystem)** integration allows Plakar to interact directly with local or mounted filesystems. This enables seamless backup and restoration of files and directories from your local environment or any accessible filesystem.

This integration allows:

- Seamless backup of files and directories from local or mounted filesystems into a Kloset repository
- Direct restoration of snapshots to local or mounted filesystem destinations
- Compatibility with a wide range of filesystems supported by your OS

## Configuration

The configuration parameters are as follows:

- `location` (required): The path to the directory or mount point (e.g., `/home/user/data`)

> **Note:** With the FS integration, you can specify file or directory paths directly in your commands, no need for a protocol prefix like `fs://`. Local filesystem paths are handled automatically.

## Examples

```bash
# backup a directory to a Kloset repository
$ plakar at /tmp/store backup /tmp/example_directory

# restore a snapshot to a local directory
$ plakar at /tmp/store restore -to /tmp/restore_directory <snapid>

# create a new Kloset store
$ plakar at /tmp/store create
```
