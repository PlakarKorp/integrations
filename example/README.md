# Example integration

A minimal Plakar integration providing an importer, an exporter, and a storage
backend for the `example://` protocol. It is the complete example for the
[Go Kloset SDK reference](https://plakar.io/docs/community/main/references/go-kloset-sdk/),
which documents the interfaces, records, and location flags.

The connectors ignore the location: the importer generates five small
in-memory files, the exporter prints records to stderr, and the storage backend
keeps everything in memory, so a repository is lost when the command exits.

## Layout

```
.
├── connector.go        # Connector implementations and registration
├── plugin/
│   ├── importer/main.go
│   ├── exporter/main.go
│   └── storage/main.go
├── manifest.yaml
└── Makefile
```

To drop a connector type, remove its entry from `manifest.yaml`, its directory
under `plugin/`, and its registration in `connector.go`.

## Build and install

```sh
make build      # example-importer, example-exporter, example-storage
make create     # example_v1.0.0_<os>_<arch>.ptar
make install    # plakar pkg add -allow-unsigned
```

Set `VERSION` to package another version, for example
`make create install VERSION=v1.2.3`.

## Usage

```sh
plakar at /tmp/repo create
plakar at /tmp/repo backup example:///
```
