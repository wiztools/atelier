#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

gofmt -w *.go
# The frontend builds before the Go suite: main.go embeds frontend/dist, so
# this is what the test binary compiles against.
npm run build --prefix frontend
go test ./...
wails build

echo
echo "Built ./build/bin/Atelier.app"
