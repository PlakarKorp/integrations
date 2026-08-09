# PTAR Integration

## Overview

**PTAR (Plakar Tar Format)** is a next-generation archive format designed for modern backup, archival, and data transfer needs. Unlike legacy formats like `.tar` and `.zip`, PTAR is built for deduplication, encryption, versioning, and zero-trust environments. It encapsulates datasets into a single, self-contained, portable, immutable, deduplicated, and encrypted file.

## Configuration

- `location` (required): Path to the futur PTAR archive (e.g., `ptar://tmp/archive.ptar`)

> **Note:** The location can be write directly in the command, with `ptar://` prefix. No need to add it in the configuration.

## Example Usage

```bash
# create the kloset store
$ plakar at ptar:///tmp/myPTARstore create

# backup in the store
$ plakar at ptar:///tmp/myPTARstore backup /tmp/example.txt
```
