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

package ofdgo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
)

// 字体文件系统及目录条目的接口检查
var (
	_ fs.FS          = (*FontFS)(nil)
	_ fs.ReadDirFS   = (*FontFS)(nil)
	_ fs.ReadFileFS  = (*FontFS)(nil)
	_ fs.GlobFS      = (*FontFS)(nil)
	_ fs.File        = (*fontMemFile)(nil)
	_ fs.ReadDirFile = (*fontDir)(nil)
	_ fs.DirEntry    = (*fontDirEntry)(nil)
)

// FontFile 内存字体文件
type FontFile struct {
	Name string
	Data []byte
}

// FontFS 缓存字体数据及名称索引的只读文件系统
type FontFS struct {
	files      map[string][]byte
	loaders    map[string]*lazyFontEntry
	names      []string
	candidates []basefont.FileCandidate
}

// lazyFontEntry 按需加载单个字体文件并缓存非取消与非超时结果
type lazyFontEntry struct {
	mu       sync.Mutex
	done     bool
	data     []byte
	err      error
	original string
	loader   func(string) ([]byte, error)
}

// fontMemFile 内存字体文件
type fontMemFile struct {
	*bytes.Reader
	info fontFileInfo
}

// fontDir 字体目录
type fontDir struct {
	offset  int
	entries []fs.DirEntry
}

// fontDirEntry 字体目录条目
type fontDirEntry struct {
	info fontFileInfo
}

// fontFileInfo 字体文件信息
type fontFileInfo struct {
	name string
	size int64
	mode fs.FileMode
}

// NewFontFS 创建内存字体文件系统
// 入参: fonts 字体文件列表
// 返回: *FontFS 内存字体文件系统
func NewFontFS(fonts []FontFile) *FontFS {
	return buildFontFS(fonts, true)
}

// NewSharedFontFS 创建零拷贝共享的内存字体文件系统，调用方不得修改已传入的字体字节
// 入参: fonts 只读字体文件列表
// 返回: *FontFS 只读字体文件系统
func NewSharedFontFS(fonts []FontFile) *FontFS {
	return buildFontFS(fonts, false)
}

// NewLazyFontFS 创建按需读取的字体文件系统，按文件名匹配，不预读字体内容
// 非取消和超时读取结果仅缓存一次；目录条目大小为0，打开后可获取实际大小；load为nil时返回空文件系统
// 入参: names 包含字体名称和样式的文件名, load 按原文件名读取独立字体的函数
// 返回: *FontFS 字体文件系统
func NewLazyFontFS(names []string, load func(string) ([]byte, error)) *FontFS {
	fsys := &FontFS{files: make(map[string][]byte), loaders: make(map[string]*lazyFontEntry)}
	if load == nil {
		return fsys
	}
	for _, original := range names {
		name := cleanFontName(original)
		if name == "." || !basefont.IsFontFileName(name) {
			continue
		}
		if _, exists := fsys.files[name]; exists {
			continue
		}
		fsys.names = append(fsys.names, name)
		fsys.files[name] = nil
		fsys.loaders[name] = &lazyFontEntry{original: original, loader: load}
	}
	sort.Strings(fsys.names)
	fsys.candidates = basefont.FileCandidates(fsys.names, path.Base)
	return fsys
}

// buildFontFS 构建内存字体文件系统，按需决定是否复制原始字体切片
// 入参: fonts 字体文件列表, clone 是否复制字体字节
// 返回: *FontFS 字体文件系统
func buildFontFS(fonts []FontFile, clone bool) *FontFS {
	fsys := &FontFS{
		files: make(map[string][]byte, len(fonts)),
	}
	for _, font := range fonts {
		name := cleanFontName(font.Name)
		if name == "." || len(font.Data) == 0 {
			continue
		}
		if _, ok := fsys.files[name]; !ok {
			fsys.names = append(fsys.names, name)
		}
		if clone {
			fsys.files[name] = bytes.Clone(font.Data)
		} else {
			fsys.files[name] = slices.Clip(font.Data)
		}
	}
	sort.Strings(fsys.names)
	fsys.candidates = basefont.FileCandidates(fsys.names, path.Base)
	for i, file := range fsys.candidates {
		fsys.candidates = basefont.AppendFileNames(fsys.candidates, i, basefont.FileNames(bytes.NewReader(fsys.files[file.Name])))
	}
	return fsys
}

