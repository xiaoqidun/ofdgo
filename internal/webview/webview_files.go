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
	id         string
	path       string
	name       string
	size       int64
	modTime    int64
	data       []byte
	file       *os.File
	stop       func() bool
	directory  bool
	owned      bool
	temporary  bool
	releasable bool
	saveTarget ofdgo.Output
	readers    int
	retired    bool
	dropHandle string
}

// fileHolder 维护单个原生引擎会话持有的文件引用与调用状态
type fileHolder struct {
	entries     map[string]*fileEntry
	mainToken   string
	detectToken string
	importToken string
	activeCalls int
	packing     int
	pack        *packFileSlot
}

// fileStore 管理窗口范围内的文件授权和临时数据
type fileStore struct {
	mu        sync.Mutex
	entries   map[string]*fileEntry
	holders   map[string]*fileHolder
	writers   map[string]*fileWriter
	temporary string
	closed    bool
}

// newFileStore 创建独立文件授权表
// 返回: *fileStore 文件授权表, error 创建错误
func newFileStore() (*fileStore, error) {
	dir, err := temporaryDirectory()
	if err != nil {
		return nil, err
	}
	return &fileStore{entries: make(map[string]*fileEntry), holders: make(map[string]*fileHolder), writers: make(map[string]*fileWriter), temporary: dir}, nil
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
	if !directory && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("文件类型不匹配")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := rand.Text()
	entry := &fileEntry{id: id, path: name, name: filepath.Base(name), size: info.Size(), modTime: info.ModTime().UnixMilli(), directory: directory}
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
	entry := s.entries[id]
	if entry == nil || entry.dropHandle == "" || entry.retired {
		s.mu.Unlock()
		return
	}
	if drop := s.entries[entry.dropHandle]; drop != nil {
		drop.retired = true
		if drop.readers == 0 {
			delete(s.entries, entry.dropHandle)
		}
	}
	file, path := s.retireEntry(entry)
	s.mu.Unlock()
	if file != nil {
		_ = file.Close()
	}
	if path != "" {
		_ = os.Remove(path)
	}
}

// save 登记用户选择的输出路径
// 入参: name 输出文件路径
// 返回: map[string]any 句柄描述
func (s *fileStore) save(name string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := rand.Text()
	s.entries[id] = &fileEntry{id: id, path: name, name: filepath.Base(name)}
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

// file 创建按需读取的文件描述，选文件阶段不预先长驻打开句柄
// 入参: id 授权标识
// 返回: map[string]any 资源描述, error 读取错误
func (s *fileStore) file(id string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[id]
	if entry == nil || entry.directory || entry.retired {
		return nil, fmt.Errorf("文件授权无效")
	}
	handle := s.descriptor(id)
	size := int64(len(entry.data))
	var modified int64
	if entry.data == nil {
		if entry.temporary && entry.file != nil {
			info, err := entry.file.Stat()
			if err != nil {
				return nil, err
			}
			size = info.Size()
			modified = info.ModTime().UnixMilli()
		} else {
			info, err := os.Stat(entry.path)
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("文件类型不匹配")
			}
			size = info.Size()
			modified = info.ModTime().UnixMilli()
			id = rand.Text()
			entry = &fileEntry{id: id, path: entry.path, name: entry.name, size: size, modTime: modified, releasable: true}
			s.entries[id] = entry
		}
	}
	return map[string]any{"nativeFile": id, "name": entry.name, "size": size, "lastModified": modified, "url": "/__native/data/" + id, "handle": handle}, nil
}

// reference 返回按需读取的文件描述，不创建或持有文件快照
// 入参: id 授权标识
// 返回: map[string]any 文件描述, error 授权或状态错误
func (s *fileStore) reference(id string) (map[string]any, error) {
	return s.lazy(id, false)
}

