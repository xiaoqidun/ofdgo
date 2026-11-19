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
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sync"
)

// ErrChanged 表示目标文件已被其他操作修改
var ErrChanged = errors.New("file changed since opening")

// ErrBusy 表示目标文件正在写入
var ErrBusy = errors.New("file save is in progress")

// activeFiles 协调当前进程内的目标写入
var activeFiles sync.Map

// Target 保存文件路径及最近确认的文件状态，可跨次写入复用
type Target struct {
	mu   sync.Mutex
	name string
	info os.FileInfo
}

// Writer 写入同目录临时文件，提交前检查取消及目标冲突
type Writer struct {
	mu     sync.Mutex
	target *Target
	file   *os.File
	ctx    context.Context
	err    error
	closed bool
}

// NewTarget 记录目标当前状态，允许目标尚不存在
// 入参: name 目标路径
// 返回: *Target 保存目标, error 路径或文件错误
func NewTarget(name string) (*Target, error) {
	if name == "" {
		return nil, os.ErrInvalid
	}
	name, err := nativePath(name)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if info != nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("save target is not a regular file")
	}
	return &Target{name: name, info: info}, nil
}

// Begin 创建独占写入事务，调用方必须提交或终止
// 入参: ctx 取消上下文
// 返回: *Writer 事务, error 创建错误
func (t *Target) Begin(ctx context.Context) (*Writer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, loaded := activeFiles.LoadOrStore(t.name, true); loaded {
		return nil, ErrBusy
	}
	if err := t.check(); err != nil {
		activeFiles.Delete(t.name)
		return nil, err
	}
	perm := os.FileMode(0600)
	if t.info == nil {
		perm = 0666
	}
	for range 100 {
		n, err := rand.Int(rand.Reader, big.NewInt(10000000))
		if err != nil {
			activeFiles.Delete(t.name)
			return nil, err
		}
		name := fmt.Sprintf("%s.%07d.tmp", t.name, n.Int64())
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			activeFiles.Delete(t.name)
			return nil, err
		}
		return &Writer{target: t, file: f, ctx: ctx}, nil
	}
	activeFiles.Delete(t.name)
	return nil, os.ErrExist
}

// Write 追加数据并记录写入错误
// 入参: data 数据
// 返回: int 写入长度, error 写入错误
func (w *Writer) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	if w.err == nil {
		w.err = w.ctx.Err()
	}
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}

// Commit 完成写入并替换目标，成功后刷新目标状态
// 返回: error 提交错误
func (w *Writer) Commit() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return os.ErrClosed
	}
	w.closed = true
	defer activeFiles.Delete(w.target.name)
	defer os.Remove(w.file.Name())
	w.target.mu.Lock()
	defer w.target.mu.Unlock()
	if err := errors.Join(w.err, w.ctx.Err(), w.target.check()); err != nil {
		return errors.Join(err, w.file.Close())
	}
	if w.target.info != nil {
		if err := w.file.Chmod(w.target.info.Mode().Perm()); err != nil {
			return errors.Join(err, w.file.Close())
		}
	}
	if err := w.file.Sync(); err != nil {
		return errors.Join(err, w.file.Close())
	}
	info, err := w.file.Stat()
	if err = errors.Join(err, w.file.Close(), w.ctx.Err()); err != nil {
		return err
	}
	if err := w.target.check(); err != nil {
		return err
	}
	if err := Replace(w.file.Name(), w.target.name); err != nil {
		return err
	}
	w.target.info = info
	if current, err := os.Stat(w.target.name); err == nil {
		w.target.info = current
	}
	return nil
}

// Abort 丢弃临时输出，不改变目标
// 返回: error 清理错误
func (w *Writer) Abort() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	defer activeFiles.Delete(w.target.name)
	return errors.Join(w.file.Close(), os.Remove(w.file.Name()))
}

// NewTemp 创建私有输入缓存文件，调用方负责关闭和删除
// 入参: directory 缓存目录，空值使用系统临时目录
// 返回: *os.File 缓存文件, error 创建错误
func NewTemp(directory string) (*os.File, error) {
	if directory == "" {
		directory = os.TempDir()
	}
	directory, err := nativePath(directory)
	if err != nil {
		return nil, err
	}
	for range 100 {
		n, err := rand.Int(rand.Reader, big.NewInt(10000000))
		if err != nil {
			return nil, err
		}
		name := filepath.Join(directory, fmt.Sprintf("ofdgo-%07d.tmp", n.Int64()))
		file, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return file, err
	}
	return nil, os.ErrExist
}

// check 检查目标是否保持最近确认的状态
// 返回: error 文件状态错误
func (t *Target) check() error {
	info, err := os.Lstat(t.name)
	if errors.Is(err, os.ErrNotExist) {
		if t.info == nil {
			return nil
		}
		return ErrChanged
	}
	if err != nil {
		return err
	}
	if t.info == nil || !os.SameFile(t.info, info) || t.info.Size() != info.Size() || !t.info.ModTime().Equal(info.ModTime()) {
		return ErrChanged
	}
	if info.Mode().Perm()&0222 == 0 {
		return os.ErrPermission
	}
	return nil
}
