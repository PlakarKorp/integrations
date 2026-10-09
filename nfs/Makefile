GO=go
EXT=

all: build

build:
	${GO} build -v -o nfsImporter${EXT} ./plugin/importer
	${GO} build -v -o nfsExporter${EXT} ./plugin/exporter
	${GO} build -v -o nfsStorage${EXT} ./plugin/storage

test:
	${GO} test -v ./...

clean:
	rm -f nfsImporter nfsExporter nfsStorage nfs-*.ptar