// lazyFile 返回可释放的按需读取文件描述，枚举目录时不预先打开句柄
// 入参: id 授权标识
// 返回: map[string]any 文件描述, error 授权或状态错误
func (s *fileStore) lazyFile(id string) (map[string]any, error) {
	return s.lazy(id, true)
}

// lazy 生成按需读取的文件描述并隔离授权句柄
// 入参: id 授权标识, releasable 是否允许前端释放读取条目
// 返回: map[string]any 文件描述, error 授权或状态错误
func (s *fileStore) lazy(id string, releasable bool) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[id]
	if entry == nil || entry.directory || entry.retired {
		return nil, fmt.Errorf("文件授权无效")
	}
	info, err := os.Stat(entry.path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("文件类型不匹配")
	}
	size := info.Size()
	modified := info.ModTime().UnixMilli()
	entry.size, entry.modTime = size, modified
	handle := s.descriptor(id)
	if !releasable {
		return map[string]any{"nativeFile": id, "name": entry.name, "size": size, "lastModified": modified, "url": "/__native/data/" + id, "handle": handle}, nil
	}
	refID := rand.Text()
	s.entries[refID] = &fileEntry{id: refID, path: entry.path, name: entry.name, size: size, modTime: modified, releasable: true}
	return map[string]any{"nativeFile": refID, "name": entry.name, "size": size, "lastModified": modified, "url": "/__native/data/" + refID, "handle": handle}, nil
}

// openHolder 登记独立业务会话的文件持有表
// 入参: holder 会话标识
// 返回: error 状态错误
func (s *fileStore) openHolder(holder string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || holder == "" {
		return os.ErrClosed
	}
	if s.holders[holder] == nil {
		s.holders[holder] = &fileHolder{entries: make(map[string]*fileEntry)}
	}
	return nil
}

// beginHolderCall 登记会话调用作用域并在结束时释放非长驻文件句柄
// 入参: holder 会话标识, name 操作名称, args 操作参数
// 返回: func(bool) 幂等结束回调
func (s *fileStore) beginHolderCall(holder, name string, args []any) func(bool) {
	firstToken := nativeFileToken(args)
	var slot *packFileSlot
	s.mu.Lock()
	state := s.holders[holder]
	if state != nil && !s.closed {
		state.activeCalls++
		if name == "ofdgoPackFiles" {
			if state.pack == nil {
				state.pack = &packFileSlot{store: s}
			}
			slot = state.pack
			state.packing++
		}
	} else {
		state = nil
	}
	s.mu.Unlock()
	if state == nil {
		return func(bool) {}
	}
	var once sync.Once
	return func(ok bool) {
		once.Do(func() {
			var closeSlot *packFileSlot
			var files []*os.File
			var paths []string
			s.mu.Lock()
			current := s.holders[holder]
			if current == state {
				if name == "ofdgoPackFiles" && state.packing > 0 {
					state.packing--
					if state.packing == 0 {
						closeSlot = state.pack
						state.pack = nil
					}
				}
				if ok {
					switch name {
					case "ofdgoDetectFormat":
						if firstToken != "" {
							state.detectToken = firstToken
						}
					case "ofdgoOpen", "ofdgoOpenPDF":
						state.mainToken = firstToken
						state.detectToken = ""
						state.importToken = ""
					case "ofdgoCreateDocument":
						state.mainToken = ""
						state.detectToken = ""
						state.importToken = ""
					case "ofdgoLoadImport":
						state.importToken = firstToken
					}
				}
				state.activeCalls--
				if state.activeCalls == 0 {
					for token, entry := range state.entries {
						if token == state.mainToken || token == state.detectToken || token == state.importToken {
							continue
						}
						delete(state.entries, token)
						entry.readers--
						file, path := s.collectIdleEntry(entry)
						if file != nil {
							files = append(files, file)
						}
						if path != "" {
							paths = append(paths, path)
						}
					}
				}
			} else if slot != nil {
				closeSlot = slot
			}
			s.mu.Unlock()
			if closeSlot != nil {
				_ = closeSlot.close()
			}
			for _, file := range files {
				_ = file.Close()
			}
			for _, path := range paths {
				_ = os.Remove(path)
			}
		})
	}
}

