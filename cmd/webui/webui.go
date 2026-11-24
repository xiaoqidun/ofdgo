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
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xiaoqidun/ofdgo/assets/webui"
	"github.com/xiaoqidun/ofdgo/assets/webui/wasm"
	"github.com/xiaoqidun/ofdgo/internal/build"
)

// webuiCSP 定义WebUI静态服务内容安全策略，支持WASM与离线Worker并禁止外域脚本与插件
const webuiCSP = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' blob: data:; font-src 'self' blob: data:; media-src 'self' blob: data:; connect-src 'self' blob: data: https: http:; worker-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "listen address")
	flag.Parse()
	if err := runServer(*listen); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runServer 启动带超时配置和优雅关闭的WebUI服务
// 入参: addr 监听地址
// 返回: error 启动或关闭错误
func runServer(addr string) error {
	handler, err := serveWebUI()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		return errors.Join(shutdownErr, <-serveErr)
	}
}

// serveWebUI 创建WebUI静态文件处理器并统一附加安全响应头
// 返回: http.Handler HTTP处理器, error 资源读取错误
func serveWebUI() (http.Handler, error) {
	files := http.FileServerFS(webuiassets.FS)
	wasm := http.FileServerFS(wasmassets.FS)
	checksum, err := resourceChecksum()
	if err != nil {
		return nil, err
	}
	serviceWorker, err := webuiassets.FS.ReadFile("ofdgo_work.js")
	if err != nil {
		return nil, err
	}
	serviceWorker = append([]byte(fmt.Sprintf("const BUNDLE_CHECKSUM = %q;\n", checksum)), serviceWorker...)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", webuiCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-OFDGo-Checksum", checksum)
		w.Header().Set("X-OFDGo-Build-Version", build.Version)
		if r.URL.Path == "/ofdgo.wasm" {
			w.Header().Set("Content-Type", "application/wasm")
			wasm.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/ofdgo_work.js" {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			http.ServeContent(w, r, "ofdgo_work.js", time.Time{}, bytes.NewReader(serviceWorker))
			return
		}
		files.ServeHTTP(w, r)
	}), nil
}

// resourceChecksum 计算WebUI界面与WASM引擎的资源校验值
// 返回: string SHA-256十六进制校验值, error 资源读取错误
func resourceChecksum() (string, error) {
	hash := sha256.New()
	for _, resources := range []embed.FS{webuiassets.FS, wasmassets.FS} {
		entries, err := resources.ReadDir(".")
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			data, err := resources.ReadFile(entry.Name())
			if err != nil {
				return "", err
			}
			hash.Write(data)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
