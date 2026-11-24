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
	"fmt"
	"os"
	"strings"
)

// commandLineUsage 描述桌面端支持的合法命令行参数格式
const commandLineUsage = "用法: ofdgo [--data-dir <目录>] [--] [文件...]"

// commandLine 读取桌面启动参数并提取文件路径与数据目录
// 返回: []string 文件路径, string 浏览器数据目录, error 参数解析错误
func commandLine() ([]string, string, error) {
	return parseCommandLine(os.Args[1:])
}

// parseCommandLine 解析桌面启动参数，遇到未知参数或缺失参数值时报错
// 入参: args 命令行参数切片
// 返回: []string 文件路径, string 浏览器数据目录, error 参数解析错误
func parseCommandLine(args []string) ([]string, string, error) {
	var paths []string
	var dataDir string
	for len(args) > 0 {
		arg := args[0]
		switch {
		case arg == "--":
			return append(paths, args[1:]...), dataDir, nil
		case arg == "--data-dir":
			if len(args) < 2 || args[1] == "" || strings.HasPrefix(args[1], "-") {
				return nil, "", fmt.Errorf("参数 --data-dir 缺少目录路径\n%s", commandLineUsage)
			}
			dataDir, args = args[1], args[1:]
		case strings.HasPrefix(arg, "--data-dir="):
			value := strings.TrimPrefix(arg, "--data-dir=")
			if value == "" {
				return nil, "", fmt.Errorf("参数 --data-dir 缺少目录路径\n%s", commandLineUsage)
			}
			dataDir = value
		case arg == "-h" || arg == "--help":
			return nil, "", fmt.Errorf("%s", commandLineUsage)
		case strings.HasPrefix(arg, "-"):
			if info, err := os.Stat(arg); err == nil && !info.IsDir() {
				paths = append(paths, arg)
			} else {
				return nil, "", fmt.Errorf("未知参数: %s\n%s", arg, commandLineUsage)
			}
		default:
			paths = append(paths, arg)
		}
		args = args[1:]
	}
	return paths, dataDir, nil
}