// resolveHolder 按会话与授权标识读取文件并登记会话持有引用
// 入参: holder 会话标识, id 授权标识
// 返回: io.ReaderAt 随机读取器, int64 文件大小, error 读取错误
func (s *fileStore) resolveHolder(holder, id string) (io.ReaderAt, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, 0, os.ErrClosed
	}
	state, ok := s.holders[holder]
	if !ok || state == nil {
		return nil, 0, os.ErrClosed
	}
	if entry := state.entries[id]; entry != nil && entry.file != nil {
		info, err := entry.file.Stat()
		if err != nil {
			return nil, 0, err
		}
		return entry.file, info.Size(), nil
	}
	raw := s.entries[id]
	if raw == nil || raw.directory || raw.retired {
		return nil, 0, fmt.Errorf("文件授权无效")
	}
	if raw.data != nil {
		return bytes.NewReader(raw.data), int64(len(raw.data)), nil
	}
	if state.packing > 0 && state.pack != nil && !raw.temporary {
		info, err := os.Stat(raw.path)
		if err != nil {
			return nil, 0, err
		}
		if !info.Mode().IsRegular() {
			return nil, 0, fmt.Errorf("文件类型不匹配")
		}
		return packSlotReader{slot: state.pack, id: id}, info.Size(), nil
	}
	entry, size, err := s.openEntry(id)
	if err != nil {
		return nil, 0, err
	}
	state.entries[id] = entry
	entry.readers++
	return entry.file, size, nil
}

// closeHolder 释放指定会话持有的全部文件引用
// 入参: holder 会话标识
// 返回: error 关闭错误
func (s *fileStore) closeHolder(holder string) error {
	s.mu.Lock()
	state := s.holders[holder]
	delete(s.holders, holder)
	var files []*os.File
	var paths []string
	var pack *packFileSlot
	if state != nil {
		pack = state.pack
		state.pack = nil
		for _, entry := range state.entries {
			entry.readers--
			file, path := s.collectIdleEntry(entry)
			if file != nil {
				files = append(files, file)
			}
			if path != "" {
				paths = append(paths, path)
			}
		}
	}
	s.mu.Unlock()
	var err error
	if pack != nil {
		err = errors.Join(err, pack.close())
	}
	for _, file := range files {
		err = errors.Join(err, file.Close())
	}
	for _, path := range paths {
		_ = os.Remove(path)
	}
	return err
}

// acquire 获取单次文件读取引用及释放函数
// 入参: id 授权标识
// 返回: io.ReaderAt 随机读取器, int64 文件大小, func() error 释放函数, error 读取错误
func (s *fileStore) acquire(id string) (io.ReaderAt, int64, func() error, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, 0, nil, os.ErrClosed
	}
	entry, size, err := s.openEntry(id)
	if err != nil {
		return nil, 0, nil, err
	}
	if entry.data != nil {
		return bytes.NewReader(entry.data), size, func() error { return nil }, nil
	}
	entry.readers++
	var once sync.Once
	var closeErr error
	return entry.file, size, func() error {
		once.Do(func() { closeErr = s.releaseEntry(entry) })
		return closeErr
	}, nil
}

