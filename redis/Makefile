GO      = go
EXT     =
PLAKAR  ?= plakar
VERSION ?= v1.0.0
GOOS   := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)
PTAR   := redis_$(VERSION)_$(GOOS)_$(GOARCH).ptar

all: build

build:
	${GO} build -v -o redisImporter${EXT} ./plugin/redis-importer
	${GO} build -v -o redisExporter${EXT} ./plugin/redis-exporter

package: build
	rm -f $(PTAR)
	$(PLAKAR) pkg create ./manifest.yaml $(VERSION)

uninstall:
	-$(PLAKAR) pkg rm redis

install: package
	$(PLAKAR) pkg add ./$(PTAR)

reinstall: uninstall install

test:
	${GO} test -v ./...

clean:
	rm -f redisImporter redisExporter
