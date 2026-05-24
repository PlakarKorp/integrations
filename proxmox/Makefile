GO=go
EXT=

all: build

build:
	${GO} build -v -o proxmoxImporter${EXT} ./plugin/importer
	${GO} build -v -o proxmoxExporter${EXT} ./plugin/exporter

clean:
	rm -f proxmoxImporter proxmoxExporter proxmox_*.ptar
