#!/bin/bash

set -v
set -e
set -u
set -o pipefail

SRC_ROOT=$(dirname "$0")/..
SRC_ROOT=$(realpath "$SRC_ROOT")
cd "$SRC_ROOT"

HTTP_PORT=8089

[ -f bazel-remote ] || ./linux-build.sh

[ -f glauth-linux-amd64 ] || wget https://github.com/glauth/glauth/releases/download/v2.3.2/glauth-linux-amd64
chmod +x glauth-linux-amd64

tmpdir=$(mktemp -d bazel-remote-ldap-tests.XXXXXXX --tmpdir=${TMPDIR:-/tmp})
cd $tmpdir

BIND_USER_NAME="bazel-remote-ldap-user"
BIND_USER_PASSWORD="bazel-remote-ldap-password"
BIND_USER_PASSWORD_HASH=$(echo -n "$BIND_USER_PASSWORD" | sha256sum | cut -d' ' -f1)

END_USER_NAME="user-name"
END_USER_PASSWORD="user-password"
END_USER_PASSWORD_HASH=$(echo -n "$END_USER_PASSWORD" | sha256sum | cut -d' ' -f1)

# Based on https://github.com/glauth/glauth/blob/master/v2/sample-simple.cfg
cat << EOF > glauth.config
[ldap]
  enabled = true
  listen = "0.0.0.0:3893"
  tls = false

[ldaps]
  enabled = false

[tracing]
  enabled = false

[backend]
  datastore = "config"
  baseDN = "dc=glauth,dc=com"
  nameformat = "cn"
  groupformat = "ou"

# The users section contains a hardcoded list of valid users.
#   to create a passSHA256:   echo -n "mysecret" | openssl dgst -sha256

[[users]]
  name = "$BIND_USER_NAME"
  uidnumber = 5003
  primarygroup = 5502
  passsha256 = "$BIND_USER_PASSWORD_HASH"
    [[users.capabilities]]
    action = "search"
    object = "*"

[[users]]
  name = "$END_USER_NAME"
  uidnumber = 5001
  primarygroup = 5501
  passsha256 = "$END_USER_PASSWORD_HASH"
    [[users.capabilities]]
    action = "search"
    object = "ou=superheros,dc=glauth,dc=com"

EOF

"$SRC_ROOT/glauth-linux-amd64" -c glauth.config &
glauth_pid=$!
sleep 5

"$SRC_ROOT/bazel-remote" --dir data --max_size 1 --http_address "0.0.0.0:$HTTP_PORT" \
	--enable_endpoint_metrics \
	--ldap.url ldap://127.0.0.1:3893 \
	--ldap.base_dn dc=glauth,dc=com \
	--ldap.bind_user "$BIND_USER_NAME" \
	--ldap.bind_password "$BIND_USER_PASSWORD" &
bazel_remote_pid=$!

# Wait a bit for bazel-remote to start up...

running=false
for i in $(seq 1 20)
do
	sleep 1

	ps -p $bazel_remote_pid > /dev/null || break

	if wget --inet4-only -d -O - --timeout=2 \
		--http-user "$END_USER_NAME" --http-password "$END_USER_PASSWORD" \
		"http://127.0.0.1:$HTTP_PORT/status"
	then
		running=true
		break
	fi
done

if [ "$running" != true ]
then
	echo "Error: bazel-remote took too long to start"
	kill -9 $bazel_remote_pid $glauth_pid
	exit 1
fi

# Check that metrics are reachable with authentication.
wget --inet4-only -d -O - \
	--http-user "$END_USER_NAME" --http-password "$END_USER_PASSWORD" \
	http://127.0.0.1:$HTTP_PORT/metrics 2>&1 | tee authenticated_metrics.log

# Check that metrics are not reachable without authentication.
set +e
wget --inet4-only -d -O - \
	http://127.0.0.1:$HTTP_PORT/metrics > unauthenticated_metrics.log 2>&1
result=$?
if [ $result = 0 ]
then
	cat unauthenticated_metrics.log
	echo Error: should not have been able to fetch metrics without authentication.
	exit 1
fi
set -e


echo LDAP tests passed, cleaning up...
kill -9 $bazel_remote_pid $glauth_pid
cd "$SRC_ROOT"
rm -rf "$tmpdir"
