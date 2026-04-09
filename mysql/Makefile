OS   ?= $(shell go env GOOS)
ARCH ?= $(shell go env GOARCH)

MYSQLIMPORTER = mysqlImporter
MYSQLEXPORTER = mysqlExporter

PLAKAR  ?= plakar
VERSION ?= v1.0.0

build: $(MYSQLIMPORTER) $(MYSQLEXPORTER)

$(MYSQLIMPORTER):
	go build -o $@ ./plugin/importer

$(MYSQLEXPORTER):
	go build -o $@ ./plugin/exporter

.PHONY: package
package: build
	mkdir -p /tmp/mysqlpkg
	cp $(MYSQLIMPORTER) $(MYSQLEXPORTER) /tmp/mysqlpkg/
	cp manifest.yaml /tmp/mysqlpkg/
	cd /tmp/mysqlpkg && \
		$(PLAKAR) pkg create ./manifest.yaml $(VERSION)
	cp /tmp/mysqlpkg/mysql_$(VERSION)_$(OS)_$(ARCH).ptar .
	rm -rf /tmp/mysqlpkg

.PHONY: install
install: package
	$(PLAKAR) pkg add ./mysql_$(VERSION)_$(OS)_$(ARCH).ptar

.PHONY: uninstall
uninstall:
	$(PLAKAR) pkg rm mysql

.PHONY: reinstall
reinstall: uninstall install

.PHONY: testdb
testdb:
	docker run --rm -it \
		-p 3306:3306 \
		-e MYSQL_ROOT_PASSWORD=secret \
		-e MYSQL_DATABASE=testdb \
		mysql:8

.PHONY: integration-test
integration-test:
	go test -v -timeout 20m ./tests/...

.PHONY: clean
clean:
	rm -f $(MYSQLIMPORTER) $(MYSQLEXPORTER)

.PHONY: all
all: build
