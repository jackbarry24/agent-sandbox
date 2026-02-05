#!/usr/bin/env bash
set -euo pipefail

ID=${1:-demo}

go run ./cli/cmd/sbx create -id "$ID"
go run ./cli/cmd/sbx exec -id "$ID" -- bash -lc 'uname -a'
go run ./cli/cmd/sbx delete -id "$ID"
