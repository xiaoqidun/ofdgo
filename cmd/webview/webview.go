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

//go:build (windows && amd64) || (linux && !android && (amd64 || arm64))

package main

import (
	"os"
	"strings"
)

// commandLine 读取桌面参数并处理参数错误
// 返回: []string 文件路径, string 浏览器数据目录
func commandLine() ([]string, string) {
	paths, dataDir, ok := parseArguments(os.Args[1:])
	if !ok {
		os.Exit(2)
	}
	return paths, dataDir
}

// parseArguments 解析双横线选项及后续文件路径
// 入参: args 启动参数
// 返回: []string 文件路径, string 浏览器数据目录, bool 参数是否有效
func parseArguments(args []string) ([]string, string, bool) {
	var dataDir string
	for len(args) > 0 {
		arg := args[0]
		switch {
		case arg == "--":
			return args[1:], dataDir, true
		case arg == "--data-dir":
			if len(args) < 2 || strings.HasPrefix(args[1], "-") {
				return nil, dataDir, false
			}
			dataDir, args = args[1], args[1:]
		case strings.HasPrefix(arg, "--data-dir="):
			dataDir = strings.TrimPrefix(arg, "--data-dir=")
		case strings.HasPrefix(arg, "-"):
			return nil, dataDir, false
		default:
			return args, dataDir, true
		}
		args = args[1:]
	}
	return nil, dataDir, true
}
