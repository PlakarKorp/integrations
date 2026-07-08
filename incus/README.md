# Plakar Incus Integration

Backup and restore Incus containers as browsable file-level snapshots.

## Importer

Import filesystem contents from Incus containers for backup:

```bash
plakar backup incus://web-1
```

## Exporter

Restore backups to new or existing Incus containers:

```bash
plakar restore -to incus://web-1-restored <snap>
```

## Notes

This integration backs up containers only and runs on the Incus host using the local unix socket.
