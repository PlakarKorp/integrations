# Forgejo Integration

## Overview

This integration backs up Forgejo instances by invoking Forgejo's built-in
[`forgejo dump`](https://forgejo.org/docs/latest/admin/command-line/#dump)
command and storing the resulting archive in a Plakar snapshot.

The snapshot contains:

- `/forgejo-dump.<type>`: the archive created by `forgejo dump`
- `/manifest.json`: metadata about the dump command, archive format, and paths

The exporter restores the snapshot content to a local directory, producing a
Forgejo-loadable dump archive that can be used with the standard Forgejo restore
workflow.

## Prerequisites

The machine running Plakar must have the `forgejo` binary in `$PATH`, or the
`binary` option must point to the Forgejo executable.

The Forgejo CLI documentation states that `forgejo dump` compresses Forgejo
files and the database into an archive and supports formats including `zip`,
`tar`, `tar.gz`, and others. The integration defaults to `zip`.

## Importer options

| Parameter | Default | Description |
|---|---|---|
| `location` | - | Optional `forgejo://` URI. A path component is treated as `work_path`. |
| `binary` | `forgejo` | Path to the Forgejo executable. |
| `work_path` | - | Forgejo work path, passed as `--work-path`. |
| `config` | - | Forgejo config path, passed as `--config`. |
| `custom_path` | - | Forgejo custom path, passed as `--custom-path`. |
| `tempdir` | OS default | Temporary directory for `forgejo dump`. |
| `dump_type` | `zip` | Archive type passed to `forgejo dump --type`. |

## Exporter options

| Parameter | Default | Description |
|---|---|---|
| `location` | - | `forgejo:///path/to/restore-dir`; snapshot files are written below this directory. |
| `output_dir` | - | Explicit output directory. Overrides the path in `location`. |

## Examples

Back up a Forgejo instance using its configured working path:

```sh
plakar source add myforgejo forgejo:///var/lib/forgejo \
    config=/etc/forgejo/app.ini
plakar backup @myforgejo
```

Restore the dump archive into a local directory:

```sh
plakar destination add forgejo-restore forgejo:///tmp/forgejo-restore
plakar restore -to @forgejo-restore <snapshot-id>
```

The restored directory will contain `forgejo-dump.zip` and `manifest.json`.
Use the Forgejo backup and restore documentation for the final application-level
restore steps, such as unpacking the archive, placing repositories and custom
files, and loading the database dump.
