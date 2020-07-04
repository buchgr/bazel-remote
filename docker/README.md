# Docker utilities

`data.tar` was created like so:

```
mkdir data
tar --numeric-owner --owner=1000 --group=1000 --same-permissions -cf data.tar data
```

`Dockerfile` can be used if you want to build docker images without
using Bazel.
