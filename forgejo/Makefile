GO=go

all: build

build:
	${GO} build -v -o forgejoImporter ./plugin/importer
	${GO} build -v -o forgejoExporter ./plugin/exporter

clean:
	rm -f forgejoImporter forgejoExporter forgejo-*.ptar
