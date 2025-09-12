GO=go
EXT=

all: importer exporter

importer:
	${GO} build -o notion-importer${EXT} -v ./importer

exporter:
	${GO} build -o notion-exporter${EXT} -v ./exporter

.PHONY: all importer exporter
