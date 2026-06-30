GO=go
EXT=

all: build

build:
	${GO} build -v -o smbImporter${EXT} ./plugin/importer
	${GO} build -v -o smbExporter${EXT} ./plugin/exporter

clean:
	rm -f smbImporter smbExporter smb-*.ptar
