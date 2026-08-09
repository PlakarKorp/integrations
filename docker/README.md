# Docker Integration

## Overview

**TAR (Tape Archive)** is a widely-used archive format for bundling multiple files and directories into a single stream or file.
It is commonly used in Unix-like systems for packaging, distribution, and backups.

This integration allows:

* **Importing TAR archives into a Kloset repository:**
  Stream or extract `.tar` or `.tar.gz` archives into the virtual filesystem of a Kloset snapshot.
  File contents, directory structure, and metadata are preserved during import.

* **Support for standard and compressed TAR formats:**
  Compatible with uncompressed `.tar` files as well as gzip-compressed `.tar.gz` and `.tgz` archives.

* **Ideal for ingesting legacy backups and exported data:**
  Useful for importing existing TAR-based backups or exported application data into Kloset for versioned tracking, deduplication, and storage unification.

## Configuration

The configuration parameters are as follow:

- `location` (required): Path to the TAR archive file

> **Note:** The location can be write directly in the command, with `tar://` prefix. No need to add it in the configuration.

## Examples

```sh
# backup a tarball into the current Kloset repository
$ plakar backup tar:/home/user/backup.tar.gz
```
