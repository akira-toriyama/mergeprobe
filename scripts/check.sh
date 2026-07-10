#!/bin/sh
# Mirror of .github/workflows/ci.yml — keep the two in lockstep so that
# "green here == green CI".
set -eu

go build ./...
go vet ./...
go test -race ./...

echo "check.sh: all green"
