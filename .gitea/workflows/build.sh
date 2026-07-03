#!/bin/bash
set -e
export TZ=Asia/Shanghai
GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -o assets/webui/ofdgo.wasm -trimpath -ldflags "-s -w -buildid=" ./cmd/webui/wasm.go
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o main -trimpath -ldflags "-s -w -buildid=" ./cmd/webui/webui.go