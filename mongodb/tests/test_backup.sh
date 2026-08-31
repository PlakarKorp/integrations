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

test_backup_basic() {
	local testname="backup_basic"
	local testroot=`test_init "$testname"`

	run_mongosh "db.collection.updateOne( \
		{ name: 'Nestor' }, \
		{ \$set: { catchphrase: 'No Backups? Are you really nuts?' } } \
		)" > $testroot/stdout

	egrep -v '(insertedId|Count):' $testroot/stdout > $testroot/stdout.filtered

	cat > $testroot/stdout.expected <<EOF
{
  acknowledged: true,
}
EOF
	cmp -s $testroot/stdout.expected $testroot/stdout.filtered
	ret=$?
	if [ $ret -ne 0 ]; then
		diff -u $testroot/stdout.expected $testroot/stdout.filtered
		test_done "$testroot" "$ret"
		return 1
	fi

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

	run_plakar at "$testroot/backups" ls | \
		awk '{ print $1, $2 }' > $testroot/stdout
	echo "$timestamp $snapshot" > $testroot/stdout.expected
	cmp -s $testroot/stdout.expected $testroot/stdout
	ret=$?
	if [ $ret -ne 0 ]; then
		diff -u $testroot/stdout.expected $testroot/stdout
		test_done "$testroot" "$ret"
		return 1
	fi

	# A zero-size snapshot means the backup has failed somehow.
	run_plakar at "$testroot/backups" ls | \
		awk '{ print $3 }' > $testroot/stdout
	echo "0" > $testroot/stdout.unexpected
	cmp -s $testroot/stdout.unexpected $testroot/stdout
	ret=$?
	if [ $ret -eq 0 ]; then
		echo "zero bytes backed up in snapshot $snapshot" >&2
		ret=1
		test_done "$testroot" "$ret"
		return 1
	fi

	run_plakar at "$testroot/backups" ls / > $testroot/stdout

	# The file's modification timestamp depends on the current time.
	# Filter it out for expected output comparison's sake.
	sed 's/^[^ ]* //' < $testroot/stdout | \
		awk '{ print $1, $2, $3, $6 }' > $testroot/stdout.filtered

	echo "-rw-r--r-- 0 0 mongodb-backup.bson" > $testroot/stdout.expected
	cmp -s $testroot/stdout.expected $testroot/stdout.filtered
	ret=$?
	if [ $ret -ne 0 ]; then
		diff -u $testroot/stdout.expected $testroot/stdout.filtered
		test_done "$testroot" "$ret"
		return 1
	fi

	test_done "$testroot" "$ret"
}

test_parseargs "$@"
run_test test_backup_basic
exit $test_status
