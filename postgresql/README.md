# PostgreSQL Integration

## Overview

This integration provides two independent backup strategies for PostgreSQL:

| Protocol | Tool | Granularity | Restore requires |
|---|---|---|---|
| `postgres://` | `pg_dump` / `pg_dumpall` | logical (SQL) | a running PostgreSQL server |
| `postgres+bin://` | `pg_basebackup` | physical (binary) | any file-restore connector |

---

## Logical backup — `postgres://`

### How it works

The importer connects to a running PostgreSQL server and produces a logical
dump — a portable, version-independent representation of the data.

- **Single database** (`database` parameter or `/dbname` in the URI):
  runs `pg_dump -Fc` (custom binary format) and stores **two records**
  in the snapshot:
  - `/globals.sql` — a `pg_dumpall --globals-only` dump containing the
    roles and tablespaces of the whole cluster.
  - `/<dbname>.dump` — the database itself in pg_dump custom format.

- **All databases** (no `database` parameter):
  runs `pg_dumpall` (plain SQL format) and stores **one record**
  named `/all.sql` in the snapshot.  This already includes global objects
  such as roles and tablespaces.

Restore dispatches on file name and extension: `.dump` files are fed to
`pg_restore`; `globals.sql` is fed to `psql` only when `restore_globals`
is enabled; `all.sql` is always fed to `psql`.

### Pros and cons

**Pros**
- Works across PostgreSQL major versions — dump on PG 15, restore on PG 16.
- Selectively back up individual databases or the whole cluster.
- The custom format (`-Fc`) supports selective table/schema restore with
  `pg_restore -t`.
- No server downtime required.

**Cons**
- Requires a live, accessible PostgreSQL server for both backup and restore.
- Logical dumps do not capture low-level objects (e.g. unlogged tables'
  content, certain system catalog details).
- Restore time scales with data volume — large databases take longer than
  a binary restore.

### Prerequisites

The following PostgreSQL client tools must be in `$PATH` on the machine
running Plakar (typically provided by the `postgresql-client` package):

- `pg_dump` — single-database backup
- `pg_dumpall` — full-server backup
- `pg_restore` — single-database restore
- `psql` — full-server restore and connectivity checks

### Importer options (`postgres://`)

| Parameter | Default | Description |
|---|---|---|
| `location` | — | Connection URI: `postgres://[user[:password]@]host[:port][/database]` |
| `host` | `localhost` | Server hostname. Overrides the URI host. |
| `port` | `5432` | Server port. Overrides the URI port. |
| `username` | — | PostgreSQL username. Overrides the URI user. |
| `password` | — | PostgreSQL password. Overrides the URI password. |
| `database` | — | Database to back up. If omitted, all databases are backed up via `pg_dumpall`. Overrides the URI path. When set, a globals dump (`/globals.sql`) is also produced automatically. |
| `compress` | `false` | Enable `pg_dump` compression. By default dumps are stored uncompressed so that Plakar's own compression is not degraded. |
| `schema_only` | `false` | Dump only the schema (no data). Mutually exclusive with `data_only`. |
| `data_only` | `false` | Dump only the data (no schema). Mutually exclusive with `schema_only`. |
| `pg_dump` | `pg_dump` | Path to the `pg_dump` binary. |
| `pg_dumpall` | `pg_dumpall` | Path to the `pg_dumpall` binary. |
| `psql` | `psql` | Path to the `psql` binary. Used for connectivity checks and server version queries. |
| `ssl_mode` | — | SSL mode: `disable`, `allow`, `prefer`, `require`, `verify-ca`, or `verify-full`. Passed via `PGSSLMODE`. |
| `ssl_cert` | — | Path to the client SSL certificate file (PEM). Passed via `PGSSLCERT`. |
| `ssl_key` | — | Path to the client SSL private key file (PEM). Passed via `PGSSLKEY`. |
| `ssl_root_cert` | — | Path to the root CA certificate used to verify the server (PEM). Passed via `PGSSLROOTCERT`. |

