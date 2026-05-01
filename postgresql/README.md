# PostgreSQL Integration

## Overview

This integration provides two independent backup strategies for PostgreSQL:

| Protocol | Tool | Granularity | Restore requires |
|---|---|---|---|
| `postgres://` | `pg_dump` / `pg_dumpall` | logical (SQL) | a running PostgreSQL server |
| `postgres+aws://` | `pg_dump` / `pg_dumpall` + AWS IAM token | logical (SQL) | a running PostgreSQL server |
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

In both cases a `/manifest.json` record is also written to the snapshot
before the dump data, containing cluster-level metadata (roles, tablespaces,
database list, schema and relation details).  See the [Manifest](#manifest)
section below for details.

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

The backup user should be a **superuser** so that `pg_dumpall` can include
role passwords from `pg_authid`.  On managed services such as Amazon RDS the
administrative user (e.g. `postgres`) is a *restricted* superuser that cannot
read `pg_authid`.  In that case the importer automatically falls back to
`--no-role-passwords` and logs a warning — the dump is otherwise complete, but
restored roles will have no password set.

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
| `pg_bin_dir` | — | Directory containing the PostgreSQL client binaries (`pg_dump`, `pg_dumpall`, `psql`). When omitted, binaries are resolved via `$PATH`. Useful when multiple PostgreSQL versions are installed. |
| `ssl_mode` | `prefer` | SSL mode: `disable`, `allow`, `prefer`, `require`, `verify-ca`, or `verify-full`. Passed via `PGSSLMODE`. |
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
| `pg_bin_dir` | — | Directory containing the PostgreSQL client binaries (`pg_restore`, `psql`). When omitted, binaries are resolved via `$PATH`. Useful when multiple PostgreSQL versions are installed. |
| `ssl_mode` | `prefer` | SSL mode: `disable`, `allow`, `prefer`, `require`, `verify-ca`, or `verify-full`. Passed via `PGSSLMODE`. |
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

## Logical backup with AWS IAM authentication — `postgres+aws://`

### How it works

`postgres+aws://` performs the same logical backup as `postgres://` but
authenticates using a short-lived IAM token instead of a static password.
The token is generated in-process via the AWS SDK before `pg_dump` /
`pg_dumpall` runs, using the standard SDK credential chain (environment
variables, `~/.aws/credentials`, EC2/ECS instance metadata, etc.).  No
`password` parameter is needed (or accepted) in the configuration.

The backup output is identical to `postgres://` and can be restored with the
same `postgres://` exporter.

### AWS setup

#### 1. Enable IAM authentication on the RDS instance

In the AWS console (or via CLI/Terraform), enable **IAM database
authentication** on the RDS instance.  Without this, the token is
accepted by the SDK but rejected by RDS.

#### 2. Create an IAM policy with `rds-db:connect`

Attach the following policy to the IAM user or role that will run Plakar:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "rds-db:connect",
      "Resource": "arn:aws:rds-db:REGION:ACCOUNT_ID:dbuser:DB_RESOURCE_ID/DB_USER"
    }
  ]
}
```

- `REGION` — e.g. `eu-west-3`
- `ACCOUNT_ID` — your 12-digit AWS account ID
- `DB_RESOURCE_ID` — the resource ID of the RDS instance (e.g. `db-ABCDEFGHIJKL1234`), found in the RDS console under **Configuration**
- `DB_USER` — the PostgreSQL username Plakar will connect as

To allow any user on any instance in the account, use `"Resource": "*"`.
This is convenient for testing but too broad for production.

#### 3. Create the PostgreSQL user with IAM login

Connect to the database as a superuser and run:

```sql
CREATE USER myuser WITH LOGIN;
GRANT rds_iam TO myuser;
```

`rds_iam` is a special RDS role that marks the user as IAM-authenticated.
The user must not have a password set — authentication is handled entirely
via the IAM token.

### Providing credentials

#### Option A — Environment variables (local testing)

Export the credentials before running Plakar:

```bash
export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE
export AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
export AWS_REGION=eu-west-3   # or set region= in the source config

plakar source add myrds postgres+aws://myuser@mydb.cluster-xyz.eu-west-3.rds.amazonaws.com/myapp \
    region=eu-west-3 ssl_mode=require
plakar source ping myrds
plakar backup @myrds
```

If your IAM user requires a session token (assumed role, temporary
credentials from `aws sts assume-role`, etc.):

```bash
export AWS_SESSION_TOKEN=AQoDYXdzEJr...
```

#### Option B — `~/.aws` profile (local testing)

```ini
# ~/.aws/credentials
[default]
aws_access_key_id     = AKIAIOSFODNN7EXAMPLE
aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
```

```ini
# ~/.aws/config
[default]
region = eu-west-3
```

Then run Plakar without any environment variables — the SDK picks up the
profile automatically.  To use a named profile instead of `default`:

```bash
export AWS_PROFILE=myprofile
plakar backup @myrds
```

#### Option C — EC2 instance profile (production)

Attach an IAM role with the `rds-db:connect` policy to the EC2 instance
that runs Plakar.  No credentials need to be configured anywhere — the
SDK automatically retrieves short-lived credentials from the instance
metadata service (IMDS).

```bash
# No AWS_* variables needed on the instance.
plakar source add myrds postgres+aws://myuser@mydb.cluster-xyz.eu-west-3.rds.amazonaws.com/myapp \
    region=eu-west-3 ssl_mode=require
