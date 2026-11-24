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
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/xiaoqidun/ofdgo"
	basefile "github.com/xiaoqidun/ofdgo/internal/base/file"
)

// fileEntry 保存窗口授权的文件或目录
type fileEntry struct {
	id         string
	parentID   string
	path       string
	pathKey    string
	name       string
	size       int64
	modTime    int64
	data       []byte
	file       *os.File
	stop       func() bool
	directory  bool
	owned      bool
	temporary  bool
	readOnly   bool
	pinned     bool
	saveTarget ofdgo.Output
	readers    int
	reused     bool
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
	pathIndex map[string]string
	children  map[string]map[string]struct{}
	holders   map[string]*fileHolder
	writers   map[string]*fileWriter
	temporary string
	closed    bool
}

// fileWriter 在同目录临时文件中保存输出
type fileWriter struct {
	id      string
	entryID string
	store   *fileStore
	output  ofdgo.WriteTransaction
}

// packFileSlot 在单次打包任务中按顺序复用单个打开的文件句柄
type packFileSlot struct {
	mu      sync.Mutex
	store   *fileStore
	id      string
	reader  io.ReaderAt
	release func() error
}

// packSlotReader 将打包文件的随机读取转发到共享单槽缓存
type packSlotReader struct {
	slot *packFileSlot
	id   string
}

// newFileStore 创建独立文件授权表
// 返回: *fileStore 文件授权表, error 创建错误
func newFileStore() (*fileStore, error) {
	dir, err := temporaryDirectory()
	if err != nil {
		return nil, err
	}
	return &fileStore{entries: make(map[string]*fileEntry), pathIndex: make(map[string]string), children: make(map[string]map[string]struct{}), holders: make(map[string]*fileHolder), writers: make(map[string]*fileWriter), temporary: dir}, nil
}

// temporaryDirectory 创建独立临时目录
// 返回: string 目录路径, error 创建错误
func temporaryDirectory() (string, error) {
	return os.MkdirTemp("", "ofdgo-*")
}

// isNewImageDocument 判断图片导入参数是否为新建文档模式
// 入参: args 调用参数列表
// 返回: bool 插入位置为负数时返回true
func isNewImageDocument(args []any) bool {
	if len(args) <= 2 {
		return false
	}
	switch v := args[2].(type) {
	case float64:
		return v < 0
	case int:
		return v < 0
	case int64:
		return v < 0
	default:
		return false
	}
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

// normalizePath 生成用于路径索引的规范化键
// 入参: path 文件或目录路径
// 返回: string 规范化路径键
func normalizePath(path string) string {
	cleaned := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
}

// isSubPath 判断目标路径是否严格位于根目录之下
// 入参: root 根目录路径, target 目标路径
// 返回: bool 是否为有效子路径
func isSubPath(root, target string) bool {
	if root == "" || target == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil || rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// saveError 将文件保存冲突与权限错误转换为面向用户的中文提示
// 入参: err 原始错误
// 返回: error 中文提示错误
func saveError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ofdgo.ErrSaveConflict) {
		return errors.New("目标文件已被其他程序修改，请重新打开或另存为")
	}
	if errors.Is(err, ofdgo.ErrSaveBusy) {
		return errors.New("目标文件正在保存中，请稍后再试")
	}
	if errors.Is(err, os.ErrPermission) {
		return errors.New("没有写入该文件的权限")
	}
	return err
}

