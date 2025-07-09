all: importer exporter

importer:
	go build -o rclone-importer -v importer/rclone/main/main.go
	go build -o googlephotos-importer -v importer/googlephotos/main/main.go

exporter:
	go build -o rclone-exporter -v exporter/rclone/main/main.go
	go build -o googlephotos-exporter -v exporter/googlephoto/main/main.go

clean:
	@rm -f rclone-importer googlephotos-importer rclone-exporter googlephotos-exporter

.PHONY: all importer exporter clean