#!/bin/sh
#
# Run a throwaway MongoDB in docker to try the integration by hand.
#
# usage: dev-db.sh start|stop

set -e

DEV_DB_NAME=${DEV_DB_NAME:-plakar-mongodb-dev}
DEV_DB_IMAGE=${DEV_DB_IMAGE:-mongodb/mongodb-community-server:latest}
DEV_DB_PORT=${DEV_DB_PORT:-27017}
DEV_DB_USER=${DEV_DB_USER:-admin}
DEV_DB_PASSWORD=${DEV_DB_PASSWORD:-plakar}

run_mongosh() {
	docker exec "$DEV_DB_NAME" mongosh --quiet \
		-u "$DEV_DB_USER" -p "$DEV_DB_PASSWORD" \
		--authenticationDatabase admin "$@"
}

stop() {
	docker rm -f "$DEV_DB_NAME" > /dev/null 2>&1 || true
}

start() {
	stop
	docker run -d --name "$DEV_DB_NAME" -p "$DEV_DB_PORT:27017" \
		-e MONGODB_INITDB_ROOT_USERNAME="$DEV_DB_USER" \
		-e MONGODB_INITDB_ROOT_PASSWORD="$DEV_DB_PASSWORD" \
		"$DEV_DB_IMAGE" > /dev/null

	# The root user only exists once init is done, so wait on an
	# authenticated ping rather than on the port.
	until run_mongosh --eval 'db.runCommand({ping: 1})' > /dev/null 2>&1; do
		sleep 1
	done

	run_mongosh shop --eval '
		db.products.insertMany([
			{name: "keyboard", price: 49},
			{name: "mouse", price: 19},
			{name: "screen", price: 199},
		]);
		db.customers.insertMany([
			{name: "alice", email: "alice@example.com"},
			{name: "bob", email: "bob@example.com"},
		]);
	' > /dev/null

	url="mongodb://127.0.0.1:$DEV_DB_PORT"
	opts="username=$DEV_DB_USER password=$DEV_DB_PASSWORD use_tls=false"
	echo "plakar source add mongodb_dev $url $opts"
	echo "plakar destination add mongodb_dev $url $opts"
}

case "$1" in
start)	start ;;
stop)	stop ;;
*)	echo "usage: $0 start|stop" >&2; exit 1 ;;
esac