// openEntry 按授权标识准备可读文件或内存条目，调用方持有mu
// 入参: id 授权标识
// 返回: *fileEntry 文件条目, int64 数据大小, error 读取错误
func (s *fileStore) openEntry(id string) (*fileEntry, int64, error) {
	entry := s.entries[id]
	if entry == nil || entry.directory || entry.retired {
		return nil, 0, fmt.Errorf("文件授权无效")
	}
	if entry.data != nil {
		return entry, int64(len(entry.data)), nil
	}
	if entry.file == nil {
		file, err := basefile.Open(entry.path)
		if err != nil {
			return nil, 0, err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return nil, 0, err
		}
		if !info.Mode().IsRegular() {
			file.Close()
			return nil, 0, fmt.Errorf("文件类型不匹配")
		}
		entry.file = file
		entry.size = info.Size()
		entry.modTime = info.ModTime().UnixMilli()
		return entry, entry.size, nil
	}
	info, err := entry.file.Stat()
	if err != nil {
		return nil, 0, err
	}
	return entry, info.Size(), nil
}

// collectIdleEntry 在无在途读取时回收底层文件句柄，已退役条目同步从表中移除
// 入参: entry 文件条目
// 返回: *os.File 待关闭文件, string 待删除临时路径
func (s *fileStore) collectIdleEntry(entry *fileEntry) (*os.File, string) {
	if entry.readers != 0 {
		return nil, ""
	}
	var file *os.File
	var path string
	if entry.path != "" && !entry.temporary {
		file = entry.file
		entry.file = nil
	}
	if entry.retired {
		if entry.id != "" && s.entries[entry.id] == entry {
			delete(s.entries, entry.id)
		}
		entry.data = nil
		if file == nil {
			file = entry.file
			entry.file = nil
		}
		if entry.temporary {
			path = entry.path
		}
	}
	return file, path
}

// retireEntry 标记条目退役并在无在途读取时交出待清理资源，调用方持有mu
// 入参: entry 文件条目
// 返回: *os.File 待关闭文件, string 待删除临时路径
func (s *fileStore) retireEntry(entry *fileEntry) (*os.File, string) {
	entry.retired = true
	return s.collectIdleEntry(entry)
}

// releaseEntry 递减条目读取计数并在无在途读取时立即释放底层文件句柄
// 入参: entry 文件条目
// 返回: error 关闭错误
func (s *fileStore) releaseEntry(entry *fileEntry) error {
	s.mu.Lock()
	entry.readers--
	file, path := s.collectIdleEntry(entry)
	s.mu.Unlock()
	var err error
	if file != nil {
		err = file.Close()
	}
	if path != "" {
		_ = os.Remove(path)
	}
	return err
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
	s.entries[id] = &fileEntry{id: id, data: data, name: "data", stop: context.AfterFunc(ctx, func() { s.release(id) })}
	return map[string]any{"nativeFile": id, "size": len(data), "name": "data", "url": "/__native/data/" + id, "temporary": true}
}

