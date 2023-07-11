#!/usr/bin/env bash

set -euo pipefail

pkg=go1.20.6.linux-amd64.tar.gz

wget -o "$HOME/$pkg" "https://golang.org/dl/$pkg" 1>&2
tar -xv -C "$HOME" -f "$pkg" 1>&2
