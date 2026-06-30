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
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

// FontFile 内存字体文件
type FontFile struct {
	Name string
	Data []byte
}

// FontFS 内存字体文件系统
type FontFS struct {
	files map[string][]byte
	names []string
}

// NewFontFS 创建内存字体文件系统
// 入参: fonts 字体文件列表
// 返回: *FontFS 内存字体文件系统
func NewFontFS(fonts []FontFile) *FontFS {
	fsys := &FontFS{
		files: make(map[string][]byte),
	}
	for _, font := range fonts {
		name := cleanFontName(font.Name)
		if name == "." || len(font.Data) == 0 {
			continue
		}
		if _, ok := fsys.files[name]; !ok {
			fsys.names = append(fsys.names, name)
		}
		fsys.files[name] = append([]byte(nil), font.Data...)
	}
	sort.Strings(fsys.names)
	return fsys
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
	name = cleanFontName(name)
	if name == "." {
		return &fontDir{entries: fsys.entries()}, nil
	}
	data, ok := fsys.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return &fontMemFile{
		Reader: bytes.NewReader(data),
		info: fontFileInfo{
			name: name,
			size: int64(len(data)),
		},
	}, nil
}

// ReadDir 读取字体目录
// 入参: name 目录名
// 返回: []fs.DirEntry 目录条目, error 错误信息
func (fsys *FontFS) ReadDir(name string) ([]fs.DirEntry, error) {
	name = cleanFontName(name)
	if name != "." {
		return nil, fs.ErrNotExist
	}
	return fsys.entries(), nil
}

// Glob 匹配字体文件
// 入参: pattern 匹配模式
// 返回: []string 字体文件列表, error 错误信息
func (fsys *FontFS) Glob(pattern string) ([]string, error) {
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, err
	}
	return fsys.match(pattern), nil
}

// Match 匹配指定字体名称
// 入参: names 字体名称列表
// 返回: string 匹配字体文件, bool 是否为名称匹配
func (fsys *FontFS) Match(names ...string) (string, bool) {
	for _, name := range fontCandidateNames(names...) {
		if matches := fsys.match(name + "*"); len(matches) > 0 {
			return matches[0], true
		}
	}
	if matches := fsys.fallbackFonts(); len(matches) > 0 {
		return matches[0], false
	}
	return "", false
}

// match 匹配字体文件模式
// 入参: pattern 匹配模式
// 返回: []string 字体文件列表
func (fsys *FontFS) match(pattern string) []string {
	var matches []string
	for _, name := range fsys.names {
		if matchFontPattern(pattern, name) {
			matches = append(matches, name)
		}
	}
	return matches
}

// fallbackFonts 获取回退字体文件
// 返回: []string 字体文件列表
func (fsys *FontFS) fallbackFonts() []string {
	for _, item := range fontFallbackFiles {
		if _, ok := fsys.files[item]; ok {
			return []string{item}
		}
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
	if name == "." || name == "/" {
		return "."
	}
	return strings.ToLower(name)
}

// matchFontPattern 匹配字体文件模式
// 入参: pattern 匹配模式, name 字体文件名
// 返回: bool 是否匹配
func matchFontPattern(pattern, name string) bool {
	if ok, _ := path.Match(pattern, name); ok {
		return true
	}
	if ok, _ := path.Match(strings.ToLower(pattern), strings.ToLower(name)); ok {
		return true
	}
	stem := strings.TrimSuffix(pattern, "*")
	stem = strings.TrimSuffix(stem, path.Ext(stem))
	stem = fontNormalizeName(stem)
	if stem == "" {
		return false
	}
	name = fontNormalizeName(name)
	if strings.HasPrefix(name, stem) {
		return true
	}
	for _, alias := range fontCandidateNames(stem) {
		alias = fontNormalizeName(alias)
		if alias != "" && strings.HasPrefix(name, alias) {
			return true
		}
	}
	return false
}

// fontMemFile 内存字体文件
type fontMemFile struct {
	*bytes.Reader
	info fontFileInfo
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

// fontDir 字体目录
type fontDir struct {
	offset  int
	entries []fs.DirEntry
}

// Stat 获取字体目录信息
// 返回: fs.FileInfo 文件信息, error 错误信息
func (d *fontDir) Stat() (fs.FileInfo, error) {
	return fontFileInfo{name: ".", mode: fs.ModeDir}, nil
}

// Read 读取字体目录数据
// 返回: int 读取字节数, error 错误信息
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
		return nil, io.EOF
	}
	if count <= 0 || d.offset+count > len(d.entries) {
		count = len(d.entries) - d.offset
	}
	entries := d.entries[d.offset : d.offset+count]
	d.offset += count
	return entries, nil
}

// fontDirEntry 字体目录条目
type fontDirEntry struct {
	info fontFileInfo
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

// fontFileInfo 字体文件信息
type fontFileInfo struct {
	name string
	size int64
	mode fs.FileMode
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

var _ fs.FS = (*FontFS)(nil)
var _ fs.ReadDirFS = (*FontFS)(nil)
var _ fs.GlobFS = (*FontFS)(nil)
var _ fs.File = (*fontMemFile)(nil)
var _ fs.ReadDirFile = (*fontDir)(nil)
var _ fs.DirEntry = (*fontDirEntry)(nil)