// resource 创建独立资源读取句柄
// 入参: id 文件标识
// 返回: io.ReaderAt 随机读取器, int64 文件大小, func() error 关闭函数, error 读取错误
func (s *fileStore) resource(id string) (io.ReaderAt, int64, func() error, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[id]
	if entry == nil || entry.directory || entry.retired {
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
			once.Do(func() { closeErr = s.releaseEntry(entry) })
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
	entry := s.entries[id]
	if entry == nil || entry.retired || entry.data == nil && !entry.temporary && entry.file == nil && !entry.releasable {
		s.mu.Unlock()
		return
	}
	stop := entry.stop
	entry.stop = nil
	file, path := s.retireEntry(entry)
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if file != nil {
		_ = file.Close()
	}
	if path != "" {
		_ = os.Remove(path)
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
	if parent == nil || !parent.directory || parent.retired {
		return nil, fmt.Errorf("目录授权无效")
	}
	path := filepath.Join(parent.path, name)
	for key, entry := range s.entries {
		if !entry.retired && entry.path == path && entry.directory == directory {
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
	s.entries[key] = &fileEntry{id: key, path: filepath.Join(parent.path, name), name: name, size: info.Size(), modTime: info.ModTime().UnixMilli(), directory: directory, owned: created}
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
	if parent == nil || !parent.directory || parent.retired {
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
		key := rand.Text()
		s.entries[key] = &fileEntry{id: key, path: filepath.Join(parent.path, name), name: name, directory: directory, owned: true}
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
	if entry == nil || entry.directory || entry.retired || entry.path == "" {
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
	if parent == nil || !parent.directory || parent.retired {
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
	var files []*os.File
	for _, entry := range s.entries {
		if entry.path == target || recursive && strings.HasPrefix(entry.path, target+string(filepath.Separator)) {
			file, _ := s.retireEntry(entry)
			if file != nil {
				files = append(files, file)
			}
		}
	}
	for _, file := range files {
		_ = file.Close()
	}
	if recursive {
		return root.RemoveAll(name)
	}
	return root.Remove(name)
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
	s.mu.Lock()
	entry := s.entries[id]
	name := ""
	if entry != nil && !entry.retired {
		name = entry.name
	}
	s.mu.Unlock()
	reader, size, release, err := s.acquire(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer release()
	http.ServeContent(w, r, name, time.Time{}, io.NewSectionReader(reader, 0, size))
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
	var stops []func() bool
	var files []*os.File
	for _, entry := range entries {
		if entry.stop != nil {
			stops = append(stops, entry.stop)
			entry.stop = nil
		}
		file, _ := s.retireEntry(entry)
		if file != nil {
			files = append(files, file)
		}
	}
	s.mu.Unlock()
	var err error
	for _, writer := range writers {
		err = errors.Join(err, writer.Abort())
	}
	for _, stop := range stops {
		stop()
	}
	for _, file := range files {
		err = errors.Join(err, file.Close())
	}
	return errors.Join(err, os.RemoveAll(s.temporary))
}

// fileWriter 在同目录临时文件中保存输出
type fileWriter struct {
	id     string
	store  *fileStore
	output ofdgo.WriteTransaction
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

// packFileSlot 在单次打包任务中按顺序复用单个打开的文件句柄
type packFileSlot struct {
	mu      sync.Mutex
	store   *fileStore
	id      string
	reader  io.ReaderAt
	release func() error
}

// readAt 按需切换打包槽位文件并读取指定区间数据
// 入参: id 文件授权标识, data 目标缓冲区, offset 字节偏移
// 返回: int 读取字节数, error 读取或切换错误
func (p *packFileSlot) readAt(id string, data []byte, offset int64) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.id != id || p.reader == nil {
		if p.release != nil {
			_ = p.release()
			p.release = nil
			p.reader = nil
			p.id = ""
		}
		reader, _, release, err := p.store.acquire(id)
		if err != nil {
			return 0, err
		}
		p.id = id
		p.reader = reader
		p.release = release
	}
	return p.reader.ReadAt(data, offset)
}

// close 释放打包槽位当前持有的文件句柄
// 返回: error 关闭错误
func (p *packFileSlot) close() error {
	p.mu.Lock()
	release := p.release
	p.release = nil
	p.reader = nil
	p.id = ""
	p.mu.Unlock()
	if release != nil {
		return release()
	}
	return nil
}

// packSlotReader 将打包文件的随机读取转发到共享单槽缓存
type packSlotReader struct {
	slot *packFileSlot
	id   string
}

// ReadAt 从共享打包槽位读取指定位置的数据
// 入参: data 目标缓冲区, offset 字节偏移
// 返回: int 读取字节数, error 读取错误
func (r packSlotReader) ReadAt(data []byte, offset int64) (int, error) {
	return r.slot.readAt(r.id, data, offset)
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

// nativeFileToken 提取调用首参数中的原生文件标识
// 入参: args 调用参数列表
// 返回: string 文件授权标识，不存在时返回空字符串
func nativeFileToken(args []any) string {
	if len(args) == 0 {
		return ""
	}
	descriptor, ok := args[0].(map[string]any)
	if !ok {
		return ""
	}
	token, _ := descriptor["nativeFile"].(string)
	return token
}
