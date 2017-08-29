# bazel-remote
A remote cache for [Bazel](https://bazel.build) using HTTP/1.1.

The cache contents are stored in a directory on disk. One can specify a maximum cache size and bazel-remote will automatically enforce this limit and clean the cache by deleting files based on their last access time. The cache requires Bazel to use SHA256 as its hash function.

## Using bazel-remote
```
Usage of bazel-remote:
  -dir string
    	Directory path where to store the cache contents
  -max_size int
    	The maximum size of the remote cache in GiB (default -1)
  -port int
    	The port the HTTP server listens on (default 8080)
```

## Configuring Bazel
In order to set up Bazel for remote caching, it needs to be passed some special flags.

```
bazel 
  --host_jvm_args=-Dbazel.DigestFunction=sha256 
build
  --spawn_strategy=remote
  --strategy=Javac=remote
  --genrule_strategy=remote
  --remote_rest_cache=http://<BAZEL-REMOTE-HOST>:<PORT>
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
