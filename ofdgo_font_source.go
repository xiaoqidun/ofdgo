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
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"

	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
)

// 字体来源：文件、文件系统和系统字体
const (
	fontSourceFile fontSourceKind = iota
	fontSourceFS
	fontSourceSystem
)

// systemFontIndex 按需建立只读系统字体索引
var systemFontIndex = sync.OnceValue(indexSystemFonts)

// fontSourceKind 字体来源类型
type fontSourceKind uint8

// fontSource 字体来源
type fontSource struct {
	kind     fontSourceKind
	index    int
	face     int
	name     string
	exact    bool
	priority int
}

// fontSourceKey 字体来源去重键
type fontSourceKey struct {
	kind  fontSourceKind
	index int
	face  int
	name  string
}

// fontCandidateKey 按字体标识、名称与固有样式区分候选列表，避免页面局部同ID串台
type fontCandidateKey struct {
	id         string
	fontName   string
	familyName string
	bold       bool
	italic     bool
}

// fontSourceData 保存读取结果，避免重复打开同一字体文件
type fontSourceData struct {
	data []byte
	err  error
}

// fontSourceResolved 保存默认后端解析后的只读外部字体结果
type fontSourceResolved struct {
	data   []byte
	digest [32]byte
	face   FontFace
}

// fontFaceResolveError 标记已打开字体在解析字面信息时的错误，用于区分候选回退与直接失败
type fontFaceResolveError struct {
	err error
}

// Error 返回字面解析错误描述
// 返回: string 错误信息
func (e fontFaceResolveError) Error() string { return e.err.Error() }

// Unwrap 返回底层字面解析错误
// 返回: error 底层错误
func (e fontFaceResolveError) Unwrap() error { return e.err }

// sharedFontSourceIndex 为派生渲染器提供并发安全的目录、文件系统与字体候选共享索引
type sharedFontSourceIndex struct {
	mu          sync.RWMutex
	candidates  map[fontCandidateKey][]fontSource
	directories map[string][]basefont.FileCandidate
	filesystems map[int][]basefont.FileCandidate
	names       map[int][]string
}

// fontSourceCache 保存字体候选及独立字体数据，按字节预算淘汰，不包含绘图库对象
type fontSourceCache struct {
	shared       *sharedFontSourceIndex
	data         *renderCache[fontSourceKey, fontSourceData]
	collections  *renderCache[fontSourceKey, []byte]
	resolved     *renderCache[fontSourceKey, fontSourceResolved]
	fallback     ResolvedFont
	fallbackRead bool
}

// newSharedFontSourceIndex 创建并发安全的共享字体来源索引
// 返回: *sharedFontSourceIndex 共享字体来源索引
func newSharedFontSourceIndex() *sharedFontSourceIndex {
	return &sharedFontSourceIndex{candidates: make(map[fontCandidateKey][]fontSource), directories: make(map[string][]basefont.FileCandidate), filesystems: make(map[int][]basefont.FileCandidate), names: make(map[int][]string)}
}

// newFontSourceCache 创建独立字体来源缓存
// 入参: resources 共享资源策略
// 返回: *fontSourceCache 字体来源缓存
func newFontSourceCache(resources *Resources) *fontSourceCache {
	return &fontSourceCache{
		shared:      newSharedFontSourceIndex(),
		data:        resourceCache[fontSourceKey, fontSourceData](resources, 64<<20),
		collections: resourceCache[fontSourceKey, []byte](resources, 64<<20),
		resolved:    resourceCache[fontSourceKey, fontSourceResolved](resources, 64<<20),
	}
}

// sortFontSources 将完整名称匹配排在回退候选之前
// 入参: sources 字体来源列表
func sortFontSources(sources []fontSource) {
	sort.SliceStable(sources, func(i, j int) bool {
		return sources[i].priority < sources[j].priority
	})
}

// appendFontSource 追加字体来源
// 入参: sources 字体来源列表, seen 去重映射, source 字体来源
// 返回: []fontSource 字体来源列表
func appendFontSource(sources []fontSource, seen map[fontSourceKey]bool, source fontSource) []fontSource {
	if source.name == "" {
		return sources
	}
	key := fontSourceKey{kind: source.kind, index: source.index, name: source.name, face: source.face}
	if seen[key] {
		return sources
	}
	seen[key] = true
	return append(sources, source)
}

