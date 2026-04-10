# MySQL / MariaDB Integration for Plakar

Logical backup and restore of MySQL and MariaDB databases using [Plakar](https://github.com/PlakarKorp/plakar).

Backups are performed with `mysqldump` (MySQL) or `mariadb-dump` (MariaDB) and
produce standard SQL files. Restores are performed with the `mysql` or `mariadb`
CLI. Because the dump format is plain SQL, the backup is fully human-readable
and can be restored without Plakar if needed.

## Protocols

| Protocol          | Target           | Dump tool       | Client tool |
|-------------------|------------------|-----------------|-------------|
| `mysql://`        | MySQL 5.7 / 8.x  | `mysqldump`     | `mysql`     |
| `mysql+mariadb://`| MariaDB 10.x / 11.x | `mariadb-dump` | `mariadb`  |

Use the protocol that matches your server. The two connectors are independent:
`mysql://` always uses MySQL binaries; `mysql+mariadb://` always uses MariaDB
binaries.

## Prerequisites

**MySQL**
- Plakar ≥ 1.1
- `mysqldump` and `mysql` CLI tools in `$PATH` (or specify `mysql_bin_dir`)
- MySQL 5.7 or MySQL 8.x server

**MariaDB**
- Plakar ≥ 1.1
- `mariadb-dump` and `mariadb` CLI tools in `$PATH` (or specify `mariadb_bin_dir`)
- MariaDB 10.x or 11.x server

A database user with sufficient privileges is required for both (see [Required Privileges](#required-privileges)).

## Installation

```sh
plakar pkg add mysql_v1.0.0_linux_amd64.ptar
```

Or build from source:

```sh
git clone https://github.com/PlakarKorp/integration-mysql
cd integration-mysql
make install
```

## Quick Start

### Back up a single database

```sh
# MySQL
plakar source add mydb mysql://dbuser:secret@db.example.com/mydb
plakar backup @mydb

# MariaDB
plakar source add mydb mysql+mariadb://dbuser:secret@db.example.com/mydb
plakar backup @mydb
```

### Back up all databases

Omit the database path from the URI to use `--all-databases`:

```sh
# MySQL
plakar source add alldb mysql://root:secret@db.example.com
plakar backup @alldb

# MariaDB
plakar source add alldb mysql+mariadb://root:secret@db.example.com
plakar backup @alldb
```

### Restore a single database

```sh
# Restore into an existing (empty) database
plakar restore -to mysql://dbuser:secret@target.example.com/mydb <snapshot-id>

# Restore and create the database first
plakar restore -to "mysql://dbuser:secret@target.example.com/mydb create_db=true" <snapshot-id>

# MariaDB
plakar restore -to "mysql+mariadb://dbuser:secret@target.example.com/mydb create_db=true" <snapshot-id>
```

### Restore all databases

```sh
plakar restore -to mysql://root:secret@target.example.com <snapshot-id>
plakar restore -to mysql+mariadb://root:secret@target.example.com <snapshot-id>
```

## Connection URI

**MySQL**
```
mysql://[user[:password]@]host[:port][/database]
```

**MariaDB**
```
mysql+mariadb://[user[:password]@]host[:port][/database]
```

| Component  | Default     | Description                                        |
|------------|-------------|----------------------------------------------------|
| `user`     | —           | Username                                           |
| `password` | —           | Password (never passed on the command line)        |
| `host`     | `127.0.0.1` | Server hostname or IP address                      |
| `port`     | `3306`      | Server port                                        |
| `database` | —           | Target database (omit for `--all-databases` backup)|

Individual keys (`host`, `port`, `username`, `password`) always override the
corresponding URI component when both are provided.

## Importer Options

### Common options (MySQL and MariaDB)

| Option               | Type    | Default  | Description |
|----------------------|---------|----------|-------------|
| `location`           | string  | —        | Connection URI |
| `host`               | string  | `127.0.0.1` | Server hostname (overrides URI) |
| `port`               | string  | `3306`   | Server port (overrides URI) |
| `username`           | string  | —        | Username (overrides URI) |
| `password`           | string  | —        | Password (overrides URI, passed via `MYSQL_PWD`) |
| `database`           | string  | —        | Database to back up (overrides URI path) |
| `single_transaction` | boolean | `true`   | Use `--single-transaction` for a lock-free InnoDB snapshot |
| `routines`           | boolean | `true`   | Include stored procedures and functions (`--routines`) |
| `events`             | boolean | `true`   | Include event scheduler events (`--events`) |
| `triggers`           | boolean | `true`   | Include triggers (set `false` for `--skip-triggers`) |
| `no_data`            | boolean | `false`  | Dump DDL only, no data (`--no-data`) |
| `no_create_info`     | boolean | `false`  | Dump data only, no DDL (`--no-create-info`) |
| `no_tablespaces`     | boolean | `true`   | Suppress tablespace statements (`--no-tablespaces`) |
| `hex_blob`           | boolean | `false`  | Encode BINARY/BLOB columns as hex (`--hex-blob`) |
| `ssl_mode`           | string  | —        | TLS mode: `disabled`, `preferred`, `required`, `verify_ca`, `verify_identity` |
| `ssl_cert`           | string  | —        | Path to client SSL certificate (PEM) |
| `ssl_key`            | string  | —        | Path to client SSL private key (PEM) |
| `ssl_ca`             | string  | —        | Path to CA certificate (PEM) |

### MySQL-only options

| Option              | Type    | Default | Description |
|---------------------|---------|---------|-------------|
| `column_statistics` | boolean | `true`  | Query `COLUMN_STATISTICS`; set `false` (`--column-statistics=0`) when mysqldump 8.0 targets MySQL 5.7 |
| `set_gtid_purged`   | string  | `AUTO`  | GTID mode: `AUTO`, `ON`, or `OFF` |
| `mysql_bin_dir`     | string  | —       | Directory containing `mysql`/`mysqldump` binaries |

### MariaDB-only options

| Option            | Type   | Default | Description |
|-------------------|--------|---------|-------------|
| `mariadb_bin_dir` | string | —       | Directory containing `mariadb`/`mariadb-dump` binaries |

### Examples with options

```sh
# MySQL — schema only, custom binary location
plakar source add mydb \
  "mysql://dbuser:secret@db.example.com/mydb \
   no_data=true \
   mysql_bin_dir=/usr/local/mysql/bin"

# MySQL — disable GTID tracking for cross-server restore
plakar source add mydb \
  "mysql://dbuser:secret@db.example.com/mydb set_gtid_purged=OFF"

# MariaDB — custom binary location
plakar source add mydb \
  "mysql+mariadb://dbuser:secret@db.example.com/mydb \
   mariadb_bin_dir=/usr/local/mariadb/bin"
```

## Exporter Options

### Common options (MySQL and MariaDB)

| Option       | Type    | Default | Description |
|--------------|---------|---------|-------------|
| `location`   | string  | —       | Connection URI |
| `host`       | string  | `127.0.0.1` | Server hostname (overrides URI) |
| `port`       | string  | `3306`  | Server port (overrides URI) |
| `username`   | string  | —       | Username (overrides URI) |
| `password`   | string  | —       | Password (overrides URI) |
| `database`   | string  | —       | Target database (inferred from filename if omitted) |
| `create_db`  | boolean | `false` | Issue `CREATE DATABASE IF NOT EXISTS` before restoring |
| `force`      | boolean | `false` | Pass `--force` to continue on SQL errors |
| `ssl_mode`   | string  | —       | TLS mode (same values as importer) |
| `ssl_cert`   | string  | —       | Client SSL certificate path |
| `ssl_key`    | string  | —       | Client SSL private key path |
| `ssl_ca`     | string  | —       | CA certificate path |

### MySQL-only options

| Option          | Type   | Default | Description |
|-----------------|--------|---------|-------------|
| `mysql_bin_dir` | string | —       | Directory containing the `mysql` binary |

### MariaDB-only options

| Option            | Type   | Default | Description |
|-------------------|--------|---------|-------------|
| `mariadb_bin_dir` | string | —       | Directory containing the `mariadb` binary |

## Snapshot Contents

Each backup snapshot contains:

| File              | Description |
|-------------------|-------------|
| `/manifest.json`  | Server metadata: version, configuration, databases, tables, routines, triggers, events |
| `/<database>.sql` | Single-database dump (when `database` is specified) |
| `/all.sql`        | Full-server dump (when `database` is omitted) |

### manifest.json schema

```json
{
  "version": 1,
  "created_at": "2025-04-08T12:00:00Z",
  "connector": "mysql",
  "host": "db.example.com",
  "port": "3306",
  "server_version": "8.0.36",
  "mysqldump_version": "mysqldump  Ver 8.0.36 Distrib 8.0.36, for Linux (x86_64)",
  "is_read_replica": false,
  "database": "mydb",
  "dump_format": "sql",
  "options": {
    "no_data": false,
    "no_create_info": false,
    "single_transaction": true,
    "routines": true,
    "events": true,
    "triggers": true,
    "hex_blob": false,
    "set_gtid_purged": "AUTO"
  },
  "server_config": {
    "datadir": "/var/lib/mysql",
    "hostname": "db.example.com",
    "character_set_server": "utf8mb4",
    "collation_server": "utf8mb4_0900_ai_ci",
    "max_connections": 151,
    "gtid_mode": "OFF"
  },
  "databases": [
    {
      "name": "mydb",
      "character_set": "utf8mb4",
      "collation": "utf8mb4_0900_ai_ci",
      "tables": [
        {
          "name": "users",
          "type": "BASE TABLE",
          "engine": "InnoDB",
          "row_estimate": 3,
          "columns": [...]
        }
      ],
      "routines": [...],
      "triggers": [...],
      "events": [...]
    }
  ]
}
```

For MariaDB backups the manifest uses `mariadump_version` instead of
`mysqldump_version`, and `"connector": "mariadb"`.

## Required Privileges

### For logical backup (single database)

```sql
GRANT SELECT, SHOW VIEW, TRIGGER, LOCK TABLES, EVENT ON mydb.* TO 'backup'@'%';
GRANT PROCESS ON *.* TO 'backup'@'%';
GRANT RELOAD ON *.* TO 'backup'@'%';  -- for FLUSH TABLES
```

With `--single-transaction` (default), `LOCK TABLES` is not needed for InnoDB tables:

```sql
GRANT SELECT, SHOW VIEW, TRIGGER, EVENT ON mydb.* TO 'backup'@'%';
GRANT PROCESS ON *.* TO 'backup'@'%';
```

### For all-databases backup

```sql
GRANT SELECT, SHOW VIEW, TRIGGER, LOCK TABLES, EVENT, RELOAD ON *.* TO 'backup'@'%';
GRANT PROCESS ON *.* TO 'backup'@'%';
```

### For manifest metadata collection

The manifest queries `INFORMATION_SCHEMA` (no extra privileges needed) and
`mysql.user` (requires SELECT on mysql.*). The mysql.user query is best-effort
and the backup succeeds even when it fails.

## Client Binary Compatibility

The `mysql://` connector invokes `mysqldump` (backup) and `mysql` (restore)
from `$PATH` or from `mysql_bin_dir`. **These binaries must come from the same
MySQL distribution as the server being backed up.**

The `mysql+mariadb://` connector invokes `mariadb-dump` (backup) and `mariadb`
(restore) from `$PATH` or from `mariadb_bin_dir`.

### Why not use `mysql://` against a MariaDB server?

Debian and Ubuntu systems install MariaDB's `mysqldump` by default when you
run `apt install default-mysql-client`. MariaDB's `mysqldump` is **not
compatible** with a MySQL 8 server for all-databases backups: it generates
`INSERT` statements for generated columns (e.g. `engine_cost.default_value`)
that MySQL 8 rejects during restore with `ERROR 3105 (HY000): The value
specified for generated column … is not allowed`.

If you are backing up a MySQL server, always verify you are running the correct
binary:

```sh
mysqldump --version
# Good:    mysqldump  Ver 8.4.x Distrib 8.4.x, for Linux (x86_64)
# Bad:     mysqldump from 11.x.x-MariaDB, client 10.x for …
```

If MariaDB tools are installed alongside MySQL tools, point the plugin to the
MySQL binary directory:

```sh
plakar source add mydb \
  "mysql://dbuser:secret@db.example.com/mydb mysql_bin_dir=/usr/local/mysql/bin"
```

## MySQL-Specific Notes

### InnoDB consistency with `single_transaction`

The `single_transaction` option (enabled by default) uses a consistent InnoDB
read snapshot for the entire dump without locking tables.  This is the
recommended setting for production databases running InnoDB.

**MyISAM caveat**: `single_transaction` does not prevent table locks for MyISAM
tables.  If your database contains MyISAM tables and you need a consistent
backup, disable `single_transaction` and accept table locks during the dump, or
migrate to InnoDB.

### GTIDs (`set_gtid_purged`)

When the MySQL server has GTIDs enabled (`gtid_mode=ON`), `mysqldump` includes
`SET @@GLOBAL.GTID_PURGED` statements in the dump.  Restoring such a dump to a
server that already has GTID history will fail with an error.

Solutions:
- Set `set_gtid_purged=OFF` on the source to omit GTID information from the dump.
- Reset GTID history on the target with `RESET MASTER` before restoring.

### User and grant migration

Single-database dumps do not include MySQL user accounts or grants.  To migrate
users, do one of:
- Use an all-databases backup (which includes the `mysql` system database).
- Export grants manually: `pt-show-grants` (Percona Toolkit) or iterate `SHOW GRANTS FOR`.
- Recreate user accounts manually on the target server.

### Large databases

For very large databases, consider running the backup from a read replica to avoid impacting the primary.

## SSL/TLS Configuration

```sh
# MySQL
plakar source add mydb \
  "mysql://dbuser:secret@db.example.com/mydb \
   ssl_mode=verify_identity \
   ssl_ca=/etc/ssl/mysql/ca.pem \
   ssl_cert=/etc/ssl/mysql/client-cert.pem \
   ssl_key=/etc/ssl/mysql/client-key.pem"

# MariaDB
plakar source add mydb \
  "mysql+mariadb://dbuser:secret@db.example.com/mydb \
   ssl_mode=verify_identity \
   ssl_ca=/etc/ssl/mariadb/ca.pem \
   ssl_cert=/etc/ssl/mariadb/client-cert.pem \
   ssl_key=/etc/ssl/mariadb/client-key.pem"
```

## Development

### Build

```sh
make build
```

### Run a local MySQL for manual testing

```sh
make testdb
# Then: mysql -h 127.0.0.1 -P 3306 -u root -psecret
```

### Run integration tests

Requires Docker.

```sh
make test
```

Tests run against both MySQL 8 and MariaDB 11 containers in parallel.

## Known Limitations (v1)

- **Physical backup not supported**: Only logical dumps via `mysqldump` / `mariadb-dump`.
- **No per-table filtering**: The entire database (or all databases) is dumped.
- **No restore-time no_data / no_create_info**: Filtering during restore is not yet supported; the full dump is always piped to the client CLI.
- **User/grant migration**: Single-database backups do not capture users or grants (see [User and grant migration](#user-and-grant-migration)).
