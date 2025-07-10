all: importer exporter

importer:
	go build -o importer-caldav ./importer/caldav.go

exporter:
	go build -o exporter-caldav ./exporter/caldav.go

.PHONY: all importer exporter
