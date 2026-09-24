MONGODB_DOCKER_NAME=mongodb
MONGODB_DOCKER_IMAGE=mongodb/mongodb-community-server:latest
MONGODB_PORT=27017
PLAKAR_MONGODB_ADDR=mongodb://127.0.0.1:${MONGODB_PORT}
CA_KEY=ca.key
CA_CSR=ca.csr
CA_CRT=ca.pem
MONGODB_KEY=mongodb.key
MONGODB_CSR=mongodb.csr
MONGODB_CRT=mongodb.crt
MONGODB_PEM=mongodb.pem
MONGOD_CFG=mongod.cfg
CLIENT_KEY=client.key
CLIENT_CSR=client.csr
CLIENT_CRT=client.crt
CLIENT_PEM=client.pem
TLS_MODE=allowTLS

.PHONY: all prepare shell clean start stop test_backup test_restore

all: start test_backup test_restore stop


${CA_KEY}:
	openssl genrsa -out "$@" 4096
	chmod 600 "$@"

${CA_CSR}: ${CA_KEY}
	openssl req -batch -new -key "${CA_KEY}" \
		-subj "/C=FR/O=Plakar/CN=TestCA" \
		-addext "basicConstraints=CA:true" \
		-out "$@"

${CA_CRT}: ${CA_CSR} ${CA_KEY}
	openssl x509 -req -days 365 -in "${CA_CSR}" \
		-extfile x509v3.cnf -extensions x509v3_CA \
		-signkey "${CA_KEY}" -out "$@"

${MONGODB_KEY}:
	openssl genrsa -out "$@" 4096
	chmod 600 "$@"

${MONGODB_CSR}: ${MONGODB_KEY}
	openssl req -batch -new -key "${MONGODB_KEY}" \
		-subj "/C=FR/O=Plakar/CN=TestServer" \
		-addext "subjectAltName=IP:127.0.0.1" \
		-out "$@"

${MONGODB_CRT}: ${MONGODB_CSR} ${MONGODB_KEY}
	openssl x509 -req -days 365 -in "${MONGODB_CSR}" \
		-extfile x509v3.cnf -extensions x509v3_IPAddr \
		-CA "${CA_CRT}" -CAkey "${CA_KEY}" \
		-out "$@"

${MONGODB_PEM}: ${MONGODB_CRT} ${MONGODB_KEY}
	cat ${MONGODB_CRT} ${MONGODB_KEY} > "$@"

${CLIENT_KEY}:
	openssl genrsa -out "$@" 4096
	chmod 600 "$@"

${CLIENT_CSR}: ${CLIENT_KEY}
	openssl req -batch -new -key "${CLIENT_KEY}" \
		-subj "/C=FR/O=Plakar/CN=TestClient" \
		-out "$@"

${CLIENT_CRT}: ${CLIENT_CSR} ${CLIENT_KEY}
	openssl x509 -req -days 365 -in "${CLIENT_CSR}" \
		-extfile x509v3.cnf -extensions x509v3_IPAddrClient \
		-CA "${CA_CRT}" -CAkey "${CA_KEY}" \
		-out "$@"

${CLIENT_PEM}: ${CLIENT_CRT} ${CLIENT_KEY}
	cat ${CLIENT_CRT} ${CLIENT_KEY} > "$@"

prepare: ${CA_CRT} ${MONGODB_PEM} ${CLIENT_PEM}
	docker pull ${MONGODB_DOCKER_IMAGE}
	set -e; d=`mktemp -d /tmp/plakar-mongodb-XXXXXXXXXXXX`; \
	echo "$$d" > testdir; \
	trap "rm -rf \"$$d\"" HUP INT QUIT PIPE TERM; \
	cp ${CA_CRT} "$$d"; \
	cp ${MONGODB_PEM} "$$d"; \
	cp ${MONGOD_CFG} "$$d"; \
	cp ${CLIENT_PEM} "$$d"; \
	docker container create --name ${MONGODB_DOCKER_NAME} \
		-p ${MONGODB_PORT}:${MONGODB_PORT} \
		--env-file mongo-test-authdata \
		--mount type=bind,src=/tmp,dst=/tmp \
		${MONGODB_DOCKER_IMAGE} \
		--tlsCAFile="$$d/${CA_CRT}" \
		--tlsCertificateKeyFile="$$d/${MONGODB_PEM}" \
		--config "$$d/${MONGOD_CFG}" \
		--tlsMode ${TLS_MODE}

clean: stop
	rm -f "${CA_CRT}" "${CA_CSR}" "${CA_KEY}"
	rm -f "${MONGODB_PEM}" "${MONGODB_CRT}" "${MONGODB_CSR}" "${MONGODB_KEY}"
	rm -f "${CLIENT_PEM}" "${CLIENT_CRT}" "${CLIENT_CSR}" "${CLIENT_KEY}"
	-set -e; d=`cat testdir`; [ -d "$$d" ] && rm -f "$$d"/* && rmdir "$$d"
	-docker container remove ${MONGODB_DOCKER_NAME} > /dev/null

start:
	docker start ${MONGODB_DOCKER_NAME} > /dev/null
	@echo -n "Waiting for container to finish booting..."; \
	while sleep 1; do \
		if nc -z 127.0.0.1 ${MONGODB_PORT}; then \
			echo "done"; \
			break; \
		else \
			echo; \
			echo -n "Waiting for container to finish booting..."; \
		fi; \
	done

stop:
	-docker stop ${MONGODB_DOCKER_NAME} > /dev/null

shell:
	docker exec -it ${MONGODB_DOCKER_NAME} /bin/bash

test_backup:
	@echo === $@ ===
	@set -e; \
	d=`cat testdir`; \
	env MONGODB_PORT=${MONGODB_PORT} \
		MONGODB_DOCKER_NAME=${MONGODB_DOCKER_NAME} \
		CA_CRT="$$d/${CA_CRT}" \
		CLIENT_PEM="$$d/${CLIENT_PEM}" \
		PLAKAR_MONGODB_ADDR=${PLAKAR_MONGODB_ADDR} \
		./test_backup.sh; ret=$$?; \
	if [ $$ret -eq 0 ]; then echo "All tests passed"; fi; exit $$ret

test_restore:
	@echo === $@ ===
	@set -e; \
	d=`cat testdir`; \
	env MONGODB_PORT=${MONGODB_PORT} \
		MONGODB_DOCKER_NAME=${MONGODB_DOCKER_NAME} \
		CA_CRT="$$d/${CA_CRT}" \
		CLIENT_PEM="$$d/${CLIENT_PEM}" \
		PLAKAR_MONGODB_ADDR=${PLAKAR_MONGODB_ADDR} \
		./test_restore.sh; ret=$$?; \
	if [ $$ret -eq 0 ]; then echo "All tests passed"; fi; exit $$ret
