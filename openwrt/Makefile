GO=go
PLAKAR=plakar

VERSION=v1.1.0-beta.1

all: build

build: importer exporter

importer:
	${GO} build -v -o openwrt-importer ./plugin/importer

exporter:
	${GO} build -v -o openwrt-exporter ./plugin/exporter

package: build
	rm -f openwrt_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

clean:
	rm -f openwrt-importer
	rm -f openwrt-exporter
	rm -f fetcharchive

fclean: clean
	rm -f openwrt_${VERSION}_*.ptar

re: clean build

install: package
	${PLAKAR} pkg add -allow-unsigned ./openwrt_${VERSION}_*.ptar

uninstall:
	${PLAKAR} pkg rm openwrt

reinstall: uninstall install

test:
	${GO} test -v ./...

check: test

fetcharchive:
	go build -v -o fetcharchive ./cmd/fetcharchive/

.PHONY: all build importer exporter package clean fclean re install uninstall reinstall test check
