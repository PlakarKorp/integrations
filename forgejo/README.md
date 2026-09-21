# Forgejo integration

[Forgejo](https://forgejo.org/) is a self-hosted software forge. This
integration backs up a Forgejo instance by invoking Forgejo's native
`forgejo dump` command and storing the generated archive in a Plakar snapshot.

The restore connector extracts the archived dump contents to a target
directory. Forgejo does not provide a single `forgejo restore` command for full
instance dumps, so the extracted directory is intended to be used with
Forgejo's documented restore flow for the selected database and storage
backend.

## Requirements

- The `forgejo` binary must be available on the host where the backup runs, or
  `forgejo_bin` must point to it.
- The backup process must run as an operating-system user that can read
  Forgejo's configured data, repositories, attachments, and database.
- For consistent production backups, follow Forgejo's operational guidance for
  your database and storage backend before running `forgejo dump`.

## Backup

```sh
plakar backup forgejo://local
```

Common options:

```sh
plakar backup forgejo://local \
  -o forgejo_bin=/usr/local/bin/forgejo \
  -o work_path=/var/lib/forgejo \
  -o config_path=/etc/forgejo/app.ini \
  -o custom_path=/var/lib/forgejo/custom
```

The importer defaults to a gzip-compressed tar stream and stores it as
`/forgejo-dump.tar.gz` inside the Plakar snapshot:

```sh
forgejo dump --file - --type tar.gz --quiet
```

Additional backup options:

| Option | Description |
| --- | --- |
| `forgejo_bin` | Path to the Forgejo binary. Defaults to `forgejo`. |
| `work_path` | Value passed to `--work-path`. |
| `custom_path` | Value passed to `--custom-path`. |
| `config_path` | Value passed to `--config`. |
| `temp_dir` | Value passed to `--tempdir`. |
| `database` | Value passed to `--database` when Forgejo cannot infer the database syntax. |
| `dump_type` | Forgejo dump output type. Defaults to `tar.gz`. Supported restore extraction types are `tar`, `tar.gz`, `tgz`, and `zip`. |
| `skip_repository` | Pass `--skip-repository`. |
| `skip_log` | Pass `--skip-log`. |
| `skip_custom_dir` | Pass `--skip-custom-dir`. |
| `skip_lfs_data` | Pass `--skip-lfs-data`. |
| `skip_attachment_data` | Pass `--skip-attachment-data`. |
| `skip_package_data` | Pass `--skip-package-data`. |
| `skip_index` | Pass `--skip-index`. |
| `skip_repo_archives` | Pass `--skip-repo-archives`. |

## Restore

Restore the snapshot to a directory:

```sh
plakar restore -to forgejo:///tmp/forgejo-restore <snapshot-id>
```

or pass an explicit target directory:

```sh
plakar restore -to forgejo://local -o target_dir=/tmp/forgejo-restore <snapshot-id>
```

The exporter creates the target directory if it does not exist and extracts the
Forgejo dump archive into it. It rejects archive entries that would escape the
target directory.
