GO=go
EXT=

all: build

build:
	${GO} build -v -o nfsImporter${EXT} ./plugin/importer
	${GO} build -v -o nfsExporter${EXT} ./plugin/exporter

clean:
	rm -f nfsImporter nfsExporter nfs-*.ptar
