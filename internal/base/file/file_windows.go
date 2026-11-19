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

package file

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// replaceFileProc 保留目标文件属性并替换文件内容
var replaceFileProc = syscall.NewLazyDLL("kernel32.dll").NewProc("ReplaceFileW")

// Open 保留替换共享权限，路径替换后已有读取句柄仍有效
// 入参: name 文件路径
// 返回: *os.File 文件, error 打开错误
func Open(name string) (*os.File, error) {
	longPath, err := nativePath(name)
	if err != nil {
		return nil, err
	}
	path, err := syscall.UTF16PtrFromString(longPath)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(path, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	return os.NewFile(uintptr(handle), name), nil
}

// Replace 替换目标，目标不存在时重命名
// 入参: source 临时路径, target 目标路径
// 返回: error 替换错误
func Replace(source, target string) error {
	fromPath, err := nativePath(source)
	if err != nil {
		return err
	}
	toPath, err := nativePath(target)
	if err != nil {
		return err
	}
	from, err := syscall.UTF16PtrFromString(fromPath)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(toPath)
	if err != nil {
		return err
	}
	result, _, err := replaceFileProc.Call(uintptr(unsafe.Pointer(to)), uintptr(unsafe.Pointer(from)), 0, 0, 0, 0)
	if result != 0 {
		return nil
	}
	if err == syscall.ERROR_FILE_NOT_FOUND {
		return os.Rename(fromPath, toPath)
	}
	return &os.LinkError{Op: "replace", Old: source, New: target, Err: err}
}

// nativePath 转换绝对长路径
// 入参: name 路径
// 返回: string 原生长路径, error 转换错误
func nativePath(name string) (string, error) {
	if strings.HasPrefix(name, `\\?\`) {
		return name, nil
	}
	path, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + path[2:], nil
	}
	return `\\?\` + path, nil
}
