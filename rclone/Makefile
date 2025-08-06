GO = go

all: build

build:
	${GO} build -v -o rclone-importer ./plugin/importer
	${GO} build -v -o rclone-exporter ./plugin/exporter
	${GO} build -v -o rclone-storage ./plugin/storage

clean:
	rm -f rclone-importer rclone-exporter rclone-storage rclone-*.ptar