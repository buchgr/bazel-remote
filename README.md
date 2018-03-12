test

![Build status](https://badge.buildkite.com/c11240e6e9519111f2380dfcf5fcb49e69fd5b2326c11a3059.svg?branch=master)

# A remote build cache for [Bazel](https://bazel.build)

bazel-remote is a HTTP/1.1 server that is intended to be used as a remote build cache for
[Bazel](https://bazel.build). The cache contents are stored in a directory on disk. One can specify a maximum cache
size and bazel-remote will automatically enforce this limit and clean the cache by deleting files based on their
last access time. The cache supports HTTP basic authentication with usernames and passwords being specified by a
`.htpasswd` file.

## Using bazel-remote

```
Usage of ./bazel-remote:
  -dir string
	Directory path where to store the cache contents. This flag is required.
  -host string
	Address to listen on. Listens on all network interfaces by default.
  -htpasswd_file string
	Path to a .htpasswd file. This flag is optional. Please read
	https://httpd.apache.org/docs/2.4/programs/htpasswd.html.
  -max_size int
	The maximum size of the remote cache in GiB. This flag is required. (default -1)
  -port int
	The port the HTTP server listens on (default 8080)
  -tls_cert_file string
	Path to a PEM encoded certificate file.  Required if tls_enabled is set to true.
  -tls_enabled
	Bool specifying whether or not to start the server with tls.  If true, server_cert and server_key flags
	are requred.
  -tls_key_file string
	Path to a PEM encoded key file.  Required if tls_enabled is set to true.
```

## Docker

### Prebuilt Image

We publish docker images to [DockerHub](https://hub.docker.com/r/buchgr/bazel-remote-cache/) that you can use with
`docker run`. The below command will start the remote cache on port `9090` with the default maximum cache size of
`5 GiB`.

```bash
$ docker pull buchgr/bazel-remote-cache
$ docker run -v /path/to/cache/dir:/data -p 9090:8080 buchgr/bazel-remote-cache
```

Note that you will need to change `/path/to/cache/dir` to a valid directory where the docker container can write to
and read from. If you want the docker container to run in the background pass the `-d` flag right after `docker run`.

You can change the maximum cache size by appending the `--max_size=N` flag with `N` being the max. size in Gibibytes.

### Build your own

The below command will build a docker image from source and install it into your local docker registry.

```bash
$ bazel run :bazel-remote-image
```

## Build a standalone Linux binary

```bash
$ bazel build :bazel-remote
```

### Authentication

In order to pass a `.htpasswd` and/or server key file(s) to the cache inside a docker container, you first need
to mount the file in the container and pass the path to the cache. The below example also configures TLS which is technically optional but highly recommended in order to not send passwords in plain text.

```bash
$ docker run -v /path/to/cache/dir:/data \
-v /path/to/htpasswd:/etc/bazel-remote/htpasswd \
-v /path/to/server_cert:/etc/bazel-remote/server_cert \
-v /path/to/server_key:/etc/bazel-remote/server_key \
-p 9090:8080 buchgr/bazel-remote-cache --tls_enabled=true \
--tls_cert_file=/etc/bazel-remote/server_cert --tls_key_file=/etc/bazel-remote/server_key \
--htpasswd_file /etc/bazel-remote/htpasswd --max_size=5
```

## Configuring Bazel

Please take a look at Bazel's documentation section on [remote
caching](https://docs.bazel.build/versions/master/remote-caching.html#run-bazel-using-the-remote-cache)
