GO=go
PLAKAR=plakar
VERSION=v0.1.0

all: build

build:
	${GO} build -v -o incusImporter ./plugin/importer
	${GO} build -v -o incusExporter ./plugin/exporter

pkg: build
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

install: pkg
	-${PLAKAR} pkg rm incus
	${PLAKAR} pkg add ./incus_${VERSION}_*.ptar

test:
	${GO} test ./...

clean:
	rm -f incusImporter incusExporter *.ptar
