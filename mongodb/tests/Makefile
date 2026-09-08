MONGODB_DOCKER_NAME=mongodb
MONGODB_DOCKER_IMAGE=mongodb/mongodb-community-server:latest
MONGODB_PORT=27017
PLAKAR_MONGODB_ADDR=mongodb://127.0.0.1:${MONGODB_PORT}

.PHONY: all prepare shell clean start stop test_backup test_restore

all: start test_backup test_restore stop

prepare:
	docker pull ${MONGODB_DOCKER_IMAGE}
	docker container create --name ${MONGODB_DOCKER_NAME} \
		-p ${MONGODB_PORT}:${MONGODB_PORT} \
		--env-file mongo-test-authdata \
		--mount type=bind,src=/tmp,dst=/tmp \
		${MONGODB_DOCKER_IMAGE}

clean: stop
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
	env MONGODB_PORT=${MONGODB_PORT} \
		MONGODB_DOCKER_NAME=${MONGODB_DOCKER_NAME} \
		PLAKAR_MONGODB_ADDR=${PLAKAR_MONGODB_ADDR} \
		./test_backup.sh; ret=$$?; \
	if [ $$ret -eq 0 ]; then echo "All tests passed"; fi; exit $$ret

test_restore:
	@echo === $@ ===
	@set -e; \
	env MONGODB_PORT=${MONGODB_PORT} \
		MONGODB_DOCKER_NAME=${MONGODB_DOCKER_NAME} \
		PLAKAR_MONGODB_ADDR=${PLAKAR_MONGODB_ADDR} \
		./test_restore.sh; ret=$$?; \
	if [ $$ret -eq 0 ]; then echo "All tests passed"; fi; exit $$ret
