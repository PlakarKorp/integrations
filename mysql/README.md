# MySQL Integration for Plakar

Logical backup and restore of MySQL databases using [Plakar](https://github.com/PlakarKorp/plakar).

Backups are performed with `mysqldump` and produce standard SQL files. Restores
are performed with the `mysql` CLI.  Because the dump format is plain SQL, the
backup is fully human-readable and can be restored without Plakar if needed.

## Prerequisites

- Plakar ≥ 1.1
- `mysqldump` and `mysql` CLI tools in `$PATH` (or specify `mysql_bin_dir`)
- MySQL 5.7 or MySQL 8.x server
- A MySQL user with sufficient privileges (see [Required Privileges](#required-privileges))

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
# Add the source once
plakar source add mydb mysql://dbuser:secret@db.example.com/mydb

# Back it up
plakar backup @mydb
```

### Back up all databases

Omit the database path from the URI to use `mysqldump --all-databases`:

```sh
plakar source add alldb mysql://root:secret@db.example.com
plakar backup @alldb
```

### Restore a single database

```sh
# Restore into an existing (empty) database
plakar restore -to mysql://dbuser:secret@target.example.com/mydb <snapshot-id>

# Restore and create the database first
plakar restore -to "mysql://dbuser:secret@target.example.com/mydb create_db=true" <snapshot-id>
```

### Restore all databases

```sh
plakar restore -to mysql://root:secret@target.example.com <snapshot-id>
```

## Connection URI

```
mysql://[user[:password]@]host[:port][/database]
```

| Component  | Default     | Description                                        |
|------------|-------------|----------------------------------------------------|
| `user`     | —           | MySQL username                                     |
| `password` | —           | MySQL password (never passed on the command line)  |
| `host`     | `127.0.0.1` | MySQL server hostname or IP address                |
| `port`     | `3306`      | MySQL server port                                  |
| `database` | —           | Target database (omit for `--all-databases` backup)|

Individual keys (`host`, `port`, `username`, `password`) always override the
corresponding URI component when both are provided.

## Importer Options

These options are passed after the URI when adding a source or during backup:

| Option               | Type    | Default  | Description |
|----------------------|---------|----------|-------------|
| `location`           | string  | —        | Connection URI (`mysql://…`) |
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
| `no_tablespaces`     | boolean | `true`   | Suppress tablespace statements (`--no-tablespaces`); recommended for cloud/managed MySQL |
| `column_statistics`  | boolean | `true`   | Query `COLUMN_STATISTICS`; set `false` (`--column-statistics=0`) when mysqldump 8.0 targets MySQL 5.7/MariaDB |
| `hex_blob`           | boolean | `false`  | Encode BINARY/BLOB columns as hex (`--hex-blob`) |
| `set_gtid_purged`    | string  | `AUTO`   | GTID mode: `AUTO`, `ON`, or `OFF` |
| `mysql_bin_dir`      | string  | —        | Directory containing `mysql`/`mysqldump` binaries |
| `ssl_mode`           | string  | —        | TLS mode: `disabled`, `preferred`, `required`, `verify_ca`, `verify_identity` |
| `ssl_cert`           | string  | —        | Path to client SSL certificate (PEM) |
| `ssl_key`            | string  | —        | Path to client SSL private key (PEM) |
| `ssl_ca`             | string  | —        | Path to CA certificate (PEM) |

### Examples with options

```sh
# Schema only, custom binary location
plakar source add mydb \
  "mysql://dbuser:secret@db.example.com/mydb \
   no_data=true \
   mysql_bin_dir=/usr/local/mysql/bin"

# Disable GTID tracking for cross-server restore
plakar source add mydb \
  "mysql://dbuser:secret@db.example.com/mydb set_gtid_purged=OFF"

# Skip triggers
plakar source add mydb \
  "mysql://dbuser:secret@db.example.com/mydb triggers=false"
```

## Exporter Options

| Option       | Type    | Default | Description |
|--------------|---------|---------|-------------|
| `location`   | string  | —       | Connection URI (`mysql://…`) |
| `host`       | string  | `127.0.0.1` | Server hostname (overrides URI) |
| `port`       | string  | `3306`  | Server port (overrides URI) |
| `username`   | string  | —       | Username (overrides URI) |
| `password`   | string  | —       | Password (overrides URI) |
| `database`   | string  | —       | Target database (inferred from filename if omitted) |
| `create_db`  | boolean | `false` | Issue `CREATE DATABASE IF NOT EXISTS` before restoring |
| `force`      | boolean | `false` | Pass `--force` to continue on SQL errors |
| `mysql_bin_dir` | string | —    | Directory containing the `mysql` binary |
| `ssl_mode`   | string  | —       | TLS mode (same values as importer) |
| `ssl_cert`   | string  | —       | Client SSL certificate path |
| `ssl_key`    | string  | —       | Client SSL private key path |
| `ssl_ca`     | string  | —       | CA certificate path |

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

For very large databases, consider:
- Running the backup from a read replica to avoid impacting the primary.
- Ensuring enough disk/memory for the mysqldump process.
- Plakar's own deduplication and compression handle repeated content efficiently.

## SSL/TLS Configuration

```sh
plakar source add mydb \
  "mysql://dbuser:secret@db.example.com/mydb \
   ssl_mode=verify_identity \
   ssl_ca=/etc/ssl/mysql/ca.pem \
   ssl_cert=/etc/ssl/mysql/client-cert.pem \
   ssl_key=/etc/ssl/mysql/client-key.pem"
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
make integration-test
```

## Known Limitations (v1)

- **Physical backup not supported**: Only logical dumps via `mysqldump`.  For
  physical backups, consider Percona XtraBackup or MySQL Shell's dump utility
  (potential future plugin).
- **No per-table filtering**: The entire database (or all databases) is dumped.
- **No restore-time no_data / no_create_info**: Filtering during restore is not
  yet supported; the full dump is always piped to `mysql`.
- **User/grant migration**: Single-database backups do not capture users or
  grants (see [User and grant migration](#user-and-grant-migration)).

## See Also

- `mysql.1` — man page
- [Plakar documentation](https://plakar.io/docs)
- [mysqldump reference](https://dev.mysql.com/doc/refman/8.0/en/mysqldump.html)
