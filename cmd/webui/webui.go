// Copyright 2025-2026 肖其顿 (XIAO QI DUN)
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !js || !wasm

package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/xiaoqidun/ofdgo/assets/webui"
	"github.com/xiaoqidun/ofdgo/assets/webui/wasm"
	"github.com/xiaoqidun/ofdgo/internal/build"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "listen address")
	flag.Parse()
	server := &http.Server{
		Addr:              *listen,
		Handler:           serveWebUI(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// serveWebUI 创建WebUI静态文件处理器
// 返回: http.Handler HTTP处理器
func serveWebUI() http.Handler {
	files := http.FileServerFS(webuiassets.FS)
	wasm := http.FileServerFS(wasmassets.FS)
	checksum := resourceChecksum()
	serviceWorker, _ := webuiassets.FS.ReadFile("ofdgo_work.js")
	serviceWorker = append([]byte(fmt.Sprintf("const BUNDLE_CHECKSUM = %q;\n", checksum)), serviceWorker...)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-OFDGo-Checksum", checksum)
		w.Header().Set("X-OFDGo-Build-Version", build.Version)
		if r.URL.Path == "/ofdgo.wasm" {
			wasm.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/ofdgo_work.js" {
			http.ServeContent(w, r, "ofdgo_work.js", time.Time{}, bytes.NewReader(serviceWorker))
			return
		}
		files.ServeHTTP(w, r)
	})
}

// resourceChecksum 计算WebUI界面与WASM引擎的资源校验值
// 返回: string SHA-256十六进制校验值
func resourceChecksum() string {
	hash := sha256.New()
	for _, resources := range []embed.FS{webuiassets.FS, wasmassets.FS} {
		entries, _ := resources.ReadDir(".")
		for _, entry := range entries {
			data, _ := resources.ReadFile(entry.Name())
			hash.Write(data)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}