// indexSystemFonts 递归读取系统字体名称，保留集合索引
// 返回: []basefont.FileCandidate 字体候选
func indexSystemFonts() []basefont.FileCandidate {
	files := basefont.SystemFiles()
	candidates := basefont.FileCandidates(files, filepath.Base)
	for i, candidate := range candidates {
		file, err := os.Open(candidate.Name)
		if err != nil {
			continue
		}
		candidates = basefont.AppendFileNames(candidates, i, basefont.FileNames(file))
		file.Close()
	}
	return candidates
}

// getCandidates 读取共享字体候选列表
// 入参: key 字体候选键
// 返回: []fontSource 候选来源, bool 是否命中
func (s *sharedFontSourceIndex) getCandidates(key fontCandidateKey) ([]fontSource, bool) {
	s.mu.RLock()
	sources, ok := s.candidates[key]
	s.mu.RUnlock()
	return sources, ok
}

// setCandidates 写入共享字体候选列表
// 入参: key 字体候选键, sources 候选来源
func (s *sharedFontSourceIndex) setCandidates(key fontCandidateKey, sources []fontSource) {
	s.mu.Lock()
	s.candidates[key] = sources
	s.mu.Unlock()
}

// getDirectory 读取共享目录字体索引
// 入参: dir 字体目录
// 返回: []basefont.FileCandidate 候选列表, bool 是否命中
func (s *sharedFontSourceIndex) getDirectory(dir string) ([]basefont.FileCandidate, bool) {
	s.mu.RLock()
	candidates, ok := s.directories[dir]
	s.mu.RUnlock()
	return candidates, ok
}

// setDirectory 写入共享目录字体索引
// 入参: dir 字体目录, candidates 候选列表
func (s *sharedFontSourceIndex) setDirectory(dir string, candidates []basefont.FileCandidate) {
	s.mu.Lock()
	s.directories[dir] = candidates
	s.mu.Unlock()
}

// getFilesystem 读取共享文件系统字体索引
// 入参: index 文件系统索引
// 返回: []basefont.FileCandidate 候选列表, bool 是否命中
func (s *sharedFontSourceIndex) getFilesystem(index int) ([]basefont.FileCandidate, bool) {
	s.mu.RLock()
	candidates, ok := s.filesystems[index]
	s.mu.RUnlock()
	return candidates, ok
}

// setFilesystem 写入共享文件系统字体索引
// 入参: index 文件系统索引, candidates 候选列表
func (s *sharedFontSourceIndex) setFilesystem(index int, candidates []basefont.FileCandidate) {
	s.mu.Lock()
	s.filesystems[index] = candidates
	s.mu.Unlock()
}

// getNames 读取共享文件系统目录条目
// 入参: index 文件系统索引
// 返回: []string 文件名列表, bool 是否命中
func (s *sharedFontSourceIndex) getNames(index int) ([]string, bool) {
	s.mu.RLock()
	names, ok := s.names[index]
	s.mu.RUnlock()
	return names, ok
}

// setNames 写入共享文件系统目录条目
// 入参: index 文件系统索引, names 文件名列表
func (s *sharedFontSourceIndex) setNames(index int, names []string) {
	s.mu.Lock()
	s.names[index] = names
	s.mu.Unlock()
}

// child 复用相同来源的文件索引、候选与数据，回退状态保持独立
// 返回: *fontSourceCache 独立文档缓存
func (c *fontSourceCache) child() *fontSourceCache {
	return &fontSourceCache{shared: c.shared, data: c.data, collections: c.collections, resolved: c.resolved}
}

// fontSourceCacheKey 计算外部字体来源的规范缓存键
// 入参: source 字体来源, definition 字体定义
// 返回: fontSourceKey 规范键, bool 是否有效
func fontSourceCacheKey(source fontSource, definition *Font) (fontSourceKey, bool) {
	key := fontSourceKey{kind: source.kind, index: source.index, face: source.face, name: source.name}
	if source.kind == fontSourceSystem {
		matches := basefont.FileMatches(systemFontIndex(), []string{source.name}, definition.Bold, definition.Italic)
		if len(matches) == 0 {
			return fontSourceKey{}, false
		}
		key = fontSourceKey{kind: fontSourceFile, name: matches[0].Name, face: matches[0].Face}
	}
	return key, true
}

