#!/bin/bash
set -eu

echo "STABLE_GIT_COMMIT $(git rev-parse HEAD)"

git_tag_info=$(git tag --points-at HEAD | sort -h | paste -sd "," -)
echo "GIT_TAGS $git_tag_info"
