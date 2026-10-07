#!/bin/sh
#
# Copyright (c) 2026 Stefan Sperling <stsp@stsp.name>
#
# Permission to use, copy, modify, and distribute this software for any
# purpose with or without fee is hereby granted, provided that the above
# copyright notice and this permission notice appear in all copies.
#
# THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
# WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
# MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
# ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
# WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
# ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
# OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.

# This file contains test cases which backup data from MongoDB via Plakar.

. ./common.sh

test_restore_basic() {
	local testname="restore_basic"
	local testroot=`test_init "$testname"`

	run_plakar at "$testroot/backups" create -plaintext \
		> /dev/null

	run_plakar source add mongodb_src "$PLAKAR_MONGODB_ADDR" \
		> /dev/null

	get_auth_creds_yml >> $testroot/cfg/sources.yml

	timestamp="2026-08-16T14:27:24Z"
	run_plakar at "$testroot/backups" backup \
		-o use_tls=false \
		-force-timestamp "$timestamp" \
		"@mongodb_src" > $testroot/stdout

	snapshot=$(cat $testroot/stdout | cut -d : -f1)

	run_mongosh "db.collection.find()" > $testroot/collection.before

	run_mongosh "db.collection.drop()" > $testroot/stdout
	cat > $testroot/stdout.expected <<EOF
switched to db admin
{ ok: 1 }
switched to db test
true
EOF
	cmp -s $testroot/stdout.expected $testroot/stdout
	ret=$?
	if [ $ret -ne 0 ]; then
		diff -u $testroot/stdout.expected $testroot/stdout
		test_done "$testroot" "$ret"
		return 1
	fi

	run_plakar destination add mongodb_dst "$PLAKAR_MONGODB_ADDR" \
		> /dev/null

	get_auth_creds_yml >> $testroot/cfg/destinations.yml

	run_plakar at "$testroot/backups" restore -to @mongodb_dst \
		-o use_tls=false \
		$snapshot

	run_mongosh "db.collection.find()" > $testroot/collection.after
	cmp -s $testroot/collection.before $testroot/collection.after
	ret=$?
	if [ $ret -ne 0 ]; then
		diff -u $testroot/collection.before $testroot/collection.after
		test_done "$testroot" "$ret"
		return 1
	fi

	test_done "$testroot" "$ret"
}

test_restore_basic_tls() {
	local testname="restore_basic_tls"
	local testroot=`test_init "$testname"`

	run_plakar at "$testroot/backups" create -plaintext \
		> /dev/null

	run_plakar source add mongodb_src "$PLAKAR_MONGODB_ADDR" \
		> /dev/null

	get_auth_creds_yml >> $testroot/cfg/sources.yml

	timestamp="2026-08-16T14:27:24Z"
	run_plakar at "$testroot/backups" backup \
		-o tls_ca_cert="$CA_CRT" \
		-o auth_mechanism="SCRAM-SHA-256" \
		-force-timestamp "$timestamp" \
		"@mongodb_src" > $testroot/stdout

	snapshot=$(cat $testroot/stdout | cut -d : -f1)

	run_mongosh "db.collection.find()" > $testroot/collection.before

	run_mongosh "db.collection.drop()" > $testroot/stdout
	cat > $testroot/stdout.expected <<EOF
switched to db admin
{ ok: 1 }
switched to db test
true
EOF
	cmp -s $testroot/stdout.expected $testroot/stdout
	ret=$?
	if [ $ret -ne 0 ]; then
		diff -u $testroot/stdout.expected $testroot/stdout
		test_done "$testroot" "$ret"
		return 1
	fi

	run_plakar destination add mongodb_dst "$PLAKAR_MONGODB_ADDR" \
		> /dev/null

	get_auth_creds_yml >> $testroot/cfg/destinations.yml

	run_plakar at "$testroot/backups" restore -to @mongodb_dst \
		-o tls_ca_cert="$CA_CRT" \
		-o auth_mechanism="SCRAM-SHA-256" \
		$snapshot

	run_mongosh "db.collection.find()" > $testroot/collection.after
	cmp -s $testroot/collection.before $testroot/collection.after
	ret=$?
	if [ $ret -ne 0 ]; then
		diff -u $testroot/collection.before $testroot/collection.after
		test_done "$testroot" "$ret"
		return 1
	fi

	test_done "$testroot" "$ret"
}

