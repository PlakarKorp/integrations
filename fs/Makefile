GO=go

all: build

build:
	${GO} build -v -o fs-importer ./plugin/importer
	${GO} build -v -o fs-exporter ./plugin/exporter
	${GO} build -v -o fs-storage ./plugin/storage

clean:
	rm -f fs-importer fs-exporter fs-storage fs-*.ptar