plakar backup @myrds
```

The same approach works for **ECS tasks** (task IAM role) and **Lambda**
functions (execution role).

### Importer options (`postgres+aws://`)

| Parameter | Default | Description |
|---|---|---|
| `location` | — | Connection URI: `postgres+aws://[user@]host[:port][/database]`. No password component — the IAM token is fetched automatically. |
| `host` | `localhost` | RDS instance hostname. Overrides the URI host. |
| `port` | `5432` | RDS instance port. Overrides the URI port. |
| `username` | — | PostgreSQL username (required). Must be an IAM-enabled database user. Overrides the URI user. |
| `region` | — | AWS region of the RDS instance (required), e.g. `us-east-1`. |
| `database` | — | Database to back up. If omitted, all databases are backed up via `pg_dumpall`. Overrides the URI path. |
| `compress` | `false` | Enable `pg_dump` compression. By default dumps are stored uncompressed so that Plakar's own compression is not degraded. |
| `schema_only` | `false` | Dump only the schema (no data). Mutually exclusive with `data_only`. |
| `data_only` | `false` | Dump only the data (no schema). Mutually exclusive with `schema_only`. |
| `pg_bin_dir` | — | Directory containing the PostgreSQL client binaries. When omitted, binaries are resolved via `$PATH`. |
| `ssl_mode` | `prefer` | SSL mode. IAM authentication requires an encrypted connection — use `require` or higher. |
| `ssl_cert` | — | Path to the client SSL certificate file (PEM). Passed via `PGSSLCERT`. |
| `ssl_key` | — | Path to the client SSL private key file (PEM). Passed via `PGSSLKEY`. |
| `ssl_root_cert` | — | Path to the root CA certificate used to verify the server (PEM). Passed via `PGSSLROOTCERT`. |

### Examples

```bash
# Back up a single RDS database using IAM authentication
plakar source add myrds postgres+aws://myuser@mydb.cluster-xyz.us-east-1.rds.amazonaws.com/myapp \
    region=us-east-1 ssl_mode=require
plakar backup @myrds

# Back up all databases on an RDS instance
plakar source add myrds postgres+aws://myuser@mydb.cluster-xyz.us-east-1.rds.amazonaws.com/ \
    region=us-east-1 ssl_mode=require
plakar backup @myrds
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

A `/manifest.json` record is also written to the snapshot before the backup
data, containing cluster-level metadata.  See the [Manifest](#manifest)
section below for details.

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
| `pg_bin_dir` | — | Directory containing the PostgreSQL client binaries (`pg_basebackup`, `psql`). When omitted, binaries are resolved via `$PATH`. Useful when multiple PostgreSQL versions are installed. |
| `ssl_mode` | `prefer` | SSL mode: `disable`, `allow`, `prefer`, `require`, `verify-ca`, or `verify-full`. Passed via `PGSSLMODE`. |
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
| `cluster_config` | Key server GUCs: `data_directory`, `timezone`, `max_connections`, `wal_level`, `server_encoding`, `data_checksums`, `block_size`, `wal_block_size`, `shared_preload_libraries`, `lc_collate`, `lc_ctype`, `archive_mode`, `archive_command_set` (boolean — the command itself is not stored to avoid leaking credentials). |
| `roles` | All PostgreSQL roles with `superuser`, `replication`, `can_login`, `create_db`, `create_role`, `inherit`, `bypass_rls`, `connection_limit`, `valid_until`, and `member_of` (list of parent roles). |
| `tablespaces` | All tablespaces with name, owner, filesystem location, and storage options. |
| `databases` | One entry per database: name, owner, encoding, collation, `default_tablespace`, `connection_limit`, installed extensions (name, version, schema), schemas (name, owner), and a `relations` array. |

The `relations` array covers ordinary tables, partitioned tables, foreign
tables, views, materialized views, and sequences.  Each entry includes:
schema, name, owner, persistence, kind, tablespace, row estimates
(`row_estimate` from `reltuples`, `live_row_estimate` from autovacuum),
column count, primary-key and trigger flags, row-level security flags,
partitioning metadata (`is_partition`, `partition_parent`,
`partition_strategy`), storage options (reloptions), last vacuum and analyze
timestamps, `columns` (position, name, type, nullable, default), `constraints`
(name, type code, column names), and `indexes` (name, method, definition,
`is_unique`, `is_primary`, `is_valid`, `is_partial`, `constraint_name`).

Row counts are estimates from `reltuples` (`pg_class`) and `n_live_tup`
(`pg_stat_user_tables`), not exact values.

Both `postgres://` and `postgres+bin://` produce the full manifest including
per-database relation detail.  The manifest also records `pg_dump_version`
(logical backups) or `pg_basebackup_version` (physical backups).

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

### Integration tests

The test suite uses [testcontainers-go](https://golang.testcontainers.org) to spin up real Docker containers — no mocks.  Requires Docker to be running.

```bash
make integration-test
```

---

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