// cleanFontName 清理字体文件名
// 入参: name 字体文件名
// 返回: string 清理后的字体文件名
func cleanFontName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimSpace(path.Clean(name))
	if name == "" || name == "/" {
		return "."
	}
	name = path.Base(name)
	if name == "." || name == ".." || name == "/" {
		return "."
	}
	return strings.ToLower(name)
}

// Len 获取字体文件数量
// 返回: int 字体文件数量
func (fsys *FontFS) Len() int {
	if fsys == nil {
		return 0
	}
	return len(fsys.names)
}

// Open 打开字体文件
// 入参: name 字体文件名
// 返回: fs.File 字体文件, error 错误信息
func (fsys *FontFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if fsys == nil {
		if name == "." {
			return &fontDir{}, nil
		}
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if name == "." {
		return &fontDir{entries: fsys.entries()}, nil
	}
	lower := strings.ToLower(name)
	data, ok := fsys.files[lower]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if entry := fsys.loaders[lower]; entry != nil {
		var err error
		data, err = entry.load()
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
	}
	return &fontMemFile{
		Reader: bytes.NewReader(data),
		info: fontFileInfo{
			name: lower,
			size: int64(len(data)),
		},
	}, nil
}

// ReadFile 返回字体字节的独立副本，防止调用方修改内部只读数据
// 入参: name 字体文件名
// 返回: []byte 字体数据副本, error 错误信息
func (fsys *FontFS) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) || name == "." {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrInvalid}
	}
	if fsys == nil {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrNotExist}
	}
	lower := strings.ToLower(name)
	data, ok := fsys.files[lower]
	if !ok {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrNotExist}
	}
	if entry := fsys.loaders[lower]; entry != nil {
		var err error
		data, err = entry.load()
		if err != nil {
			return nil, &fs.PathError{Op: "read", Path: name, Err: err}
		}
	}
	return bytes.Clone(data), nil
}

