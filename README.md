# bazel-remote
A remote cache for [Bazel](https://bazel.build) using HTTP/1.1.

The cache contents are stored in a directory on disk. One can specify a maximum cache size and bazel-remote will automatically enforce this limit and clean the cache by deleting files based on their last access time. The cache requires Bazel to use SHA256 as its hash function.

## Build standalone Linux binary
```
./build.sh
```

## Using bazel-remote
```
Usage of bazel-remote:
  -dir string
    	Directory path where to store the cache contents
  -max_size int
    	The maximum size of the remote cache in GiB (default -1)
  -port int
    	The port the HTTP server listens on (default 8080)
  -host addr
      Address to listen. Defaults to empty : listen on all network interfaces. Can be 'localhost' for example if we want to have a local server.
  -user string
      The expected user for basic authentication (default "")
  -pass string
      The expected password for basic authentication (default "") If unset, basic authentication is not checked.
```

## Docker Image

You can also run the remote cache by pulling a prebuilt image from DockerHub and starting the docker container with `docker run`. This will start the remote cache on port `9090` with the default maximum cache size of `5 GiB`.

```bash
$ docker pull buchgr/bazel-remote-cache
$ docker run -v /path/to/cache/dir:/data -p 9090:8080 buchgr/bazel-remote-cache
```

Note that you will need to change `/path/to/cache/dir` to a valid directory where the docker container can write to and read from. If you want the docker container to run in the background pass the `-d` flag right after `docker run`.

You can change the maximum cache size by appending the `--max_size=N` flag with `N` being the max. size in Gibibytes.

## Configuring Bazel
In order to set up Bazel for remote caching, it needs to be passed some special flags.

```
bazel
  --host_jvm_args=-Dbazel.DigestFunction=sha256
build
  --spawn_strategy=remote
  --strategy=Javac=remote
  --genrule_strategy=remote
  --remote_rest_cache=http://<[username:password]><BAZEL-REMOTE-HOST>:<PORT>
//foo:target
```

Specifying these flags on each Bazel invocation can be cumbersome and thus one can also add them to their `~/.bazelrc` file
```
startup --host_jvm_args=-Dbazel.DigestFunction=sha256
build --spawn_strategy=remote
build --strategy=Javac=remote
build --genrule_strategy=remote
build --remote_rest_cache=http://<BAZEL-REMOTE-HOST>:<PORT>
```
