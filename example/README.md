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
│   ├── importer/{main.go,schema.json}
│   ├── exporter/{main.go,schema.json}
│   └── storage/{main.go,schema.json}
├── manifest.yaml
├── .qa-version         # PlakarKorp/QA version for lint rules and tools
└── Makefile
```

To drop a connector type, remove its entry from `manifest.yaml`, its directory
under `plugin/`, and its registration in `connector.go`.

## Configuration schemas

Each connector in `manifest.yaml` points to a JSON Schema with `validator`.
Right now these schemas are not used by `plakar ui` and only use by Plakar Control Plane.
It is however mandatory for consistency between Plakar CLI and control plane.

Each of those schemas describes the configuration accepted by the connector:
- The `location` parameter.
- The options available through the `config` parameter in your integration.

The example connectors has no options, so their schemas only accept a
`location` starting with `example://`.

## Build and install

```sh
make build      # example-importer, example-exporter, example-storage
make create     # example_v1.0.0_<os>_<arch>.ptar
make install    # plakar pkg add -allow-unsigned
```

Set `VERSION` to package another version, for example
`make create install VERSION=v1.2.3`.

## Checks

```sh
make test       # go vet and go test -race
make lint       # golangci-lint with the shared QA rules
make validate   # run the plakar package validator on the integration
```

`lint` and `validate` fetch their config and tool versions from
[PlakarKorp/QA](https://github.com/PlakarKorp/QA) at the commit in
`.qa-version`, the same one CI uses.

## Usage

```sh
plakar at /tmp/repo create
plakar at /tmp/repo backup example:///
```