// readFontSource 读取独立字体并规范符号映射，保留集合中的名称和样式选择
// 入参: source 字体来源, definition 字体定义
// 返回: []byte 字体数据, error 读取或集合错误
func (r *Renderer) readFontSource(source fontSource, definition *Font) ([]byte, error) {
	key, ok := fontSourceCacheKey(source, definition)
	if !ok {
		return nil, fmt.Errorf("font %q not found", source.name)
	}
	return r.readFontSourceKey(key)
}

// readFontSourceKey 按规范缓存键读取独立字体并规范符号映射
// 入参: key 规范字体缓存键
// 返回: []byte 字体数据, error 读取或集合错误
func (r *Renderer) readFontSourceKey(key fontSourceKey) ([]byte, error) {
	if cached, ok := r.fontSourcesCache.data.get(key); ok {
		return cached.data, cached.err
	}
	fileKey := key
	fileKey.face = 0
	ctx := r.pageContext
	if ctx == nil {
		ctx = r.outputContext()
	}
	data, err := r.fontSourcesCache.collections.load(ctx, fileKey, func() ([]byte, int, error) {
		var raw []byte
		var readErr error
		if key.kind == fontSourceFS {
			raw, readErr = fs.ReadFile(r.fontFS[key.index], key.name)
		} else {
			raw, readErr = os.ReadFile(key.name)
		}
		if readErr != nil {
			return nil, 0, readErr
		}
		return raw, len(raw) + 256, nil
	})
	if err == nil && bytes.HasPrefix(data, []byte("ttcf")) {
		data, err = basefont.ExtractCollectionFont(data, key.face)
	}
	if err == nil {
		data, _, err = basefont.SymbolProgram(ctx, data, "")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		r.fontSourcesCache.data.put(key, fontSourceData{data, err}, len(data)+256)
	}
	return data, err
}

// fontSourceMatch 匹配可被当前字体后端解析的来源
// 入参: backend 字体后端, fontID 字体ID, definition 字体定义, exact 是否禁止无关回退
// 返回: fontSource 字体来源, FontMetrics 独立字体度量
func (r *Renderer) fontSourceMatch(backend FontBackend, fontID string, definition *Font, exact bool) (fontSource, FontMetrics) {
	for _, source := range r.fontSources(fontID, definition) {
		if exact && !source.exact {
			continue
		}
		data, err := r.readFontSource(source, definition)
		if err != nil {
			continue
		}
		if metrics, err := backend.OpenFont(data); err == nil {
			return source, metrics
		}
	}
	return fontSource{}, nil
}

