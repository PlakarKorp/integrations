# Redis Integration for Plakar

Back up Redis persistent data as an RDB file and restore that file to disk for Redis startup.

## What it does

- Connects to a local or remote Redis instance with `redis-cli`.
- Optionally triggers `BGSAVE` and waits for the background save to finish.
- Captures the resulting `dump.rdb` into a Plakar snapshot as `/dump.rdb`.
- Restores `/dump.rdb` back to a file path that Redis can use on startup.

This integration is intended for deployments where Redis is used as a primary data store with persistence enabled, not just as a disposable cache.

## Prerequisites

- Plakar ≥ 1.1.
- `redis-cli` available in `$PATH`, or set `redis_bin_dir` / `redis_cli`.
- A Redis user allowed to run `PING`, `BGSAVE`, `INFO persistence`, and `CONFIG GET dir/dbfilename`.
- Filesystem access to the Redis RDB file when backing up local Redis. If the RDB file is not locally readable, the importer falls back to `redis-cli --rdb -`.

## Back up

```sh
plakar source add redis redis://:secret@127.0.0.1:6379/0
plakar backup @redis
```

TLS connections can use `rediss://` or `tls=true`:

```sh
plakar source add redis rediss://default:secret@redis.example.com:6379/0
```

## Restore

Restore writes the RDB file to disk. Stop Redis first, restore to its configured `dbfilename` under `dir`, fix ownership if needed, then start Redis.

```sh
plakar restore -to redis-file:///var/lib/redis/dump.rdb <snapshot-id>
```

Use `force=true` to overwrite an existing file:

```sh
plakar restore -to redis-file:///var/lib/redis/dump.rdb -o force=true <snapshot-id>
```

## Importer options

| Option | Default | Description |
| --- | --- | --- |
| `location` | — | `redis://` or `rediss://` URI |
| `host` | `127.0.0.1` | Redis host |
| `port` | `6379` | Redis port |
| `username` | — | ACL username |
| `password` | — | Password, passed via `REDISCLI_AUTH` |
| `database` | URI path | Logical database number for command context |
| `tls` | `false` | Enable TLS |
| `insecure_tls` | `false` | Skip TLS verification for redis-cli |
| `ca_cert`, `cert`, `key` | — | redis-cli TLS certificate flags |
| `redis_bin_dir` | — | Directory containing `redis-cli` |
| `redis_cli` | `redis-cli` | redis-cli executable name |
| `trigger_bgsave` | `true` | Run `BGSAVE` before reading the RDB |
| `wait_timeout` | `5m` | Maximum wait for BGSAVE completion |
| `output` | `/dump.rdb` | Snapshot pathname for the emitted RDB |

## Exporter options

| Option | Default | Description |
| --- | --- | --- |
| `location` | — | `redis-file://` output path |
| `output` | — | Output path override |
| `force` | `false` | Overwrite an existing RDB file |
