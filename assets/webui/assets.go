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

package webuiassets

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
)

// FS WebUI嵌入静态文件
//
//go:embed index.html ofdgo.css ofdgo.js ofdgo.sw.js ofdgo.wasm wasm_exec.js
var FS embed.FS

// Checksum 计算WebUI嵌入资源校验值
// 返回: string SHA-256十六进制校验值
func Checksum() string {
	hash := sha256.New()
	entries, _ := FS.ReadDir(".")
	for _, entry := range entries {
		data, _ := FS.ReadFile(entry.Name())
		hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