test_restore_basic_mtls() {
	local testname="restore_basic_mtls"
	local testroot=`test_init "$testname"`

	run_plakar at "$testroot/backups" create -plaintext \
		> /dev/null

	run_plakar source add mongodb_src "$PLAKAR_MONGODB_ADDR" \
		> /dev/null

	get_auth_creds_yml >> $testroot/cfg/sources.yml

	timestamp="2026-08-16T14:27:24Z"
	run_plakar at "$testroot/backups" backup \
		-o tls_ca_cert="$CA_CRT" \
		-o tls_client_cert="$CLIENT_PEM" \
		-o auth_mechanism="SCRAM-SHA-256" \
		-force-timestamp "$timestamp" \
		"@mongodb_src" > $testroot/stdout

	snapshot=$(cat $testroot/stdout | cut -d : -f1)

	run_mongosh "db.collection.find()" > $testroot/collection.before

	run_mongosh "db.collection.drop()" > $testroot/stdout
	cat > $testroot/stdout.expected <<EOF
switched to db admin
{ ok: 1 }
switched to db test
true
EOF
	cmp -s $testroot/stdout.expected $testroot/stdout
	ret=$?
	if [ $ret -ne 0 ]; then
		diff -u $testroot/stdout.expected $testroot/stdout
		test_done "$testroot" "$ret"
		return 1
	fi

	run_plakar destination add mongodb_dst "$PLAKAR_MONGODB_ADDR" \
		> /dev/null

	get_auth_creds_yml >> $testroot/cfg/destinations.yml

	run_plakar at "$testroot/backups" restore -to @mongodb_dst \
		-o tls_ca_cert="$CA_CRT" \
		-o tls_client_cert="$CLIENT_PEM" \
		-o auth_mechanism="SCRAM-SHA-256" \
		$snapshot

	run_mongosh "db.collection.find()" > $testroot/collection.after
	cmp -s $testroot/collection.before $testroot/collection.after
	ret=$?
	if [ $ret -ne 0 ]; then
		diff -u $testroot/collection.before $testroot/collection.after
		test_done "$testroot" "$ret"
		return 1
	fi

	test_done "$testroot" "$ret"
}

test_restore_basic_mtls_auth() {
	local testname="restore_basic_mtls_auth"
	local testroot=`test_init "$testname"`

	run_mongosh "db.getSiblingDB(\"\$external\").runCommand(
	  {
	    updateUser: \"$MONGODB_TESTCLIENT\",
	    roles: [
	      { role: \"readWrite\", db: \"test\" },
	      { role: \"read\", db: \"admin\" },
	      { role: \"userAdminAnyDatabase\", db: \"admin\" },
	      { role: \"read\", db: \"config\" }
	    ],
	    writeConcern: { w: \"majority\", wtimeout: 5000 }
	  }
	)" > /dev/null

	run_plakar at "$testroot/backups" create -plaintext \
		> /dev/null

	run_plakar source add mongodb_src "$PLAKAR_MONGODB_ADDR" \
		> /dev/null

	timestamp="2026-08-16T14:27:24Z"
	run_plakar at "$testroot/backups" backup \
		-o tls_ca_cert="$CA_CRT" \
		-o tls_client_cert="$CLIENT_PEM" \
		-o auth_mechanism="MONGODB-X509" \
		-force-timestamp "$timestamp" \
		"@mongodb_src" > $testroot/stdout

	snapshot=$(cat $testroot/stdout | cut -d : -f1)

	run_mongosh "db.collection.find()" > $testroot/collection.before

	run_mongosh "db.collection.drop()" > $testroot/stdout
	cat > $testroot/stdout.expected <<EOF
switched to db admin
{ ok: 1 }
switched to db test
true
EOF
	cmp -s $testroot/stdout.expected $testroot/stdout
	ret=$?
	if [ $ret -ne 0 ]; then
		diff -u $testroot/stdout.expected $testroot/stdout
		test_done "$testroot" "$ret"
		return 1
	fi

	run_mongosh "db.getSiblingDB(\"\$external\").runCommand(
	  {
	    updateUser: \"$MONGODB_TESTCLIENT\",
	    roles: [
	      { role: \"root\", db: \"admin\" },
	    ],
	    writeConcern: { w: \"majority\", wtimeout: 5000 }
	  }
	)" > /dev/null

	run_plakar destination add mongodb_dst "$PLAKAR_MONGODB_ADDR" \
		> /dev/null

	run_plakar at "$testroot/backups" restore -to @mongodb_dst \
		-o tls_ca_cert="$CA_CRT" \
		-o tls_client_cert="$CLIENT_PEM" \
		-o auth_mechanism="MONGODB-X509" \
		$snapshot

	run_mongosh "db.collection.find()" > $testroot/collection.after
	cmp -s $testroot/collection.before $testroot/collection.after
	ret=$?
	if [ $ret -ne 0 ]; then
		diff -u $testroot/collection.before $testroot/collection.after
		test_done "$testroot" "$ret"
		return 1
	fi

	test_done "$testroot" "$ret"
}

test_parseargs "$@"
run_test test_restore_basic		no-tls
run_test test_restore_basic_tls
run_test test_restore_basic_mtls
run_test test_restore_basic_mtls_auth
exit $test_status
