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
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Open 打开文件，路径替换后已有读取句柄仍有效
// 入参: name 文件路径
// 返回: *os.File 文件, error 打开错误
func Open(name string) (*os.File, error) {
	return os.Open(name)
}

// replace 替换目标路径并同步父目录以提升断电一致性
// 入参: source 临时路径, target 目标路径
// 返回: error 替换或目录同步错误
func replace(source, target string) error {
	if err := os.Rename(source, target); err != nil {
		return err
	}
	return syncDir(filepath.Dir(target))
}

// nativePath 转换绝对路径
// 入参: name 路径
// 返回: string 绝对路径, error 转换错误
func nativePath(name string) (string, error) {
	return filepath.Abs(name)
}

// lockKey 生成POSIX下规整后的进程内互斥锁键
// 入参: name 绝对路径
// 返回: string 归一化锁键
func lockKey(name string) string {
	return filepath.Clean(name)
}

// syncDir 同步目标父目录元数据，忽略不支持目录同步的底层错误
// 入参: name 父目录路径
// 返回: error 同步错误
func syncDir(name string) error {
	dir, err := os.Open(name)
	if err != nil {
		if ignoreDirSyncErr(err) {
			return nil
		}
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil && !ignoreDirSyncErr(syncErr) {
		return errors.Join(syncErr, closeErr)
	}
	return closeErr
}

// ignoreDirSyncErr 判断是否为底层平台或文件系统不支持目录同步的可忽略错误
// 入参: err 底层错误
// 返回: bool 是否可忽略
func ignoreDirSyncErr(err error) bool {
	return errors.Is(err, errors.ErrUnsupported) ||
		errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.EBADF) ||
		errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.EROFS) ||
		errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EISDIR)
}
