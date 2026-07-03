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
	"flag"
	"net/http"
	"os"
	"strings"

	webuiassets "github.com/xiaoqidun/ofdgo/assets/webui"
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
	files := http.FileServer(http.FS(webuiassets.FS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".wasm") {
			w.Header().Set("Content-Type", "application/wasm")
		}
		files.ServeHTTP(w, r)
	})
}
