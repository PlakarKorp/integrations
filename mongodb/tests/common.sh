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

export LC_ALL=C

export TEST_TMPDIR="/tmp"
export TEST_DATA_FILE="testdata.bson"
export AUTH_DATA_FILE="mongo-test-authdata"

. ./"$AUTH_DATA_FILE"
export MONGODB_INITDB_ROOT_USERNAME
export MONGODB_INITDB_ROOT_PASSWORD

test_status=0

ret=-1

test_parseargs()
{
	while getopts qr: flag; do
		case $flag in
		q)	export TEST_QUIET=1
			;;
		r)	export TEST_TMPDIR=${OPTARG%/}
			;;
		?)	echo "Supported options:"
			echo "  -q: quiet mode"
			echo "  -r PATH: use PATH as test data root directory"
			exit 2
			;;
		esac
	done
	shift $(($OPTIND - 1))
	regress_run_only="$@"
} >&2

test_init()
{
	local testname="$1"
	local no_tree="$2"
	if [ -z "$testname" ]; then
		echo "No test name provided" >&2
		return 1
	fi
	local testroot=`mktemp -d "$TEST_TMPDIR/$testname-XXXXXXXXXX"`

	mkdir $testroot/cfg

	run_mongosh "db.collection.drop()" > /dev/null

	testdata=$(cat $TEST_DATA_FILE)
	run_mongosh "db.collection.insertOne($testdata)" > /dev/null

	echo "$testroot"
}

run_test()
{
	testfunc="$1"
	limits="$2"

	if [ -n "$regress_run_only" ]; then
		case "$regress_run_only" in
		*$testfunc) ;;
		*) return ;;
		esac
	fi

	if [ -z "$TEST_QUIET" ]; then
		echo -n "$testfunc "
	fi
	$testfunc
}

test_done()
{
	local testroot="$1"
	local result="$2"
	if [ "$result" = "0" ]; then
		test_cleanup "$testroot" || return 1
		if [ -z "$TEST_QUIET" ]; then
			echo "ok"
		fi
	elif echo "$result" | grep -q "^xfail"; then
		# expected test failure; test reproduces an unfixed bug
		echo "$result"
		return test_cleanup "$testroot"
	else
		if [ "$test_status" = "0" ]; then
			test_status=1
		fi
		echo "test failed; leaving test data in $testroot"
	fi

	return $result
}

test_cleanup()
{
	local testroot="$1"

	rm -rf "$testroot"
}

run_plakar()
{
	plakar -quiet -configdir "$testroot/cfg" "$@"
}

run_mongosh()
{
	docker exec ${MONGODB_DOCKER_NAME} mongosh \
		--port ${MONGODB_PORT} \
		-u "${MONGODB_INITDB_ROOT_USERNAME}" \
		-p "${MONGODB_INITDB_ROOT_PASSWORD}" \
		--eval "$*"
}

get_auth_creds_yml()
{
	cat ./"$AUTH_DATA_FILE" | sed \
		-e 's/^MONGODB_INITDB_ROOT_USERNAME=/        username: /' \
		-e 's/^MONGODB_INITDB_ROOT_PASSWORD=/        password: /'
}
