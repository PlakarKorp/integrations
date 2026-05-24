GO=go
EXT=

all: build

build:
	${GO} build -v -o ftpImporter${EXT} ./plugin/importer
	${GO} build -v -o ftpExporter${EXT} ./plugin/exporter

clean:
	rm -f ftpImporter ftpExporter ftp-*.ptar
