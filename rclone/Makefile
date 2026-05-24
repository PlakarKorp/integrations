GO = go
EXT=

all: build

build:
	${GO} build -v -o rclone-importer${EXT} ./plugin/importer
	${GO} build -v -o rclone-exporter${EXT} ./plugin/exporter
	${GO} build -v -o rclone-storage${EXT} ./plugin/storage

clean:
	rm -f rclone-importer rclone-exporter rclone-storage rclone-*.ptar
