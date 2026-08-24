# Plakar MongoDB Integration

This integration provides a [Plakar](https://plakar.io) importer and
exporter for [MongoDB](https://mongodb.com).

## Overview

This integration allows:

- Seamless export of MongoDB data into a Kloset repository.
- Direct restoration of data from Kloset to MongoDB

This integration uses the mongosh, mongodump, and mongorestore utlities.

## Configuration

TODO

## Tests

Tests can be run with:

```bash
make -C tests
```
At time of writing the tests only work on Linux.

The following programs are required in $PATH:

- mongosh, mongodump, and mongorestore
- plakar, with this integration installed via `plakar pkg add`)
- docker, with the current user allowed to create and run containers
- netcat, either the "traditional" Hobbit version or the OpenBSD version