### Exporter options (`postgres://`)

| Parameter | Default | Description |
|---|---|---|
| `location` | — | Connection URI: `postgres://[user[:password]@]host[:port][/database]` |
| `host` | `localhost` | Server hostname. Overrides the URI host. |
| `port` | `5432` | Server port. Overrides the URI port. |
| `username` | — | PostgreSQL username. Overrides the URI user. |
| `password` | — | PostgreSQL password. Overrides the URI password. |
| `database` | — | Controls the `-d` argument passed to `pg_restore` or `psql` (see `create_db` below). If omitted, the name is inferred from the dump filename (e.g. `myapp.dump` → `myapp`). |
| `create_db` | `false` | When `false` (default), `-d` names the **target database**, which must already exist. When `true`, passes `-C` to `pg_restore`: the database is created from the metadata embedded in the archive, and `-d` names only the **initial connection database** (defaults to `postgres` if unset). Use `true` for a fresh restore onto a server that does not yet have the target database. |
| `restore_globals` | `false` | When `true`, feeds `/globals.sql` to `psql` before restoring the database dump, recreating roles and tablespaces on the target server. Useful when restoring to a different server where the source roles do not exist and `no_owner` is not sufficient — for example, when the database uses row-level security, relies on specific role memberships, or references tablespaces that must exist before the data is loaded. Not needed for `pg_dumpall` restores (`all.sql`), which already contain global objects. |
| `no_owner` | `false` | Pass `--no-owner` to `pg_restore`, skipping `ALTER OWNER` statements. Useful when roles from the source server do not exist on the target. |
| `schema_only` | `false` | Restore only the schema (no data). Mutually exclusive with `data_only`. Not applicable to `pg_dumpall` restores. |
| `data_only` | `false` | Restore only the data (no schema). Mutually exclusive with `schema_only`. Not applicable to `pg_dumpall` restores. |
| `exit_on_error` | `false` | Stop on the first restore error. Applies to both `pg_restore` (`-e`) and `psql` (`ON_ERROR_STOP=1`). |
| `ssl_mode` | — | SSL mode: `disable`, `allow`, `prefer`, `require`, `verify-ca`, or `verify-full`. Passed via `PGSSLMODE`. |
| `ssl_cert` | — | Path to the client SSL certificate file (PEM). Passed via `PGSSLCERT`. |
| `ssl_key` | — | Path to the client SSL private key file (PEM). Passed via `PGSSLKEY`. |
| `ssl_root_cert` | — | Path to the root CA certificate used to verify the server (PEM). Passed via `PGSSLROOTCERT`. |

### Examples

```bash
# Back up a single database
plakar source add mypg postgres://postgres:secret@db.example.com/myapp
plakar backup @mypg

# Back up all databases (roles, tablespaces, and data)
plakar source add mypg postgres://postgres:secret@db.example.com/
plakar backup @mypg

# Restore a single database (created automatically if absent)
plakar destination add mypgdst postgres://postgres:secret@db.example.com/myapp
plakar restore -to @mypgdst <snapid>

# Restore all databases to a fresh server
plakar destination add mypgdst postgres://postgres:secret@db.example.com/
plakar restore -to @mypgdst <snapid>

# Restore, skipping owner assignment (e.g. roles differ on target)
plakar destination add mypgdst postgres://postgres:secret@db.example.com/myapp \
    no_owner=true
plakar restore -to @mypgdst <snapid>
```

---

## Physical backup — `postgres+bin://`

### How it works

The importer runs `pg_basebackup -D - -F tar -X fetch`, which streams a
tar archive of the entire PostgreSQL data directory (`PGDATA`) directly
from the server's replication interface.  Each file inside the tar is
stored as an individual record in the snapshot, preserving paths,
permissions, and timestamps.

Because the backup is at the file level, it captures the **whole cluster**
— all databases, configuration files, and WAL segments needed for a
consistent recovery.

No subpath can be specified in the URI: `pg_basebackup` always backs up
the entire cluster.

### Pros and cons