// resolveFontSource 按公共匹配规则读取字体，由指定后端验证外部字体
// 入参: backend 字体后端, id 字体ID, exact 是否禁止无关回退
// 返回: ResolvedFont 字体及来源, error 解析错误
func (r *Renderer) resolveFontSource(backend FontBackend, id string, exact bool) (ResolvedFont, error) {
	definition := r.fontDefinition(id)
	if definition == nil && r.reader != nil && !r.reader.fontResourcesRead && r.reader.ofd != nil {
		if _, err := r.reader.Fonts(); err != nil {
			return ResolvedFont{}, err
		}
		definition = r.fontDefinition(id)
	}
	if definition == nil {
		definition = &Font{ID: id}
	}
	if definition.FontFile != "" {
		data, err := r.reader.readFontData(definition, true)
		if err != nil {
			return ResolvedFont{}, err
		}
		face, err := r.reader.embeddedFontFace(*definition)
		if err != nil {
			return ResolvedFont{}, err
		}
		return ResolvedFont{Data: data, Source: path.Base(definition.FontFile), Face: face, Exact: true}, nil
	}
	if _, ok := backend.(SFNTBackend); ok {
		ctx := r.pageContext
		if ctx == nil {
			ctx = r.outputContext()
		}
		for _, source := range r.fontSources(id, definition) {
			if exact && !source.exact {
				continue
			}
			key, ok := fontSourceCacheKey(source, definition)
			if !ok {
				continue
			}
			resolved, err := r.fontSourcesCache.resolved.load(ctx, key, func() (fontSourceResolved, int, error) {
				data, err := r.readFontSourceKey(key)
				if err != nil {
					return fontSourceResolved{}, 0, err
				}
				metrics, err := backend.OpenFont(data)
				if err != nil {
					return fontSourceResolved{}, 0, err
				}
				written, digest, face, err := metrics.(*sfntFontMetrics).resolvedWrite()
				if err != nil {
					return fontSourceResolved{}, 0, fontFaceResolveError{err: err}
				}
				return fontSourceResolved{data: written, digest: digest, face: face}, len(written) + 256, nil
			})
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return ResolvedFont{}, err
				}
				var faceErr fontFaceResolveError
				if errors.As(err, &faceErr) {
					return ResolvedFont{}, faceErr.err
				}
				continue
			}
			face := resolved.face
			face.Index = source.face
			return ResolvedFont{Data: resolved.data, Source: source.name, Face: &face, Exact: source.exact, digest: resolved.digest}, nil
		}
	} else if source, metrics := r.fontSourceMatch(backend, id, definition, exact); metrics != nil {
		var data []byte
		var digest [32]byte
		var face FontFace
		var err error
		if typed, ok := metrics.(*sfntFontMetrics); ok {
			data, digest, face, err = typed.resolvedWrite()
		} else {
			data = metrics.Write()
			faces, faceErr := (FontFile{Data: data}).Faces()
			if faceErr != nil {
				err = faceErr
			} else {
				face = faces[0]
			}
		}
		if err != nil {
			return ResolvedFont{}, err
		}
		face.Index = source.face
		return ResolvedFont{Data: data, Source: source.name, Face: &face, Exact: source.exact, digest: digest}, nil
	}
	if exact {
		return ResolvedFont{}, fmt.Errorf("font %q is unavailable", id)
	}
	if !r.fontSourcesCache.fallbackRead {
		r.fontSourcesCache.fallbackRead = true
		if basefont.CanLoadSystemFonts() {
			for _, name := range basefont.DefaultSystemNames() {
				data, err := r.readFontSource(fontSource{kind: fontSourceSystem, name: name}, &Font{})
				if err != nil {
					continue
				}
				metrics, err := backend.OpenFont(data)
				if err == nil {
					var written []byte
					var digest [32]byte
					if typed, ok := metrics.(*sfntFontMetrics); ok {
						written, digest = typed.cachedWrite()
					} else {
						written = metrics.Write()
					}
					r.fontSourcesCache.fallback = ResolvedFont{Data: written, digest: digest}
					break
				}
			}
		}
	}
	return r.fontSourcesCache.fallback, nil
}

// fontSources 获取字体来源列表
// 入参: fontID 字体ID, font OFD字体定义
// 返回: []fontSource 字体来源列表
func (r *Renderer) fontSources(fontID string, font *Font) []fontSource {
	cacheKey := fontCandidateKey{id: fontID, fontName: font.FontName, familyName: font.FamilyName, bold: font.Bold, italic: font.Italic}
	if sources, ok := r.fontSourcesCache.shared.getCandidates(cacheKey); ok {
		return sources
	}
	bold, italic := font.Bold, font.Italic
	names := []string{font.FontName, font.FamilyName}
	sources := make([]fontSource, 0)
	seen := make(map[fontSourceKey]bool)
	for _, dir := range r.fontDirs {
		for _, match := range r.matchFontFiles(dir, names, bold, italic) {
			sources = appendFontSource(sources, seen, fontSource{kind: fontSourceFile, name: match.Name, face: match.Face, exact: match.Rank == basefont.MatchExact, priority: min(match.Priority, 0)})
		}
	}
	for index := range r.fontFS {
		for _, match := range r.matchFontFS(index, names, bold, italic) {
			sources = appendFontSource(sources, seen, fontSource{kind: fontSourceFS, index: index, name: match.Name, face: match.Face, exact: match.Rank == basefont.MatchExact, priority: min(match.Priority, 0)})
		}
	}
	sortFontSources(sources)
	systemStart := len(sources)
	if basefont.CanLoadSystemFonts() {
		for _, match := range basefont.FileMatches(systemFontIndex(), names, bold, italic) {
			sources = appendFontSource(sources, seen, fontSource{kind: fontSourceFile, name: match.Name, face: match.Face, exact: match.Rank == basefont.MatchExact, priority: min(match.Priority, 0)})
		}
		for _, name := range basefont.SystemNames(names...) {
			source := fontSource{kind: fontSourceSystem, name: name, exact: true}
			for i, requested := range names {
				if basefont.NormalizeName(name) == basefont.NormalizeName(requested) {
					source.priority = (i - len(names)) * 2
					break
				}
			}
			sources = appendFontSource(sources, seen, source)
		}
	}
	sortFontSources(sources[systemStart:])
	for _, dir := range r.fontDirs {
		for _, match := range r.matchFontFiles(dir, nil, bold, italic) {
			sources = appendFontSource(sources, seen, fontSource{kind: fontSourceFile, name: match.Name, face: match.Face})
		}
	}
	for index := range r.fontFS {
		for _, match := range r.matchFontFS(index, nil, bold, italic) {
			sources = appendFontSource(sources, seen, fontSource{kind: fontSourceFS, index: index, name: match.Name, face: match.Face})
		}
	}
	for index := range r.fontFS {
		names, _ := r.fontFSNames(index)
		for _, name := range names {
			sources = appendFontSource(sources, seen, fontSource{kind: fontSourceFS, index: index, name: name})
		}
	}
	r.fontSourcesCache.shared.setCandidates(cacheKey, sources)
	return sources
}

