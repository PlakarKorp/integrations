GO = go
PLAKAR = ../plakar/plakar
VERSION = v0.0.1

all: build

build:
	${GO} build -v -o imapImporter ./plugin/importer
	${GO} build -v -o imapExporter ./plugin/exporter

create:
	${PLAKAR} pkg create manifest.yaml

uninstall:
	${PLAKAR} pkg ls | grep imap-v | xargs ${PLAKAR} pkg uninstall

install:
	${PLAKAR} pkg install imap-${VERSION}.ptar

clean:
	rm -f imapImporter imapExporter imap-*.ptar