**Pros**
- Fast backup and restore regardless of data volume — it is a file copy,
  not a query.
- Captures everything: all databases, configuration, global objects, and
  WAL in a single operation.
- The restored cluster can be started directly with any PostgreSQL binary
  of the **same major version** — no running server needed at restore time.


**Cons**
- Version-locked: the backup must be restored with the same PostgreSQL
  major version.
- Cannot selectively restore a single database or table.
- Requires a replication-capable user (or superuser) and
  `wal_level >= replica` on the server.
- The PostgreSQL server must be **stopped** before the data directory is
  replaced with the restored files.

### Prerequisites

`pg_basebackup` must be in `$PATH` on the machine running Plakar
(same `postgresql-client` package, or `postgresql` on macOS via Homebrew).

The PostgreSQL server must have:
- `wal_level = replica` (or higher) in `postgresql.conf`
- A user with the `REPLICATION` privilege (or a superuser)
- `pg_hba.conf` allowing a replication connection from the backup host

### Importer options (`postgres+bin://`)

| Parameter | Default | Description |
|---|---|---|
| `location` | — | Connection URI: `postgres+bin://[user[:password]@]host[:port]`. A subpath is not allowed. |
| `host` | `localhost` | Server hostname. Overrides the URI host. |
| `port` | `5432` | Server port. Overrides the URI port. |
| `username` | — | PostgreSQL replication username. Overrides the URI user. |
| `password` | — | PostgreSQL password. Overrides the URI password. |
| `pg_basebackup` | `pg_basebackup` | Path to the `pg_basebackup` binary. |
| `ssl_mode` | — | SSL mode: `disable`, `allow`, `prefer`, `require`, `verify-ca`, or `verify-full`. Passed via `PGSSLMODE`. |
| `ssl_cert` | — | Path to the client SSL certificate file (PEM). Passed via `PGSSLCERT`. |
| `ssl_key` | — | Path to the client SSL private key file (PEM). Passed via `PGSSLKEY`. |
| `ssl_root_cert` | — | Path to the root CA certificate used to verify the server (PEM). Passed via `PGSSLROOTCERT`. |

### Restoring a physical backup

There is no dedicated exporter for `postgres+bin://`.  Because the snapshot
contains plain files, any file-restore connector can be used to write them
back to disk.  The restored directory is a valid PostgreSQL data directory
that can be started directly.

```bash
# Restore the data directory to ./pgdata using the built-in fs connector
plakar restore -to ./pgdata <snapid>

# Start PostgreSQL against the restored data directory
docker run --rm \
    -v "$PWD/pgdata:/var/lib/postgresql/data" \
    postgres:<postgres_version>
```

Replace `<postgres_version>` with the **same major version** that was
running when the backup was taken (e.g. `17`).

> **Important:** the data directory must not be in use by a running
> PostgreSQL instance before restoration begins.  Stop the server first,
> or restore to a fresh directory as shown above.

### Examples

```bash
# Back up the entire cluster
plakar source add mypg postgres+bin://replicator:secret@db.example.com
plakar backup @mypg

# Restore to a local directory and start with Docker
plakar restore -to ./pgdata <snapid>
docker run --rm -v "$PWD/pgdata:/var/lib/postgresql/data" postgres:17

# Restore to a remote host via SFTP
plakar restore -to sftp://user@host/var/lib/postgresql/data <snapid>
# then on the remote host: pg_ctl -D /var/lib/postgresql/data start
```

---

## Manifest

Every snapshot written by this integration contains a `/manifest.json` record
(schema version 2) that is written **before** the backup data begins.  It
captures the cluster state at the moment the backup was initiated.

Top-level fields include the server version, host, port, `pg_dump_version`
(logical backups only), `cluster_system_identifier` (unique cluster ID from
`pg_control_system()`), `in_recovery` (true when the backup was taken from a
hot standby), target database, dump format, and backup options.