// matchFontFiles 查找字体文件
// 入参: dir 字体目录, names 字体名称, bold 是否粗体, italic 是否斜体
// 返回: []basefont.FileMatch 字体匹配列表
func (r *Renderer) matchFontFiles(dir string, names []string, bold, italic bool) []basefont.FileMatch {
	candidates, ok := r.fontSourcesCache.shared.getDirectory(dir)
	if !ok {
		files, _ := filepath.Glob(filepath.Join(dir, "*"))
		candidates = basefont.FileCandidates(files, filepath.Base)
		for i, candidate := range candidates {
			file, err := os.Open(candidate.Name)
			if err != nil {
				continue
			}
			candidates = basefont.AppendFileNames(candidates, i, basefont.FileNames(file))
			file.Close()
		}
		r.fontSourcesCache.shared.setDirectory(dir, candidates)
	}
	return basefont.FileMatches(candidates, names, bold, italic)
}

// matchFontFS 匹配字体文件系统中的字体名称
// 入参: index 字体文件系统索引, names 字体名称, bold 是否粗体, italic 是否斜体
// 返回: []basefont.FileMatch 字体匹配列表
func (r *Renderer) matchFontFS(index int, names []string, bold, italic bool) []basefont.FileMatch {
	fsys := r.fontFS[index]
	if fonts, ok := fsys.(*FontFS); ok {
		return basefont.FileMatches(fonts.candidates, names, bold, italic)
	}
	candidates, ok := r.fontSourcesCache.shared.getFilesystem(index)
	if !ok {
		files, err := r.fontFSNames(index)
		if err != nil {
			return nil
		}
		candidates = basefont.FileCandidates(files, path.Base)
		for i, candidate := range candidates {
			file, err := fsys.Open(candidate.Name)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil
				}
				continue
			}
			reader, ok := file.(io.ReaderAt)
			if !ok {
				data, _ := io.ReadAll(file)
				reader = bytes.NewReader(data)
			}
			candidates = basefont.AppendFileNames(candidates, i, basefont.FileNames(reader))
			file.Close()
		}
		r.fontSourcesCache.shared.setFilesystem(index, candidates)
	}
	return basefont.FileMatches(candidates, names, bold, italic)
}

// fontFSNames 复用当前字体来源的目录条目，保留无扩展名文件的回退，失败结果不缓存
// 入参: index 字体文件系统索引
// 返回: []string 只读文件名列表, error 目录读取错误
func (r *Renderer) fontFSNames(index int) ([]string, error) {
	if names, ok := r.fontSourcesCache.shared.getNames(index); ok {
		return names, nil
	}
	var names []string
	var err error
	if glob, ok := r.fontFS[index].(fs.GlobFS); ok {
		names, err = glob.Glob("*")
	} else {
		var entries []fs.DirEntry
		entries, err = fs.ReadDir(r.fontFS[index], ".")
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
	}
	if err == nil {
		r.fontSourcesCache.shared.setNames(index, names)
	}
	return names, err
}
