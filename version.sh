#!/usr/bin/env bash
# Print the entire-tail version: 1.YYYYMMDD.N
#
# YYYYMMDD is the local date of HEAD's commit and N is how many commits on HEAD
# were made that day, so the same source always gives the same version.
# Uncommitted changes to tracked files add -dirty. Outside a git checkout it
# prints 1.0.0-dev.

set -euo pipefail

if ! git rev-parse --verify -q HEAD >/dev/null 2>&1; then
  echo "1.0.0-dev"
  exit 0
fi

day="$(git log -1 --format=%cd --date=format-local:%Y%m%d HEAD)"
seq="$(git log --format=%cd --date=format-local:%Y%m%d HEAD | grep -c "^$day\$")"
version="1.$day.$seq"
if ! git diff --quiet HEAD --; then
  version="$version-dirty"
fi
echo "$version"
