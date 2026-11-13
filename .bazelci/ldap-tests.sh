#!/bin/bash

set -v
set -e
set -u
set -o pipefail

SRC_ROOT=$(dirname "$0")/..
SRC_ROOT=$(realpath "$SRC_ROOT")
cd "$SRC_ROOT"

HTTP_PORT=8089
GRPC_PORT=9092

[ -f bazel-remote ] || ./linux-build.sh

[ -f glauth-linux-amd64 ] || wget https://github.com/glauth/glauth/releases/download/v2.3.2/glauth-linux-amd64
chmod +x glauth-linux-amd64

tmpdir=$(mktemp -d bazel-remote-ldap-tests.XXXXXXX --tmpdir=${TMPDIR:-/tmp})

go build -o "$tmpdir/grpcreadclient" ./utils/grpcreadclient

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
	--grpc_address "0.0.0.0:$GRPC_PORT" \
	--enable_endpoint_metrics \
	--ldap.url ldap://127.0.0.1:3893 \
	--ldap.base_dn dc=glauth,dc=com \
	--ldap.bind_user "$BIND_USER_NAME" \
	--ldap.bind_password "$BIND_USER_PASSWORD" \
	--ldap.timeout 2s &
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

# Check that gRPC requests without credentials are refused.
if ! ./grpcreadclient -server-addr "127.0.0.1:$GRPC_PORT"
then
	echo "Error: gRPC requests without credentials were not refused."
	kill -9 $bazel_remote_pid $glauth_pid
	exit 1
fi

# Check that gRPC reads and writes work with valid LDAP credentials.
if ! ./grpcreadclient -server-addr "127.0.0.1:$GRPC_PORT" \
	-basic-auth-user "$END_USER_NAME" -basic-auth-pass "$END_USER_PASSWORD"
then
	echo "Error: gRPC requests with valid LDAP credentials failed."
	kill -9 $bazel_remote_pid $glauth_pid
	exit 1
fi

# Check that gRPC requests with a wrong password are refused.
if ./grpcreadclient -server-addr "127.0.0.1:$GRPC_PORT" \
	-basic-auth-user "$END_USER_NAME" -basic-auth-pass "wrong-password" \
	> grpc_wrong_password.log 2>&1
then
	cat grpc_wrong_password.log
	echo "Error: gRPC requests with a wrong password were not refused."
	kill -9 $bazel_remote_pid $glauth_pid
	exit 1
fi
if ! grep -q Unauthenticated grpc_wrong_password.log
then
	cat grpc_wrong_password.log
	echo "Error: gRPC requests with a wrong password failed for another reason."
	kill -9 $bazel_remote_pid $glauth_pid
	exit 1
fi

# Pause the LDAP server: it still accepts connections but never answers.
# A login that needs LDAP must be refused after --ldap.timeout instead of
# waiting indefinitely.
kill -STOP $glauth_pid

SECONDS=0
if wget --inet4-only -O - --timeout=30 --tries=1 \
	--http-user "$END_USER_NAME" --http-password "uncached-password-hang" \
	"http://127.0.0.1:$HTTP_PORT/status"
then
	echo "Error: uncached credentials were accepted while LDAP was not answering."
	kill -9 $bazel_remote_pid $glauth_pid
	exit 1
fi
hang_seconds=$SECONDS
if ! ps -p $bazel_remote_pid > /dev/null
then
	echo "Error: bazel-remote exited when LDAP was not answering."
	kill -9 $glauth_pid
	exit 1
fi
if [ $hang_seconds -gt 10 ]
then
	echo "Error: a login took ${hang_seconds}s to be refused while LDAP was not answering; --ldap.timeout is 2s."
	kill -9 $bazel_remote_pid $glauth_pid
	exit 1
fi

# Stop the LDAP server. bazel-remote must keep running: credentials it has
# already verified keep working, and new ones are refused.
kill -9 $glauth_pid
wait $glauth_pid 2>/dev/null || true

if ! wget --inet4-only -O - \
	--http-user "$END_USER_NAME" --http-password "$END_USER_PASSWORD" \
	"http://127.0.0.1:$HTTP_PORT/status"
then
	echo "Error: cached LDAP credentials were refused while LDAP was down."
	kill -9 $bazel_remote_pid
	exit 1
fi

# These credentials have not been checked yet, so LDAP must be queried.
if wget --inet4-only -O - \
	--http-user "$END_USER_NAME" --http-password "uncached-password-http" \
	"http://127.0.0.1:$HTTP_PORT/status"
then
	echo "Error: uncached credentials were accepted while LDAP was down."
	kill -9 $bazel_remote_pid
	exit 1
fi
if ! ps -p $bazel_remote_pid > /dev/null
then
	echo "Error: bazel-remote exited when LDAP was unavailable for an HTTP request."
	exit 1
fi

if ./grpcreadclient -server-addr "127.0.0.1:$GRPC_PORT" \
	-basic-auth-user "$END_USER_NAME" -basic-auth-pass "uncached-password-grpc" \
	> grpc_ldap_down.log 2>&1
then
	cat grpc_ldap_down.log
	echo "Error: gRPC requests were not refused while LDAP was down."
	kill -9 $bazel_remote_pid
	exit 1
fi
if ! ps -p $bazel_remote_pid > /dev/null
then
	cat grpc_ldap_down.log
	echo "Error: bazel-remote exited when LDAP was unavailable for a gRPC request."
	exit 1
fi
if ! grep -q Unauthenticated grpc_ldap_down.log
then
	cat grpc_ldap_down.log
	echo "Error: gRPC requests failed for another reason while LDAP was down."
	kill -9 $bazel_remote_pid
	exit 1
fi

echo LDAP tests passed, cleaning up...
kill -9 $bazel_remote_pid
cd "$SRC_ROOT"
rm -rf "$tmpdir"