// ReadDir 读取字体目录
// 入参: name 目录名
// 返回: []fs.DirEntry 目录条目, error 错误信息
func (fsys *FontFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	if fsys == nil {
		if name == "." {
			return nil, nil
		}
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	if name != "." {
		err := fs.ErrNotExist
		if _, ok := fsys.files[strings.ToLower(name)]; ok {
			err = fs.ErrInvalid
		}
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	return fsys.entries(), nil
}

// Glob 匹配字体文件
// 入参: pattern 匹配模式
// 返回: []string 字体文件列表, error 错误信息
func (fsys *FontFS) Glob(pattern string) ([]string, error) {
	if fsys == nil {
		return nil, nil
	}
	return fs.Glob(struct{ fs.FS }{fsys}, pattern)
}

// Match 匹配指定字体名称
// 入参: names 字体名称列表
// 返回: string 匹配字体文件, bool 是否为名称匹配
func (fsys *FontFS) Match(names ...string) (string, bool) {
	return fsys.MatchStyle(false, false, names...)
}

// MatchStyle 匹配指定样式的字体名称
// 入参: bold 是否粗体, italic 是否斜体, names 字体名称列表
// 返回: string 匹配字体文件, bool 是否为名称匹配
func (fsys *FontFS) MatchStyle(bold, italic bool, names ...string) (string, bool) {
	if fsys == nil {
		return "", false
	}
	if len(names) > 0 {
		if matches := basefont.FileMatches(fsys.candidates, names, bold, italic); len(matches) > 0 {
			return matches[0].Name, true
		}
	}
	if matches := fsys.fallbackFonts(bold, italic); len(matches) > 0 {
		return matches[0], false
	}
	return "", false
}

// MatchFontFiles 按文档字体名称和样式选择首选文件，未声明的使用编号按默认规则回退
// 入参: files 可用字体文件名, ids 已发现的使用编号，省略时仅匹配声明字体
// 返回: []string 所需字体文件名, error 错误信息
func (r *Reader) MatchFontFiles(files []string, ids ...string) ([]string, error) {
	fonts, err := r.Fonts()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if r.fontDefinition(id) == nil {
			fonts = append(fonts, Font{ID: id})
		}
	}
	candidates := basefont.FileCandidates(files, path.Base)
	names := make([]string, 0, len(fonts))
	seen := make(map[string]bool)
	for _, font := range fonts {
		if font.FontFile != "" {
			continue
		}
		matches := basefont.FileMatches(candidates, []string{font.FontName, font.FamilyName}, font.Bold, font.Italic)
		if len(matches) == 0 {
			matches = basefont.FileMatches(candidates, nil, font.Bold, font.Italic)
		}
		if len(matches) > 0 {
			name := matches[0].Name
			if !seen[name] {
				names = append(names, name)
				seen[name] = true
			}
		}
	}
	return names, nil
}

// Stat 获取字体文件信息
// 返回: fs.FileInfo 文件信息, error 错误信息
func (f *fontMemFile) Stat() (fs.FileInfo, error) {
	return f.info, nil
}

// Close 关闭字体文件
// 返回: error 错误信息
func (f *fontMemFile) Close() error {
	return nil
}

// Stat 获取字体目录信息
// 返回: fs.FileInfo 文件信息, error 错误信息
func (d *fontDir) Stat() (fs.FileInfo, error) {
	return fontFileInfo{name: ".", mode: fs.ModeDir}, nil
}

// Read 字体目录不提供字节内容，始终返回EOF
// 返回: int 恒为0, error 目录不可按文件读取错误
func (d *fontDir) Read([]byte) (int, error) {
	return 0, io.EOF
}

// Close 关闭字体目录
// 返回: error 错误信息
func (d *fontDir) Close() error {
	return nil
}

// ReadDir 读取字体目录条目
// 入参: count 读取数量
// 返回: []fs.DirEntry 目录条目, error 错误信息
func (d *fontDir) ReadDir(count int) ([]fs.DirEntry, error) {
	if d.offset >= len(d.entries) {
		if count <= 0 {
			return nil, nil
		}
		return nil, io.EOF
	}
	if count <= 0 || d.offset+count > len(d.entries) {
		count = len(d.entries) - d.offset
	}
	entries := d.entries[d.offset : d.offset+count]
	d.offset += count
	return entries, nil
}

// Name 获取目录条目名称
// 返回: string 目录条目名称
func (e fontDirEntry) Name() string {
	return e.info.Name()
}

// IsDir 判断是否为目录
// 返回: bool 是否为目录
func (e fontDirEntry) IsDir() bool {
	return false
}

// Type 获取目录条目类型
// 返回: fs.FileMode 文件模式
func (e fontDirEntry) Type() fs.FileMode {
	return e.info.Mode().Type()
}

// Info 获取目录条目信息
// 返回: fs.FileInfo 文件信息, error 错误信息
func (e fontDirEntry) Info() (fs.FileInfo, error) {
	return e.info, nil
}

// Name 获取文件名
// 返回: string 文件名
func (i fontFileInfo) Name() string {
	if i.name == "" {
		return "."
	}
	return i.name
}

// Size 获取文件大小
// 返回: int64 文件大小
func (i fontFileInfo) Size() int64 {
	return i.size
}

// Mode 获取文件模式
// 返回: fs.FileMode 文件模式
func (i fontFileInfo) Mode() fs.FileMode {
	if i.mode != 0 {
		return i.mode
	}
	return 0444
}

// ModTime 获取文件修改时间
// 返回: time.Time 修改时间
func (i fontFileInfo) ModTime() time.Time {
	return time.Time{}
}

// IsDir 判断是否为目录
// 返回: bool 是否为目录
func (i fontFileInfo) IsDir() bool {
	return i.mode.IsDir()
}

// Sys 获取底层文件信息
// 返回: any 底层文件信息
func (i fontFileInfo) Sys() any {
	return nil
}

// fallbackFonts 获取回退字体文件
// 入参: bold 是否粗体, italic 是否斜体
// 返回: []string 字体文件列表
func (fsys *FontFS) fallbackFonts(bold, italic bool) []string {
	if matches := basefont.FileMatches(fsys.candidates, nil, bold, italic); len(matches) > 0 {
		return basefont.FileMatchNames(matches)
	}
	if len(fsys.names) > 0 {
		return []string{fsys.names[0]}
	}
	return nil
}

// entries 获取字体目录条目
// 返回: []fs.DirEntry 目录条目
func (fsys *FontFS) entries() []fs.DirEntry {
	entries := make([]fs.DirEntry, 0, len(fsys.names))
	for _, name := range fsys.names {
		entries = append(entries, fontDirEntry{info: fontFileInfo{
			name: name,
			size: int64(len(fsys.files[name])),
		}})
	}
	return entries
}

// load 按需加载并缓存字体数据，取消或超时错误不缓存以允许重试
// 返回: []byte 字体字节, error 读取错误
func (e *lazyFontEntry) load() ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done {
		return e.data, e.err
	}
	data, err := e.loader(e.original)
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return nil, err
	}
	e.data = bytes.Clone(data)
	e.err = err
	e.done = true
	e.loader = nil
	return e.data, e.err
}
