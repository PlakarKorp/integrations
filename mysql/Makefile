GO      = go
EXT     =

PLAKAR  ?= plakar
VERSION ?= v1.0.0

GOOS   := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)
PTAR   := mysql_$(VERSION)_$(GOOS)_$(GOARCH).ptar

all: build

build:
	${GO} build -v -o mysqlImporter${EXT} ./plugin/mysql-importer
	${GO} build -v -o mysqlExporter${EXT} ./plugin/mysql-exporter
	${GO} build -v -o mariadbImporter${EXT} ./plugin/mariadb-importer
	${GO} build -v -o mariadbExporter${EXT} ./plugin/mariadb-exporter

package: build
	rm -f $(PTAR)
	$(PLAKAR) pkg create ./manifest.yaml $(VERSION)

uninstall:
	-$(PLAKAR) pkg rm mysql

install: package
	$(PLAKAR) pkg add ./$(PTAR)

reinstall: uninstall install

# Start a throw-away MySQL instance for restore testing.
# The restore hint is printed first since docker run blocks.
testdb:
	@echo "To restore a snapshot to this database:"
	@echo "  $(PLAKAR) destination rm mydb"
	@echo "  $(PLAKAR) destination add mydb mysql://root@127.0.0.1:3306 password=secret"
	@echo "  $(PLAKAR) restore -to @mydb <snapid>"
	@echo ""
	docker run --rm -ti --name test -p 3306:3306 -e MYSQL_ROOT_PASSWORD=secret -e MYSQL_DATABASE=testdb mysql:8

test:
	${GO} test -v ./tests/...

clean:
	rm -f mysqlImporter mysqlExporter
