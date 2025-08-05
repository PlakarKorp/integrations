# PTAR Integration

## What is PTAR?

**PTAR (Plakar Tar Format)** is a next-generation archive format designed for modern backup, archival, and data transfer needs. Unlike legacy formats like `.tar` and `.zip`, PTAR is built for deduplication, encryption, versioning, and zero-trust environments. It encapsulates datasets into a single, self-contained, portable, immutable, deduplicated, and encrypted file.

## Installation

If a pre-built package exists for your system and architecture, install it with:

```sh
plakar pkg add ptar
```

Otherwise, build it:

```sh
plakar pkg build ptar
```

This produces `ptar-vX.Y.Z.ptar`, which you can install with:

```sh
plakar pkg add ./ptar-v0.1.0.ptar
```

## Configuration

- `location` (required): Path or URL to the PTAR archive (e.g., `/path/to/archive.ptar` or `ptar://host/archive.ptar`)
- `compression` (optional): Compression method (e.g., `gzip`, `none`)
- `encryption` (optional): Encryption settings

## Example Usage

### Create a PTAR archive

```sh
plakar ptar -o backup.ptar ~/mydata
# or for a non-encrypted archive:
plakar ptar -plaintext -o backup.ptar ~/mydata
```

### Browse archive contents (no extraction needed)

```sh
plakar at backup.ptar ls
plakar at backup.ptar ui  # Launches a local web UI
```

### Inspect a single file

```sh
plakar at backup.ptar cat <snapshotid>:path/to/file.txt
```

### Restore files from archive

```sh
plakar at backup.ptar restore -to ./recovery /etc/nginx/nginx.conf
```

### Sync into a regular Kloset

```sh
plakar at /var/backups sync from backup.ptar
```

## Real-World Use Cases

- **Air-Gapped Backups:** Store encrypted, verifiable archives offline.
- **Cold Storage:** Efficient, deduplicated, and inspectable long-term archives.
- **Disaster Recovery:** Simple restoration with no external dependencies.
- **Compliance:** Immutable, signed, and auditable archives for regulatory needs.
- **Distribution:** Securely transfer datasets across environments.

For more details, see the [official PTAR announcement](https://www.plakar.io/posts/2025-06-27/it-doesnt-make-sense-to-wrap-modern-data-in-a-1979-format-introducing-.ptar/).
