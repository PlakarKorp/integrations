# SQLite

## Overview

**SQLite** is a lightweight, embeddable relational database engine widely used for local data storage. It requires no separate server process and stores data in a single portable file, making it ideal for local or embedded storage scenarios.

This integration allows:

* **Storing a Kloset repository inside an SQLite database file:**
  All snapshot data, metadata, and content-addressable storage are persisted within an SQLite file, enabling easy distribution, portability, and local backup scenarios.

* **Using a fully self-contained repository format:**
  Ideal for desktop, embedded, testing, or air-gapped environments where running a remote server or distributed backend is not feasible.

* **Compatibility with filesystem-based tools and version control:**
  SQLite repositories can be checked into version control, duplicated, archived, or copied across systems easily.

---

## Configuration

The supported configuration options are:

* `location`: full path to the SQLite database file (e.g., `sqlite:/home/user/backup.db`)

---

## Examples

```sh
# create a repository backed by an SQLite file
$ plakar at sqlite:/home/user/kloset.db create

# push a backup into the SQLite-backed repository
$ plakar at sqlite:/home/user/kloset.db backup /etc

# list snapshots in the repository
$ plakar at sqlite:/home/user/kloset.db ls
```
