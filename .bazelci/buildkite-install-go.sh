#!/usr/bin/env bash

set -euo pipefail

wget -o $HOME/go1.22.4.linux-amd64.tar.gz https://golang.org/dl/go1.22.4.linux-amd64.tar.gz 1>&2
tar -xv -C $HOME -f go1.22.4.linux-amd64.tar.gz 1>&2
