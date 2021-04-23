# Docker utilities

`data.tar` was created like so:

```
mkdir data
# 65532 is the uid/gid of the nonroot user in the distroless base image.
tar --numeric-owner --owner=65532 --group=65532 --same-permissions -cf data.tar data
```
