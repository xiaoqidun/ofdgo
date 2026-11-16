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

package webview

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xiaoqidun/ofdgo"
	basefile "github.com/xiaoqidun/ofdgo/internal/base/file"
)

// fileEntry 保存窗口授权的文件或目录
type fileEntry struct {
	path       string
	name       string
	data       []byte
	file       *os.File
	stop       func() bool
	directory  bool
	owned      bool
	temporary  bool
	saveTarget ofdgo.Output
	readers    int
	retired    bool
	dropHandle string
}

// fileStore 管理窗口范围内的文件授权和临时数据
type fileStore struct {
	mu        sync.Mutex
	entries   map[string]*fileEntry
	writers   map[string]*fileWriter
	temporary string
	closed    bool
}

// fileWriter 在同目录临时文件中保存输出
type fileWriter struct {
	id     string
	store  *fileStore
	output ofdgo.WriteTransaction
}

// newFileStore 创建独立文件授权表
// 返回: *fileStore 文件授权表, error 创建错误
func newFileStore() (*fileStore, error) {
	dir, err := temporaryDirectory()
	if err != nil {
		return nil, err
	}
	return &fileStore{entries: make(map[string]*fileEntry), writers: make(map[string]*fileWriter), temporary: dir}, nil
}

// temporaryDirectory 创建独立临时目录
// 返回: string 目录路径, error 创建错误
func temporaryDirectory() (string, error) {
	for i := 0; i < 100; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(10000000))
		if err != nil {
			return "", err
		}
		path := filepath.Join(os.TempDir(), fmt.Sprintf("ofdgo-%07d", n.Int64()))
		err = os.Mkdir(path, 0700)
		if os.IsExist(err) {
			continue
		}
		return path, err
	}
	return "", os.ErrExist
}

// temporaryFile 创建临时文件
// 入参: dir 所在目录, prefix 文件名前缀
// 返回: *os.File 临时文件, error 创建错误
func temporaryFile(dir, prefix string) (*os.File, error) {
	for i := 0; i < 100; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(10000000))
		if err != nil {
			return nil, err
		}
		path := filepath.Join(dir, fmt.Sprintf("%s%07d.tmp", prefix, n.Int64()))
		file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			continue
		}
		return file, err
	}
	return nil, os.ErrExist
}

