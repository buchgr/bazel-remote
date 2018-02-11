# A remote build cache for [Bazel](https://bazel.build)

bazel-remote is a HTTP/1.1 server that is intended to be used as a remote build cache for [Bazel](https://bazel.build). The cache contents are stored in a directory on disk. One can specify a maximum cache size and bazel-remote will automatically enforce this limit and clean the cache by deleting files based on their last access time. The cache supports HTTP basic authentication with usernames and passwords being specified by a `.htpasswd` file.

## Build a standalone Linux binary
```
./linux-build.sh
```

## Using bazel-remote
```
Usage of ./bazel-remote:
  -dir string
    	Directory path where to store the cache contents. This flag is required.
  -host string
    	Address to listen on. Listens on all network interfaces by default.
  -htpasswd_file string
    	Path to a .htpasswd file. This flag is optional. Please read https://httpd.apache.org/docs/2.4/programs/htpasswd.html.
  -max_size int
    	The maximum size of the remote cache in GiB. This flag is required. (default -1)
  -port int
    	The port the HTTP server listens on (default 8080)
```

## Docker Image

You can also run the remote cache by pulling a prebuilt image from [DockerHub](https://hub.docker.com/r/buchgr/bazel-remote-cache/) and starting the docker container with `docker run`. This will start the remote cache on port `9090` with the default maximum cache size of `5 GiB`.

```bash
$ docker pull buchgr/bazel-remote-cache
$ docker run -v /path/to/cache/dir:/data -p 9090:8080 buchgr/bazel-remote-cache
```

Note that you will need to change `/path/to/cache/dir` to a valid directory where the docker container can write to and read from. If you want the docker container to run in the background pass the `-d` flag right after `docker run`.

You can change the maximum cache size by appending the `--max_size=N` flag with `N` being the max. size in Gibibytes.

### Authentication

In order to pass a `.htpasswd` file to the cache inside a docker container, you first need to mount the file in the container and pass the path to the cache. For example:

```bash
$ docker run -v /path/to/cache/dir:/data \
-v /path/to/htpasswd:/etc/bazel-remote/htpasswd \
-p 9090:8080 buchgr/bazel-remote-cache --htpasswd_file /etc/bazel-remote/htpasswd --max_size=5
```

## Configuring Bazel

Please take a look at Bazel's documentation section on [remote caching](https://docs.bazel.build/versions/master/remote-caching.html#run-bazel-using-the-remote-cache)