// add 登记已有授权文件
// 入参: name 文件路径, directory 是否为目录, writable 是否允许初始化保存目标
// 返回: map[string]any 句柄描述, error 登记错误
func (s *fileStore) add(name string, directory, writable bool) (map[string]any, error) {
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
	var target ofdgo.Output
	if !directory && writable {
		linfo, lerr := os.Lstat(name)
		if lerr != nil {
			return nil, lerr
		}
		if linfo.Mode().IsRegular() {
			target, err = ofdgo.NewFileOutput(name)
			if err != nil {
				return nil, err
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	id := rand.Text()
	entry := &fileEntry{id: id, path: name, name: filepath.Base(name), size: info.Size(), modTime: info.ModTime().UnixMilli(), directory: directory, readOnly: !directory && !writable, saveTarget: target}
	s.registerEntry(entry, true)
	return s.descriptor(id), nil
}

// addFont 登记常驻只读系统字体条目，不初始化保存目标
// 入参: name 字体文件路径, size 文件大小, modTime 修改时间毫秒戳
// 返回: map[string]any 文件描述, error 登记错误
func (s *fileStore) addFont(name string, size, modTime int64) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	id := rand.Text()
	entry := &fileEntry{id: id, path: name, name: filepath.Base(name), size: size, modTime: modTime, readOnly: true, pinned: true}
	s.registerEntry(entry, false)
	return map[string]any{"nativeFile": id, "name": entry.name, "size": size, "lastModified": modTime, "url": "/__native/data/" + id}, nil
}

// addDrop 登记拖放文件及读取快照，失败时撤销本次授权
// 入参: name 拖放文件路径
// 返回: map[string]any 文件描述, error 登记错误
func (s *fileStore) addDrop(name string) (map[string]any, error) {
	handle, err := s.add(name, false, true)
	if err != nil {
		return nil, err
	}
	id := handle["nativeHandle"].(string)
	file, err := s.file(id)
	var files []*os.File
	s.mu.Lock()
	if err == nil && !s.closed {
		if entry := s.entries[file["nativeFile"].(string)]; entry != nil && s.entries[id] != nil {
			entry.dropHandle = id
			file["drop"] = true
			s.mu.Unlock()
			return file, nil
		}
		err = os.ErrNotExist
	}
	if drop := s.entries[id]; drop != nil {
		if f, _ := s.retireEntry(drop); f != nil {
			files = append(files, f)
		}
	}
	if file != nil {
		key := file["nativeFile"].(string)
		if entry := s.entries[key]; entry != nil {
			if f, _ := s.retireEntry(entry); f != nil {
				files = append(files, f)
			}
		}
	}
	s.mu.Unlock()
	for _, f := range files {
		_ = f.Close()
	}
	if err == nil {
		err = os.ErrClosed
	}
	return nil, err
}

// discardDrop 撤销未接收的拖放文件及其授权，不删除源文件
// 入参: id 拖放读取快照标识
func (s *fileStore) discardDrop(id string) {
	var files []*os.File
	var path string
	s.mu.Lock()
	entry := s.entries[id]
	if entry == nil || entry.dropHandle == "" || entry.retired {
		s.mu.Unlock()
		return
	}
	if drop := s.entries[entry.dropHandle]; drop != nil {
		if f, _ := s.retireEntry(drop); f != nil {
			files = append(files, f)
		}
	}
	var file *os.File
	file, path = s.retireEntry(entry)
	if file != nil {
		files = append(files, file)
	}
	s.mu.Unlock()
	for _, f := range files {
		_ = f.Close()
	}
	if path != "" {
		_ = os.Remove(path)
	}
}

// save 登记用户选择的输出路径
// 入参: name 输出文件路径
// 返回: map[string]any 句柄描述, error 登记错误
func (s *fileStore) save(name string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	id := rand.Text()
	s.registerEntry(&fileEntry{id: id, path: name, name: filepath.Base(name)}, true)
	return s.descriptor(id), nil
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
			entry.size, entry.modTime = size, modified
			id = rand.Text()
			entry = &fileEntry{id: id, path: entry.path, name: entry.name, size: size, modTime: modified, readOnly: true}
			s.registerEntry(entry, false)
		}
	}
	return map[string]any{"nativeFile": id, "name": entry.name, "size": size, "lastModified": modified, "url": "/__native/data/" + id, "handle": handle}, nil
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
				switch name {
				case "ofdgoDetectFormat":
					if ok && firstToken != "" {
						state.detectToken = firstToken
					} else {
						state.detectToken = ""
					}
				case "ofdgoOpen", "ofdgoOpenPDF":
					if ok {
						state.mainToken = firstToken
						state.detectToken = ""
						state.importToken = ""
					} else {
						state.detectToken = ""
					}
				case "ofdgoCreateDocument":
					if ok {
						state.mainToken = ""
						state.detectToken = ""
						state.importToken = ""
					}
				case "ofdgoImportImages":
					if ok {
						if isNewImageDocument(args) {
							state.mainToken = ""
						}
						state.detectToken = ""
						state.importToken = ""
					}
				case "ofdgoLoadImport":
					state.detectToken = ""
					if ok {
						state.importToken = firstToken
					} else {
						state.importToken = ""
					}
				case "ofdgoClearImport":
					state.importToken = ""
					state.detectToken = ""
				case "ofdgoImportPages", "ofdgoImportDocuments":
					if ok {
						state.importToken = ""
						state.detectToken = ""
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

// registerEntry 登记条目并按需写入路径索引及父子关系，调用方持有mu
// 入参: entry 文件条目, indexed 是否写入路径索引
func (s *fileStore) registerEntry(entry *fileEntry, indexed bool) {
	s.entries[entry.id] = entry
	if indexed && entry.path != "" {
		entry.pathKey = normalizePath(entry.path)
		existing := s.entries[s.pathIndex[entry.pathKey]]
		if existing == nil || existing.retired || entry.owned || !existing.owned {
			s.pathIndex[entry.pathKey] = entry.id
		}
	}
	if entry.parentID != "" {
		if s.children[entry.parentID] == nil {
			s.children[entry.parentID] = make(map[string]struct{})
		}
		s.children[entry.parentID][entry.id] = struct{}{}
	}
}

// unregisterIndex 从路径索引及父子关系表中移除条目，调用方持有mu
// 入参: entry 文件条目
func (s *fileStore) unregisterIndex(entry *fileEntry) {
	if entry.pathKey != "" && s.pathIndex[entry.pathKey] == entry.id {
		delete(s.pathIndex, entry.pathKey)
	}
	if entry.parentID != "" {
		if set := s.children[entry.parentID]; set != nil {
			delete(set, entry.id)
			if len(set) == 0 {
				delete(s.children, entry.parentID)
			}
		}
	}
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
		if entry.temporary && !entry.directory {
			path = entry.path
		}
	}
	return file, path
}

// retireEntry 标记条目退役并在无在途读取时交出待清理资源，调用方持有mu
// 入参: entry 文件条目
// 返回: *os.File 待关闭文件, string 待删除临时路径
func (s *fileStore) retireEntry(entry *fileEntry) (*os.File, string) {
	if !entry.retired {
		entry.retired = true
		s.unregisterIndex(entry)
	}
	return s.collectIdleEntry(entry)
}

// collectTreeFiles 收集目录条目及其子条目的闲置文件句柄，调用方持有mu
// 入参: entry 根条目, files 待关闭文件句柄列表
func (s *fileStore) collectTreeFiles(entry *fileEntry, files *[]*os.File) {
	if entry == nil || entry.retired {
		return
	}
	for childID := range s.children[entry.id] {
		s.collectTreeFiles(s.entries[childID], files)
	}
	if file, _ := s.collectIdleEntry(entry); file != nil {
		*files = append(*files, file)
	}
}

// retireTree 递归退役目录条目及其登记的子条目，调用方持有mu
// 入参: entry 根条目, files 待关闭文件句柄列表
func (s *fileStore) retireTree(entry *fileEntry, files *[]*os.File) {
	if entry == nil {
		return
	}
	if set := s.children[entry.id]; len(set) != 0 {
		ids := make([]string, 0, len(set))
		for childID := range set {
			ids = append(ids, childID)
		}
		delete(s.children, entry.id)
		for _, childID := range ids {
			s.retireTree(s.entries[childID], files)
		}
	}
	if file, _ := s.retireEntry(entry); file != nil {
		*files = append(*files, file)
	}
}

// disownTree 递归剥离条目及子项的本次创建标记，调用方持有mu
// 入参: entry 根条目
func (s *fileStore) disownTree(entry *fileEntry) {
	if entry == nil || entry.retired {
		return
	}
	entry.owned = false
	for childID := range s.children[entry.id] {
		s.disownTree(s.entries[childID])
	}
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
	s.registerEntry(&fileEntry{id: id, data: data, name: "data", stop: context.AfterFunc(ctx, func() { s.release(id) })}, false)
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

// release 释放可读条目、句柄、非临时目录树、内存结果或临时文件，保留常驻条目、临时目录、在途读取及源文件
// 入参: id 资源标识
func (s *fileStore) release(id string) {
	if id == "" {
		return
	}
	var files []*os.File
	s.mu.Lock()
	entry := s.entries[id]
	if entry == nil || entry.retired || entry.pinned || entry.directory && entry.temporary {
		s.mu.Unlock()
		return
	}
	if entry.directory {
		s.retireTree(entry, &files)
		s.mu.Unlock()
		for _, file := range files {
			_ = file.Close()
		}
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

// discardEntries 批量撤销刚登记但未交付的授权条目，不删除源文件
// 入参: ids 待撤销的授权标识列表
func (s *fileStore) discardEntries(ids []string) {
	if len(ids) == 0 {
		return
	}
	var files []*os.File
	s.mu.Lock()
	for _, id := range ids {
		if entry := s.entries[id]; entry != nil && !entry.retired && !entry.pinned {
			if file, _ := s.retireEntry(entry); file != nil {
				files = append(files, file)
			}
		}
	}
	s.mu.Unlock()
	for _, file := range files {
		_ = file.Close()
	}
}

// disown 剥离已交付批量输出项及子项的本次创建标记，避免外部重建同名文件被误删
// 入参: id 父目录授权标识, name 输出入口名称
func (s *fileStore) disown(id, name string) {
	if id == "" || name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	parent := s.entries[id]
	if parent == nil || !parent.directory || parent.retired {
		return
	}
	norm := normalizePath(filepath.Join(parent.path, name))
	s.disownTree(s.entries[s.pathIndex[norm]])
}

// tempDirectory 在宿主临时根目录下创建并登记批量暂存子目录
// 返回: map[string]any 临时目录描述, error 创建错误
func (s *fileStore) tempDirectory() (map[string]any, error) {
	s.mu.Lock()
	if s.closed || s.temporary == "" {
		s.mu.Unlock()
		return nil, os.ErrClosed
	}
	tempRoot := s.temporary
	s.mu.Unlock()
	dir, err := os.MkdirTemp(tempRoot, "batch-*")
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = os.RemoveAll(dir)
		return nil, os.ErrClosed
	}
	id := rand.Text()
	s.registerEntry(&fileEntry{id: id, path: dir, name: filepath.Base(dir), directory: true, owned: true, temporary: true}, true)
	result := s.descriptor(id)
	s.mu.Unlock()
	return result, nil
}

// removeTempDirectory 校验并删除由宿主创建的批量暂存子目录及其全部子条目
// 入参: id 临时目录授权标识
// 返回: error 删除或越权校验错误
func (s *fileStore) removeTempDirectory(id string) error {
	if id == "" {
		return fmt.Errorf("临时目录授权无效")
	}
	var files []*os.File
	var targetPath string
	s.mu.Lock()
	entry := s.entries[id]
	if entry == nil || entry.retired {
		s.mu.Unlock()
		return nil
	}
	if !entry.directory || !entry.temporary || !isSubPath(s.temporary, entry.path) {
		s.mu.Unlock()
		return fmt.Errorf("临时目录授权无效")
	}
	targetPath = entry.path
	s.retireTree(entry, &files)
	s.mu.Unlock()
	for _, file := range files {
		_ = file.Close()
	}
	return os.RemoveAll(targetPath)
}

// child 在已授权目录中查找或创建直接子项
// 入参: id 目录授权标识, name 子项名称, directory 是否为目录, create 是否允许创建
// 返回: map[string]any 句柄描述, bool 是否本次新登记条目, bool 是否本次新建磁盘项, error 访问错误
func (s *fileStore) child(id, name string, directory, create bool) (map[string]any, bool, bool, error) {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "/\\:") {
		return nil, false, false, fmt.Errorf("文件名无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	parent := s.entries[id]
	if parent == nil || !parent.directory || parent.retired {
		return nil, false, false, fmt.Errorf("目录授权无效")
	}
	path := filepath.Join(parent.path, name)
	norm := normalizePath(path)
	if key, ok := s.pathIndex[norm]; ok {
		if entry := s.entries[key]; entry != nil && !entry.retired && entry.directory == directory {
			if _, err := os.Stat(path); err == nil {
				entry.reused = true
				return s.descriptor(key), false, false, nil
			} else if errors.Is(err, os.ErrNotExist) {
				s.unregisterIndex(entry)
			}
		}
	}
	root, err := os.OpenRoot(parent.path)
	if err != nil {
		return nil, false, false, err
	}
	defer root.Close()
	info, err := root.Stat(name)
	created := false
	if errors.Is(err, os.ErrNotExist) && create {
		if directory {
			err = root.Mkdir(name, 0755)
		} else {
			var file *os.File
			file, err = root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
			if err == nil {
				err = file.Close()
			}
		}
		if err != nil {
			return nil, false, false, err
		}
		created = true
		info, err = root.Stat(name)
	}
	if err != nil {
		return nil, false, false, err
	}
	if info.IsDir() != directory {
		return nil, false, false, fmt.Errorf("文件类型不匹配")
	}
	key := rand.Text()
	s.registerEntry(&fileEntry{id: key, parentID: id, path: path, name: name, size: info.Size(), modTime: info.ModTime().UnixMilli(), directory: directory, owned: created}, true)
	return s.descriptor(key), true, created, nil
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
			file, err = root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
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
		s.registerEntry(&fileEntry{id: key, parentID: id, path: filepath.Join(parent.path, name), name: name, directory: directory, owned: true}, true)
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
	if entry.readOnly {
		return nil, fmt.Errorf("文件为只读，无法写入")
	}
	if entry.saveTarget == nil {
		if linfo, lerr := os.Lstat(entry.path); lerr == nil && linfo.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("不支持直接覆盖符号链接，请另存为")
		}
		var err error
		entry.saveTarget, err = ofdgo.NewFileOutput(entry.path)
		if err != nil {
			return nil, saveError(err)
		}
	}
	output, err := entry.saveTarget.Begin(ctx)
	if err != nil {
		return nil, saveError(err)
	}
	writer := &fileWriter{id: rand.Text(), entryID: id, store: s, output: output}
	s.writers[writer.id] = writer
	return writer, nil
}

// hasActiveHolder 判断授权标识是否仍被任一会话持有，调用方持有mu
// 入参: id 授权标识
// 返回: bool 是否存在活跃会话持有者
func (s *fileStore) hasActiveHolder(id string) bool {
	for _, holder := range s.holders {
		if holder == nil {
			continue
		}
		if holder.entries[id] != nil || holder.mainToken == id || holder.detectToken == id || holder.importToken == id {
			return true
		}
	}
	return false
}

// hasActiveWriter 判断授权标识是否仍存在未结束的写入事务，调用方持有mu
// 入参: id 授权标识
// 返回: bool 是否存在活跃写入事务
func (s *fileStore) hasActiveWriter(id string) bool {
	for _, writer := range s.writers {
		if writer != nil && writer.entryID == id {
			return true
		}
	}
	return false
}

// discardChild 精确按条目ID回滚未交付的子项授权并按条件清理本次新建的磁盘路径
// 入参: childID 子项授权标识, created 是否本次新建磁盘项
func (s *fileStore) discardChild(childID string, created bool) {
	if childID == "" {
		return
	}
	var files []*os.File
	var stop func() bool
	s.mu.Lock()
	entry := s.entries[childID]
	if entry == nil || entry.retired || entry.pinned || entry.directory && entry.temporary || entry.reused {
		s.mu.Unlock()
		return
	}
	otherActive := entry.readers > 0 || entry.saveTarget != nil || len(s.children[childID]) > 0 || s.hasActiveHolder(childID) || s.hasActiveWriter(childID)
	canRemove := created && entry.owned && !otherActive && entry.path != "" && s.pathIndex[normalizePath(entry.path)] == childID
	if canRemove {
		parent := s.entries[entry.parentID]
		if parent != nil && parent.directory && !parent.retired && parent.path != "" && entry.name != "" {
			if entry.directory {
				s.collectTreeFiles(entry, &files)
			} else if file, _ := s.collectIdleEntry(entry); file != nil {
				files = append(files, file)
			}
			for _, file := range files {
				_ = file.Close()
			}
			files = nil
			if root, err := os.OpenRoot(parent.path); err == nil {
				if entry.directory {
					_ = root.RemoveAll(entry.name)
				} else {
					_ = root.Remove(entry.name)
				}
				_ = root.Close()
			}
		}
	}
	if entry.directory {
		s.retireTree(entry, &files)
	} else {
		stop = entry.stop
		entry.stop = nil
		if file, _ := s.retireEntry(entry); file != nil {
			files = append(files, file)
		}
	}
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	for _, file := range files {
		_ = file.Close()
	}
}

// remove 清理由当前窗口创建的目录子项
// 入参: id 目录授权标识, name 子项名称, recursive 是否递归删除
// 返回: error 删除错误
func (s *fileStore) remove(id, name string, recursive bool) error {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "/\\:") {
		return fmt.Errorf("文件名无效")
	}
	s.mu.Lock()
	parent := s.entries[id]
	if parent == nil || !parent.directory || parent.retired {
		s.mu.Unlock()
		return fmt.Errorf("目录授权无效")
	}
	parentPath := parent.path
	target := filepath.Join(parentPath, name)
	norm := normalizePath(target)
	entry := s.entries[s.pathIndex[norm]]
	if entry == nil || entry.retired || !entry.owned {
		s.mu.Unlock()
		if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("只能清理本次创建的输出")
	}
	var files []*os.File
	if recursive {
		s.collectTreeFiles(entry, &files)
	} else if file, _ := s.collectIdleEntry(entry); file != nil {
		files = append(files, file)
	}
	s.mu.Unlock()
	for _, file := range files {
		_ = file.Close()
	}
	root, err := os.OpenRoot(parentPath)
	if err != nil {
		return err
	}
	defer root.Close()
	if recursive {
		err = root.RemoveAll(name)
	} else {
		err = root.Remove(name)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var retired []*os.File
	s.mu.Lock()
	if recursive {
		s.retireTree(entry, &retired)
	} else if file, _ := s.retireEntry(entry); file != nil {
		retired = append(retired, file)
	}
	s.mu.Unlock()
	for _, file := range retired {
		_ = file.Close()
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
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
	w.Header().Set("Content-Type", "application/octet-stream")
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
	s.pathIndex = make(map[string]string)
	s.children = make(map[string]map[string]struct{})
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

// Write 追加输出数据
// 入参: data 输出数据
// 返回: int 写入字节数, error 写入错误
func (w *fileWriter) Write(data []byte) (int, error) {
	n, err := w.output.Write(data)
	return n, saveError(err)
}

// Close 同步并替换目标文件
// 返回: error 提交错误
func (w *fileWriter) Close() error {
	defer w.release()
	if err := w.output.Commit(); err != nil {
		return saveError(errors.Join(err, w.output.Abort()))
	}
	return nil
}

// Abort 删除未提交的临时输出
// 返回: error 清理错误
func (w *fileWriter) Abort() error {
	defer w.release()
	return saveError(w.output.Abort())
}

// release 移除已结束的写入事务
func (w *fileWriter) release() {
	w.store.mu.Lock()
	defer w.store.mu.Unlock()
	delete(w.store.writers, w.id)
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

// ReadAt 从共享打包槽位读取指定位置的数据
// 入参: data 目标缓冲区, offset 字节偏移
// 返回: int 读取字节数, error 读取错误
func (r packSlotReader) ReadAt(data []byte, offset int64) (int, error) {
	return r.slot.readAt(r.id, data, offset)
}
