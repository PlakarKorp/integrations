GO=go
EXT=

all: build

build:
	${GO} build -v -o sftpImporter${EXT} ./plugin/importer
	${GO} build -v -o sftpExporter${EXT} ./plugin/exporter
	${GO} build -v -o sftpStorage${EXT} ./plugin/storage

clean:
	rm -f sftpImporter sftpExporter sftpStorage sftp-*.ptar