// add 登记已有授权文件
// 入参: name 文件路径, directory 是否为目录
// 返回: map[string]any 句柄描述, error 登记错误
func (s *fileStore) add(name string, directory bool) (map[string]any, error) {
	info, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	if info.IsDir() != directory {
		return nil, fmt.Errorf("文件类型不匹配")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := rand.Text()
	entry := &fileEntry{path: name, name: filepath.Base(name), directory: directory}
	if !directory {
		entry.saveTarget, err = ofdgo.NewFileOutput(name)
		if err != nil {
			return nil, err
		}
	}
	s.entries[id] = entry
	return s.descriptor(id), nil
}

// addDrop 登记拖放文件及读取快照，失败时撤销本次授权
// 入参: name 拖放文件路径
// 返回: map[string]any 文件描述, error 登记错误
func (s *fileStore) addDrop(name string) (map[string]any, error) {
	handle, err := s.add(name, false)
	if err != nil {
		return nil, err
	}
	id := handle["nativeHandle"].(string)
	file, err := s.file(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil && !s.closed {
		if entry := s.entries[file["nativeFile"].(string)]; entry != nil && s.entries[id] != nil {
			entry.dropHandle = id
			file["drop"] = true
			return file, nil
		}
		err = os.ErrNotExist
	}
	delete(s.entries, id)
	if file != nil {
		key := file["nativeFile"].(string)
		if entry := s.entries[key]; entry != nil {
			delete(s.entries, key)
			if entry.file != nil {
				entry.file.Close()
			}
		}
	}
	if err == nil {
		err = os.ErrClosed
	}
	return nil, err
}

// discardDrop 撤销未接收的拖放文件及其授权，不删除源文件
// 入参: id 拖放读取快照标识
func (s *fileStore) discardDrop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[id]
	if entry == nil || entry.dropHandle == "" {
		return
	}
	delete(s.entries, entry.dropHandle)
	delete(s.entries, id)
	entry.retired = true
	if entry.file != nil && entry.readers == 0 {
		entry.file.Close()
	}
}

// save 登记用户选择的输出路径
// 入参: name 输出文件路径
// 返回: map[string]any 句柄描述
func (s *fileStore) save(name string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := rand.Text()
	s.entries[id] = &fileEntry{path: name, name: filepath.Base(name)}
	return s.descriptor(id)
}

// descriptor 返回文件句柄，不暴露本机路径
// 入参: id 授权标识
// 返回: map[string]any 句柄描述
func (s *fileStore) descriptor(id string) map[string]any {
	entry := s.entries[id]
	kind := "file"
	if entry.directory {
		kind = "directory"
	}
	return map[string]any{"nativeHandle": id, "name": entry.name, "kind": kind}
}

// file 创建独立读取快照并返回资源描述
// 入参: id 授权标识
// 返回: map[string]any 资源描述, error 读取错误
func (s *fileStore) file(id string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[id]
	if entry == nil || entry.directory {
		return nil, fmt.Errorf("文件授权无效")
	}
	handle := s.descriptor(id)
	size := int64(len(entry.data))
	var modified int64
	if entry.data == nil {
		file, err := basefile.Open(entry.path)
		if err != nil {
			return nil, err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return nil, err
		}
		size = info.Size()
		modified = info.ModTime().UnixMilli()
		id = rand.Text()
		entry = &fileEntry{path: entry.path, name: entry.name, file: file}
		s.entries[id] = entry
	}
	return map[string]any{"nativeFile": id, "name": entry.name, "size": size, "lastModified": modified, "url": "/__native/data/" + id, "handle": handle}, nil
}

// reference 返回按需读取的文件描述，不创建或持有文件快照
// 入参: id 授权标识
// 返回: map[string]any 文件描述, error 授权或状态错误
func (s *fileStore) reference(id string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[id]
	if entry == nil || entry.directory {
		return nil, fmt.Errorf("文件授权无效")
	}
	info, err := os.Stat(entry.path)
	if err != nil {
		return nil, err
	}
	return map[string]any{"nativeFile": id, "name": entry.name, "size": info.Size(), "lastModified": info.ModTime().UnixMilli(), "url": "/__native/data/" + id, "handle": s.descriptor(id)}, nil
}

// resolve 按授权标识读取文件并保持随机读取句柄
// 入参: id 授权标识
// 返回: io.ReaderAt 随机读取器, int64 文件大小, error 读取错误
func (s *fileStore) resolve(id string) (io.ReaderAt, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[id]
	if entry == nil || entry.directory {
		return nil, 0, fmt.Errorf("文件授权无效")
	}
	if entry.data != nil {
		return bytes.NewReader(entry.data), int64(len(entry.data)), nil
	}
	if entry.file == nil {
		file, err := basefile.Open(entry.path)
		if err != nil {
			return nil, 0, err
		}
		entry.file = file
	}
	info, err := entry.file.Stat()
	if err != nil {
		return nil, 0, err
	}
	return entry.file, info.Size(), nil
}

// bytes 登记原生输出，页面退出时释放未读取数据
// 入参: ctx 页面上下文, data 输出数据
// 返回: map[string]any 资源描述，页面取消或授权表关闭时返回nil
func (s *fileStore) bytes(ctx context.Context, data []byte) map[string]any {
	if data == nil {
		data = []byte{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || ctx.Err() != nil {
		return nil
	}
	id := rand.Text()
	s.entries[id] = &fileEntry{data: data, name: "data", stop: context.AfterFunc(ctx, func() { s.release(id) })}
	return map[string]any{"nativeFile": id, "size": len(data), "name": "data", "url": "/__native/data/" + id, "temporary": true}
}

// resource 创建独立资源读取句柄
// 入参: id 文件标识
// 返回: io.ReaderAt 随机读取器, int64 文件大小, func() error 关闭函数, error 读取错误
func (s *fileStore) resource(id string) (io.ReaderAt, int64, func() error, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[id]
	if entry == nil || entry.directory {
		return nil, 0, nil, os.ErrNotExist
	}
	if entry.data != nil {
		return bytes.NewReader(entry.data), int64(len(entry.data)), nil, nil
	}
	if entry.file != nil && !entry.temporary {
		info, err := entry.file.Stat()
		if err != nil {
			return nil, 0, nil, err
		}
		entry.readers++
		var once sync.Once
		var closeErr error
		return entry.file, info.Size(), func() error {
			once.Do(func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				entry.readers--
				if entry.retired && entry.readers == 0 {
					closeErr = entry.file.Close()
				}
			})
			return closeErr
		}, nil
	}
	file, err := basefile.Open(entry.path)
	if err != nil {
		return nil, 0, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, nil, err
	}
	return file, info.Size(), file.Close, nil
}

// release 释放读取快照、内存结果或临时文件，保留在途读取及源文件
// 入参: id 资源标识
func (s *fileStore) release(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.entries[id]; entry != nil && (entry.data != nil || entry.temporary || entry.file != nil) {
		delete(s.entries, id)
		entry.retired = true
		if entry.stop != nil {
			entry.stop()
		}
		if entry.file != nil && entry.readers == 0 {
			entry.file.Close()
		}
		if entry.temporary {
			os.Remove(entry.path)
		}
	}
}

// child 在已授权目录中查找或创建直接子项
// 入参: id 目录授权标识, name 子项名称, directory 是否为目录, create 是否允许创建
// 返回: map[string]any 句柄描述, error 访问错误
func (s *fileStore) child(id, name string, directory, create bool) (map[string]any, error) {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "/\\:") {
		return nil, fmt.Errorf("文件名无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	parent := s.entries[id]
	if parent == nil || !parent.directory {
		return nil, fmt.Errorf("目录授权无效")
	}
	path := filepath.Join(parent.path, name)
	for key, entry := range s.entries {
		if entry.path == path && entry.directory == directory {
			if _, err := os.Stat(path); err == nil {
				return s.descriptor(key), nil
			}
		}
	}
	root, err := os.OpenRoot(parent.path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Stat(name)
	created := false
	if errors.Is(err, os.ErrNotExist) && create {
		if directory {
			err = root.Mkdir(name, 0755)
		} else {
			var file *os.File
			file, err = root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err == nil {
				err = file.Close()
			}
		}
		if err != nil {
			return nil, err
		}
		created = true
		info, err = root.Stat(name)
	}
	if err != nil {
		return nil, err
	}
	if info.IsDir() != directory {
		return nil, fmt.Errorf("文件类型不匹配")
	}
	key := rand.Text()
	s.entries[key] = &fileEntry{path: filepath.Join(parent.path, name), name: name, directory: directory, owned: created}
	return s.descriptor(key), nil
}

// reserve 为批量输出独占创建入口并避开已有文件
// 入参: id 目录授权标识, base 基础名称, extension 扩展名, directory 是否创建目录
// 返回: string 输出基础名称, string 输出入口名称, error 创建错误
func (s *fileStore) reserve(id, base, extension string, directory bool) (string, string, error) {
	if !fs.ValidPath(base) || base == "." || strings.ContainsAny(base+extension, "/\\:") {
		return "", "", fmt.Errorf("文件名无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	parent := s.entries[id]
	if parent == nil || !parent.directory {
		return "", "", fmt.Errorf("目录授权无效")
	}
	root, err := os.OpenRoot(parent.path)
	if err != nil {
		return "", "", err
	}
	defer root.Close()
	for suffix := 0; ; suffix++ {
		stem := base
		if suffix > 0 {
			stem = fmt.Sprintf("%s (%d)", base, suffix+1)
		}
		name := stem
		if !directory {
			name += "." + extension
		}
		if directory {
			err = root.Mkdir(name, 0755)
		} else {
			var file *os.File
			file, err = root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err == nil {
				err = file.Close()
				if err != nil {
					root.Remove(name)
				}
			}
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		s.entries[rand.Text()] = &fileEntry{path: filepath.Join(parent.path, name), name: name, directory: directory, owned: true}
		return stem, name, nil
	}
}

// begin 创建文件写入事务
// 入参: ctx 取消上下文, id 输出授权标识
// 返回: *fileWriter 事务写入器, error 创建错误
func (s *fileStore) begin(ctx context.Context, id string) (*fileWriter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	entry := s.entries[id]
	if entry == nil || entry.directory || entry.path == "" {
		return nil, fmt.Errorf("输出授权无效")
	}
	if entry.saveTarget == nil {
		var err error
		entry.saveTarget, err = ofdgo.NewFileOutput(entry.path)
		if err != nil {
			return nil, err
		}
	}
	output, err := entry.saveTarget.Begin(ctx)
	if err != nil {
		return nil, err
	}
	writer := &fileWriter{id: rand.Text(), store: s, output: output}
	s.writers[writer.id] = writer
	return writer, nil
}

// remove 清理由当前窗口创建的目录子项
// 入参: id 目录授权标识, name 子项名称, recursive 是否递归删除
// 返回: error 删除错误
func (s *fileStore) remove(id, name string, recursive bool) error {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "/\\:") {
		return fmt.Errorf("文件名无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	parent := s.entries[id]
	if parent == nil || !parent.directory {
		return fmt.Errorf("目录授权无效")
	}
	target := filepath.Join(parent.path, name)
	owned := false
	for _, entry := range s.entries {
		if entry.path == target && entry.owned {
			owned = true
			break
		}
	}
	if !owned {
		if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("只能清理本次创建的输出")
	}
	root, err := os.OpenRoot(parent.path)
	if err != nil {
		return err
	}
	defer root.Close()
	for key, entry := range s.entries {
		if entry.path == target || recursive && strings.HasPrefix(entry.path, target+string(filepath.Separator)) {
			entry.retired = true
			if entry.file != nil && entry.readers == 0 {
				entry.file.Close()
			}
			delete(s.entries, key)
		}
	}
	if recursive {
		return root.RemoveAll(name)
	}
	return root.Remove(name)
}

// Write 追加输出数据
// 入参: data 输出数据
// 返回: int 写入字节数, error 写入错误
func (w *fileWriter) Write(data []byte) (int, error) {
	return w.output.Write(data)
}

// Close 同步并替换目标文件
// 返回: error 提交错误
func (w *fileWriter) Close() error {
	defer w.release()
	if err := w.output.Commit(); err != nil {
		return errors.Join(err, w.output.Abort())
	}
	return nil
}

// Abort 删除未提交的临时输出
// 返回: error 清理错误
func (w *fileWriter) Abort() error {
	defer w.release()
	return w.output.Abort()
}

// release 移除已结束的写入事务
func (w *fileWriter) release() {
	w.store.mu.Lock()
	defer w.store.mu.Unlock()
	delete(w.store.writers, w.id)
}

// serve 提供窗口内资源，不建立网络监听器
// 入参: w 响应写入器, r 资源请求
func (s *fileStore) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !strings.HasPrefix(r.URL.Path, "/__native/data/") || r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/__native/data/")
	reader, size, err := s.resolve(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	entry := s.entries[id]
	s.mu.Unlock()
	if entry == nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, entry.name, time.Time{}, io.NewSectionReader(reader, 0, size))
}

// close 释放授权文件与未提交输出
// 返回: error 清理错误
func (s *fileStore) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	writers := s.writers
	entries := s.entries
	s.writers = make(map[string]*fileWriter)
	s.entries = make(map[string]*fileEntry)
	s.mu.Unlock()
	var err error
	for _, writer := range writers {
		err = errors.Join(err, writer.Abort())
	}
	for _, entry := range entries {
		if entry.stop != nil {
			entry.stop()
		}
		s.mu.Lock()
		entry.retired = true
		if entry.file != nil && entry.readers == 0 {
			err = errors.Join(err, entry.file.Close())
		}
		s.mu.Unlock()
	}
	return errors.Join(err, os.RemoveAll(s.temporary))
}
