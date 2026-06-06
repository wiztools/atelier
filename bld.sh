#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

gofmt -w *.go
go test ./...
npm run build --prefix frontend
wails build

echo
echo "Built ./build/bin/Atelier.app"
