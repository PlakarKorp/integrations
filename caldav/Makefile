all: importer exporter

importer:
	go build -o importer-caldav ./importer/caldav.go ./importer/main.go

exporter:
	go build -o exporter-caldav ./exporter/caldav.go ./exporter/main.go

.PHONY: all importer exporter
