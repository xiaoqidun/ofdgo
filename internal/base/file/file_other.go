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

//go:build !windows

package file

import (
	"os"
	"path/filepath"
)

// Open 打开文件，路径替换后已有读取句柄仍有效
// 入参: name 文件路径
// 返回: *os.File 文件, error 打开错误
func Open(name string) (*os.File, error) {
	return os.Open(name)
}

// Replace 替换目标路径
// 入参: source 临时路径, target 目标路径
// 返回: error 替换错误
func Replace(source, target string) error {
	return os.Rename(source, target)
}

// nativePath 转换绝对路径
// 入参: name 路径
// 返回: string 绝对路径, error 转换错误
func nativePath(name string) (string, error) {
	return filepath.Abs(name)
}
