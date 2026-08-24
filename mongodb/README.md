# Plakar MongoDB Integration

This integration provides a [Plakar](https://plakar.io) importer and
exporter for [MongoDB](https://mongodb.com).

## Overview

This integration allows:

- Seamless export of MongoDB data into a Kloset repository.
- Direct restoration of data from Kloset to MongoDB

This integration uses the mongosh, mongodump, and mongorestore utlities.

## Configuration
\
The required configuration parameters are as follows:

- `location`: A URL to the MongoDB server. On the command line this URL must begin with mongodb://`

The optional configuration parameters are as follows:

- `port`: The MongoDB server port. The default port is 27017.
- `username`: The username for authentication to MongoDB.
- `password`: The password for authentication to MongoDB.
- `use_tls`: Indicates Whether to use an encrypted TLS/SSL connection. Defaults to true.

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
