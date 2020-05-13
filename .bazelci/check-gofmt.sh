#!/bin/bash

# Source: https://github.com/golang/go/blob/88da9ccb98ffaf84bb06b98c9a24af5d0a7025d2/misc/git/pre-commit

unformatted=$(gofmt -l -s "$@")
[ -z "$unformatted" ] && exit 0

# Some files are not gofmt -s'ed. Print message and fail.

echo >&2 "Go files must be formatted with gofmt -s. Please run:"
for fn in $unformatted; do
	echo >&2 "  gofmt -s -w $fn"
done

exit 1
