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
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/xiaoqidun/ofdgo/assets/webui"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "listen address")
	flag.Parse()
	if err := http.ListenAndServe(*listen, serveWebUI()); err != nil {
		os.Exit(1)
	}
}

// serveWebUI 创建WebUI静态文件处理器
// 返回: http.Handler HTTP处理器
func serveWebUI() http.Handler {
	files := http.FileServerFS(webuiassets.FS)
	checksum := webuiassets.Checksum()
	serviceWorker, _ := webuiassets.FS.ReadFile("ofdgo.sw.js")
	serviceWorker = append([]byte(fmt.Sprintf("const BUNDLE_CHECKSUM = %q;\n", checksum)), serviceWorker...)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-OFDGo-Checksum", checksum)
		if r.URL.Path == "/ofdgo.sw.js" {
			http.ServeContent(w, r, "ofdgo.sw.js", time.Time{}, bytes.NewReader(serviceWorker))
			return
		}
		files.ServeHTTP(w, r)
	})
}
