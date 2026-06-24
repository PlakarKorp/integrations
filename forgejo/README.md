# Forgejo integration for Plakar

This integration creates a Plakar snapshot from a Forgejo dump archive.

## Import

```sh
plakar at /path/to/store backup forgejo://local?forgejo_bin=/usr/local/bin/forgejo&config=/etc/forgejo/app.ini
```

The importer runs:

```sh
forgejo dump --type zip --file <temporary-directory>
```

Optional query parameters:

- `forgejo_bin`: path to the Forgejo binary. Defaults to `forgejo`.
- `config`: path to Forgejo `app.ini`.
- `work_path`: Forgejo work path.
- `timeout`: Go duration for the dump command. Defaults to `30m`.

## Export

```sh
plakar at /path/to/store restore -to forgejo://local?output_dir=/tmp/forgejo-restore
```

The exporter writes `forgejo-dump.zip` to `output_dir`. Administrators can then restore it with the Forgejo restore procedure that matches their deployment.
