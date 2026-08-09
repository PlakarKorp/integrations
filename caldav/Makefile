GO=go
EXT=

all: importer exporter

importer:
	${GO} build -o importer-caldav${EXT} ./importer/caldav.go ./importer/main.go

exporter:
	${GO} build -o exporter-caldav${EXT} ./exporter/caldav.go ./exporter/main.go

.PHONY: all importer exporter
