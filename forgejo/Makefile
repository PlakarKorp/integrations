GO=go
EXT=

all: build

build:
	${GO} build -v -o forgejoImporter${EXT} ./plugin/importer
	${GO} build -v -o forgejoExporter${EXT} ./plugin/exporter

test:
	${GO} test ./...

clean:
	rm -f forgejoImporter forgejoExporter forgejo_*.ptar

.PHONY: all build test clean