| Field | Description |
|---|---|
| `cluster_config` | Key server GUCs: `data_directory`, `timezone`, `max_connections`, `wal_level`, `server_encoding`, `data_checksums`, `block_size`, `wal_block_size`, `shared_preload_libraries`, `lc_collate`, `lc_ctype`, `archive_mode`, `archive_command`. |
| `roles` | All PostgreSQL roles with `superuser`, `replication`, `can_login`, `create_db`, `create_role`, `inherit`, `bypass_rls`, `connection_limit`, `valid_until`, and `member_of` (list of parent roles). |
| `tablespaces` | All tablespaces with name, owner, filesystem location, size in bytes, and storage options. |
| `databases` | One entry per database: name, owner, encoding, collation, size, `default_tablespace`, `connection_limit`, installed extensions (name, version, schema), schemas (name, owner), and a `relations` array. |

The `relations` array covers ordinary tables, partitioned tables, foreign
tables, views, materialized views, and sequences.  Each entry includes:
schema, name, owner, persistence, kind, tablespace, row estimates
(`row_estimate` from `reltuples`, `live_row_estimate` from autovacuum), size
breakdown (`total_size_bytes` / `table_size_bytes` / `index_size_bytes` /
`toast_size_bytes`), column count, primary-key and trigger flags, row-level
security flags, partitioning metadata (`is_partition`, `partition_parent`,
`partition_strategy`), storage options (reloptions), last vacuum and analyze
timestamps, `columns` (position, name, type, nullable, default), `constraints`
(name, type code, column names), and `indexes` (name, method, definition,
`is_unique`, `is_primary`, `is_valid`, `is_partial`, `constraint_name`, size).

Row counts are estimates from `reltuples` (`pg_class`) and `n_live_tup`
(`pg_stat_user_tables`), not exact values.

**Note:** physical backups (`postgres+bin://`) include cluster-level metadata
(config, roles, tablespaces, and a basic database list) but **not**
per-database relation detail (schemas, tables, columns, indexes), since the
physical backup already captures every database at the file level.

Metadata collection is best-effort: if a query fails the affected field is
omitted and the backup continues normally.

---

## Development

### Building

```bash
make build
```

### Installing into Plakar

```bash
make reinstall                            # build, package, uninstall old, install
make reinstall PLAKAR=~/dev/plakar/plakar # use a development build of Plakar
make reinstall VERSION=v1.2.0            # override the package version
```

`reinstall` is the usual iteration loop: it removes the previously installed
package (if any), rebuilds the binaries, creates a fresh `.ptar`, and installs
it.  The individual steps are also available as separate targets:

| Target | Description |
|---|---|
| `make build` | Compile the three plugin binaries |
| `make package` | Build then create the `.ptar` package |
| `make install` | Package then install into Plakar |
| `make uninstall` | Remove the installed `postgresql` package |
| `make reinstall` | `uninstall` + `install` in one step |

### Testing restores with a local database

`make testdb` starts a throw-away PostgreSQL container on port 9999 and
prints the commands needed to restore a snapshot to it:

```bash
make testdb
```

The container runs in the foreground.  In a second terminal, use the printed
commands to configure the destination and trigger a restore:

```bash
plakar destination rm mydb
plakar destination add mydb postgres://postgres@localhost:9999 password=postgres
plakar restore -to @mydb <snapid>
```

---

## Open questions

### Compression default

`pg_dump` compression is currently **disabled by default** (`compress=false`)
so that Plakar's own compression and deduplication can operate on
uncompressed data.  Pre-compressing the dump would produce an
incompressible stream, defeating both Plakar's compression ratio and
its ability to deduplicate chunks across snapshots.

However, the trade-off is not clear-cut.  Depending on the size of the
database, the network bandwidth between the backup host and the
PostgreSQL server, and the Plakar store backend in use, it may be more
efficient to let `pg_dump` compress the stream and simply store it
opaquely.  More benchmarks across a variety of workloads are needed
before settling on a final default.

This is something to revisit after more testing.

---

> This integration was mostly vibe-coded with [Claude](https://claude.ai) but has been manually tested and reviewed throughout.
