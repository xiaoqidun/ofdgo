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
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
)

// FontMode 指定字体资源的保存方式，不改变文字位置或压缩选项
type FontMode uint8

// 字体保存方式枚举常量
const (
	// FontKeep 保留原有嵌入方式
	FontKeep FontMode = iota
	// FontEmbed 嵌入可精确匹配的外部字体
	FontEmbed
	// FontExternal 移除可安全恢复字符映射的内嵌字体
	FontExternal
)

// FontChange 返回单个字体的处理结果，Document为零基文档索引，Reason非空表示保持原样
type FontChange struct {
	Document int    `json:"document"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Changed  bool   `json:"changed"`
	Reason   string `json:"reason,omitempty"`
}

// FontProcessOptions 设置全包字体处理方式及进度回调
// OnProgress按prepare、scan、fonts、commit阶段同步回报进度，total为0表示总量未知
// 回调返回错误时不提交修改，不可在回调中修改编辑器
type FontProcessOptions struct {
	Mode       FontMode
	OnProgress func(stage string, completed, total int) error
}

// EmbedFont 嵌入当前文档指定字体，保留资源标识及全部使用位置，成功后可撤销
// file为空时从编辑器字体来源精确匹配；显式文件表示调用方确认替换，仍校验字符和字形
// 入参: ctx 取消上下文, id 字体标识, file 字体文件, index 集合索引
// 返回: error 字体、映射或包结构错误，失败时文档不变
func (e *Editor) EmbedFont(ctx context.Context, id string, file FontFile, index int) error {
	return e.changeFont(ctx, id, FontEmbed, file, index)
}

// ExternalizeFont 将当前文档指定字体改为外部引用，不保证其他设备具备该字体
// 仅移除可由原字体字符映射等价恢复的显式字形变换，无法确认的映射保持内嵌并返回错误
// 入参: ctx 取消上下文, id 字体标识
// 返回: error 字体、映射或包结构错误，失败时文档不变
func (e *Editor) ExternalizeFont(ctx context.Context, id string) error {
	return e.changeFont(ctx, id, FontExternal, FontFile{}, 0)
}

// ProcessFonts 处理包内各文档的字体，逐字体保留不能转换的资源，取消或包结构错误时整体不提交
// 返回成功与未处理明细，整个操作计入一条撤销记录
// 入参: ctx 取消上下文, mode 保存方式
// 返回: []FontChange 字体处理结果, error 取消或结构错误
func (e *Editor) ProcessFonts(ctx context.Context, mode FontMode) ([]FontChange, error) {
	return e.ProcessFontsWithOptions(ctx, FontProcessOptions{Mode: mode})
}

// ProcessFontsWithOptions 处理包内字体并回报已处理数量，失败或取消时不提交修改
// 入参: ctx 取消上下文, options 字体处理选项
// 返回: []FontChange 字体处理结果, error 取消、回调或结构错误
func (e *Editor) ProcessFontsWithOptions(ctx context.Context, options FontProcessOptions) ([]FontChange, error) {
	mode := options.Mode
	if mode > FontExternal {
		return nil, fmt.Errorf("invalid font mode")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if mode == FontKeep {
		return nil, nil
	}
	progress := func(stage string, completed, total int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := editorProgress(options.OnProgress).report(stage, completed, total); err != nil {
			return err
		}
		return ctx.Err()
	}
	if err := progress("prepare", 0, 0); err != nil {
		return nil, err
	}
	reader, err := e.Reader(ctx)
	if err != nil {
		return nil, err
	}
	var report []FontChange
	var externalFiles []string
	changed := false
	for document := range reader.DocumentCount() {
		if err := progress("scan", document, reader.DocumentCount()); err != nil {
			return nil, err
		}
		scan, err := scanEditorFonts(ctx, reader, document)
		if err != nil {
			return nil, err
		}
		pending := &editorFontChange{patches: make(map[string][]editorXMLPatch), files: make(map[string][]byte)}
		ids := slices.Sorted(maps.Keys(scan.fonts))
		ids = slices.DeleteFunc(ids, func(id string) bool {
			return !scan.duplicateIDs[id] && (mode == FontEmbed) == (scan.fonts[id].location != "")
		})
		total := 0
		if document == reader.DocumentCount()-1 {
			total = len(report) + len(ids)
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			declaration := scan.fonts[id]
			if err := progress("fonts", len(report), total); err != nil {
				return nil, err
			}
			item := FontChange{Document: document, ID: id, Name: declaration.font.FontName}
			change, err := e.prepareFontBinding(ctx, scan, id, mode, FontFile{}, 0, pending.files)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				item.Reason = err.Error()
			}
			item.Changed = change != nil
			if change != nil {
				for name, patches := range change.patches {
					pending.patches[name] = append(pending.patches[name], patches...)
				}
				maps.Copy(pending.files, change.files)
				pending.fonts = append(pending.fonts, change.fonts...)
			}
			report = append(report, item)
			if err := progress("fonts", len(report), total); err != nil {
				return nil, err
			}
			if item.Changed {
				changed = true
				if mode == FontExternal {
					externalFiles = append(externalFiles, declaration.location)
				}
			}
		}
		if err := pending.apply(ctx, scan); err != nil {
			return nil, err
		}
	}
	if err := progress("fonts", len(report), len(report)); err != nil {
		return nil, err
	}
	if changed {
		if err := progress("commit", 0, 1); err != nil {
			return nil, err
		}
		if err := pruneUnreferencedFiles(ctx, reader, externalFiles...); err != nil {
			return nil, err
		}
		err = e.commitResourceSnapshot(ctx, reader)
	}
	return report, err
}

// editorFontChange 汇总同一文档的字体修改，XML偏移均基于修改前的文件
type editorFontChange struct {
	patches map[string][]editorXMLPatch
	files   map[string][]byte
	fonts   []string
}

// apply 合并同一文档的XML修改，再统一隔离其他文档及版本使用的文件
// 入参: ctx 取消上下文, scan 修改前的文档引用
// 返回: error 取消、引用或结构错误
func (c *editorFontChange) apply(ctx context.Context, scan *editorFontDocument) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(c.patches) == 0 && len(c.files) == 0 {
		return nil
	}
	changes := maps.Clone(c.files)
	for name, patches := range c.patches {
		if err := ctx.Err(); err != nil {
			return err
		}
		changes[name] = editorPatchXML(scan.files[name].data, patches)
	}
	if err := invalidateResourcePreviews(ctx, scan, changes, c.fonts...); err != nil {
		return err
	}
	return scan.apply(ctx, changes)
}

// editorBindingFontCache 仅保留当前文档最近读取的字体文件及最近解析结果，操作结束后释放
type editorBindingFontCache struct {
	source   string
	data     []byte
	checksum [32]byte
	name     string
	index    int
	font     *FontResource
}

// read 复用当前文档最近读取的字体，避免共享引用重复解压，不缓存错误或取消结果
// 入参: ctx 取消上下文, reader 包快照, name 字体路径
// 返回: []byte 只读字体数据, error 读取或取消错误
func (c *editorBindingFontCache) read(ctx context.Context, reader *Reader, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name = cleanPackagePath(name)
	if c.source == name && c.data != nil {
		return c.data, nil
	}
	c.source, c.data = "", nil
	data, err := reader.readFileViewContext(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.source, c.data = name, data
	return data, nil
}

// open 按字体内容、名称及集合索引复用最近的解析结果，不缓存错误或取消结果
// 入参: ctx 取消上下文, backend 字体资源后端, file 字体文件, index 集合索引
// 返回: *FontResource 只读字体资源, error 解析或取消错误
func (c *editorBindingFontCache) open(ctx context.Context, backend FontResourceBackend, file FontFile, index int) (*FontResource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	checksum := sha256.Sum256(file.Data)
	if c.font != nil && c.checksum == checksum && c.name == file.Name && c.index == index {
		return c.font, ctx.Err()
	}
	c.font = nil
	font, err := backend.OpenFontResource(file, index)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.checksum, c.name, c.index, c.font = checksum, file.Name, index, font
	return font, nil
}

// changeFont 在独立包快照中修改单个字体，成功后一次提交
// 入参: ctx 取消上下文, id 字体标识, mode 保存方式, file 字体数据, index 集合索引
// 返回: error 检查或提交错误
func (e *Editor) changeFont(ctx context.Context, id string, mode FontMode, file FontFile, index int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reader, err := e.Reader(ctx)
	if err != nil {
		return err
	}
	scan, err := scanEditorFonts(ctx, reader, reader.DocumentIndex())
	if err != nil {
		return err
	}
	changed, err := e.changeFontSnapshot(ctx, scan, id, mode, file, index)
	if err != nil || !changed {
		return err
	}
	if location := scan.fonts[editorResourceID(id)].location; location != "" {
		if err := pruneUnreferencedFiles(ctx, reader, location); err != nil {
			return err
		}
	}
	return e.commitResourceSnapshot(ctx, reader)
}

// changeFontSnapshot 校验字体全部文字引用并修改独立阅读器，不改写正文定位
// 入参: ctx 取消上下文, scan 文档引用, id 字体标识, mode 保存方式, file 字体文件, index 集合索引
// 返回: bool 是否修改, error 字体或结构错误
func (e *Editor) changeFontSnapshot(ctx context.Context, scan *editorFontDocument, id string, mode FontMode, file FontFile, index int) (bool, error) {
	change, err := e.prepareFontBinding(ctx, scan, id, mode, file, index, nil)
	if err != nil || change == nil {
		return false, err
	}
	if err := change.apply(ctx, scan); err != nil {
		return false, err
	}
	return true, nil
}

// prepareFontBinding 校验单个字体并生成独立修改集，不改变文档及已准备的资源
// 入参: ctx 取消上下文, scan 文档引用, id 字体标识, mode 保存方式, file 字体文件, index 集合索引, pending 已准备的资源文件
// 返回: *editorFontChange 字体修改，无需处理时为nil, error 字体或结构错误
func (e *Editor) prepareFontBinding(ctx context.Context, scan *editorFontDocument, id string, mode FontMode, file FontFile, index int, pending map[string][]byte) (*editorFontChange, error) {
	key := editorResourceID(id)
	declaration := scan.fonts[key]
	if declaration == nil {
		return nil, fmt.Errorf("font not found: %s", id)
	}
	if scan.duplicateIDs[key] {
		return nil, fmt.Errorf("ambiguous font ID: %s", id)
	}
	explicit := mode == FontEmbed && len(file.Data) != 0
	if (mode == FontEmbed) == (declaration.location != "") && !explicit {
		return nil, nil
	}
	if scan.unsafe {
		return nil, fmt.Errorf("font references include unsupported content")
	}
	if e.backends.FontResources == nil {
		return nil, ErrBackendUnavailable
	}
	var data []byte
	var err error
	if mode == FontExternal {
		data, err = scan.fontCache.read(ctx, scan.reader, declaration.location)
		if err != nil {
			return nil, err
		}
		file = FontFile{Data: data}
		if bytes.HasPrefix(data, []byte("ttcf")) {
			index = basefont.CollectionIndex(data, []string{declaration.font.FontName, declaration.font.FamilyName}, declaration.font.Bold, declaration.font.Italic)
		}
	} else if len(file.Data) == 0 {
		file, err = e.resolveBindingFont(ctx, scan, declaration)
		if err != nil {
			return nil, err
		}
		index = 0
	}
	parsed, err := scan.fontCache.open(ctx, e.backends.FontResources, file, index)
	if err != nil {
		return nil, err
	}
	if mode == FontEmbed {
		if err := basefont.ValidateEmbedding(parsed.Data); err != nil {
			return nil, err
		}
	}
	var original FontFile
	var originalErr error
	sameEmbedded := false
	if explicit && declaration.location != "" {
		original, originalErr = originalBindingFont(ctx, scan, declaration)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sameEmbedded = originalErr == nil && sameBindingFont(original.Data, parsed.Data)
	}
	change := &editorFontChange{patches: make(map[string][]editorXMLPatch), files: make(map[string][]byte)}
	verifiedGlyphs := false
	for _, text := range scan.texts[editorResourceID(id)] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, node := text.file, text.node
		var object TextObject
		decoder := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(file.data[node.start:node.end])), ctx: ctx})
		if err := decoder.Decode(&object); err != nil {
			return nil, err
		}
		if err := checkFontBinding(ctx, object, parsed, mode); err != nil {
			return nil, err
		}
		if explicit && len(object.CGTransform) != 0 && !verifiedGlyphs {
			source, err := original, originalErr
			if declaration.location == "" {
				source, err = e.resolveBindingFont(ctx, scan, declaration)
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			if err != nil || !sameBindingFont(source.Data, parsed.Data) {
				return nil, fmt.Errorf("font glyph mapping requires the original font")
			}
			verifiedGlyphs = true
		}
		if mode == FontExternal {
			for _, child := range node.children {
				if child.matchesOFD("CGTransform") {
					change.patches[file.name] = append(change.patches[file.name], editorXMLPatch{start: child.start, end: child.end})
				}
			}
		}
	}
	if sameEmbedded {
		return nil, ctx.Err()
	}
	data = declaration.file.data
	node := declaration.node
	fontFile := node.child("FontFile")
	var fragment []byte
	if mode == FontEmbed {
		digest := sha256.Sum256(parsed.Data)
		name, reused, err := fontBindingFile(ctx, scan.reader, pending, fmt.Sprintf("Fonts/%x%s", digest, parsed.Extension), parsed.Data)
		if err != nil {
			return nil, err
		}
		if !reused {
			change.files[name] = bytes.Clone(parsed.Data)
		}
		content := node.textXML("FontFile", "/"+name)
		if fontFile != nil {
			fragment = editorPatchXML(data[node.start:node.end], []editorXMLPatch{{fontFile.start - node.start, fontFile.end - node.start, content}})
		} else if node.open == node.end {
			patch := editorXMLContent(data, node, content)
			patch.start -= node.start
			patch.end -= node.start
			fragment = editorPatchXML(data[node.start:node.end], []editorXMLPatch{patch})
		} else {
			fragment = editorPatchXML(data[node.start:node.end], []editorXMLPatch{{node.close - node.start, node.close - node.start, content}})
		}
	} else {
		if fontFile == nil {
			return nil, fmt.Errorf("font file declaration is missing")
		}
		fragment = editorPatchXML(data[node.start:node.end], []editorXMLPatch{{fontFile.start - node.start, fontFile.end - node.start, nil}})
		for _, field := range []string{"FontName", "FamilyName"} {
			root, err := parseEditorXMLContext(ctx, fragment)
			if err != nil {
				return nil, err
			}
			value := root.attr(field)
			if stripped := externalFontName(value); stripped != value {
				fragment, err = editorXMLAttribute(fragment, root, field, stripped)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	if explicit {
		for _, field := range []struct{ name, value string }{{"FontName", parsed.Font.FontName}, {"FamilyName", parsed.Font.FamilyName}, {"Charset", "unicode"}} {
			root, err := parseEditorXMLContext(ctx, fragment)
			if err != nil {
				return nil, err
			}
			fragment, err = editorXMLAttribute(fragment, root, field.name, field.value)
			if err != nil {
				return nil, err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	change.patches[declaration.file.name] = append(change.patches[declaration.file.name], editorXMLPatch{node.start, node.end, fragment})
	change.fonts = append(change.fonts, editorResourceID(id))
	return change, nil
}

// resolveBindingFont 精确匹配当前字体并提取独立字体数据，集合索引不传递到提取结果
// 同一文档复用入口和资源定义，字体解析结果不跨调用保留
// 入参: ctx 取消上下文, scan 文档引用, declaration 字体声明
// 返回: FontFile 独立字体, error 匹配或解析错误
func (e *Editor) resolveBindingFont(ctx context.Context, scan *editorFontDocument, declaration *editorFontDeclaration) (FontFile, error) {
	if err := ctx.Err(); err != nil {
		return FontFile{}, err
	}
	reader := scan.fontReader
	if reader == nil {
		resources := e.runtimeResources
		if resources == nil {
			resources = scan.reader.resources
		}
		reader = &Reader{Zip: scan.reader.Zip, encrypted: scan.reader.encryptedSource(), sourceFile: scan.reader.sourceFile, files: maps.Clone(scan.reader.files), documentIndex: scan.index, resources: resources}
		if scan.index == scan.reader.documentIndex {
			reader.selectedVersion = scan.reader.selectedVersion
		}
		if err := reader.initRootContext(ctx); err != nil {
			return FontFile{}, err
		}
		if _, err := reader.DocContext(ctx); err != nil {
			return FontFile{}, err
		}
		scan.fontReader = reader
	}
	definition := declaration.font
	definition.FontFile = ""
	local := &parsedRes{}
	local.fonts.put(definition.ID, &definition)
	reader.fontCache[definition.ID] = &definition
	renderer := e.newRenderer(reader)
	defer renderer.bindParsedResources([]*parsedRes{local})()
	renderer.pageContext = ctx
	resolved, err := renderer.ResolveFont(definition.ID, true)
	if err != nil {
		return FontFile{}, err
	}
	if !resolved.Exact {
		return FontFile{}, fmt.Errorf("exact font is unavailable")
	}
	file := FontFile{Data: resolved.Data}
	index := 0
	if bytes.HasPrefix(file.Data, []byte("ttcf")) && resolved.Face != nil {
		index = resolved.Face.Index
	}
	data, err := file.Face(index)
	if cancelErr := ctx.Err(); cancelErr != nil {
		return FontFile{}, cancelErr
	}
	return FontFile{Data: data}, err
}

// commitResourceSnapshot 重建资源缓存并记录包快照，避免旧缓存跨撤销或重做复用
// 入参: ctx 取消上下文, reader 修改后的独立快照
// 返回: error 重建错误
func (e *Editor) commitResourceSnapshot(ctx context.Context, reader *Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sourceFile := reader.sourceFile
	if sourceFile == nil {
		sourceFile = e.sourceFile
	}
	resources := e.runtimeResources
	if resources == nil {
		resources = reader.resources
	}
	pdfDest := e.pdfDestinations.clone()
	if pdfDest == nil {
		pdfDest = reader.pdfDestinations.clone()
	}
	clean := &Reader{Zip: reader.Zip, encrypted: reader.encryptedSource(), sourceFile: sourceFile, files: maps.Clone(reader.files), documentIndex: reader.DocumentIndex(), encryption: e.encryption, selectedVersion: reader.selectedVersion, resources: resources, pdfDestinations: pdfDest}
	if err := clean.initRootContext(ctx); err != nil {
		return err
	}
	next, err := clean.EditorContext(ctx)
	if err != nil {
		return err
	}
	if err := next.retainPendingResources(ctx, e); err != nil {
		return err
	}
	if err := next.retainLoadedStates(ctx, e); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	before := e.transactionSnapshot()
	before.pdfDestinations = e.pdfDestinations.clone()
	e.restoreResourceSnapshot(*next)
	if change := e.recordChange(); change != nil {
		after := e.transactionSnapshot()
		after.pdfDestinations = e.pdfDestinations.clone()
		before.history, after.history = nil, nil
		change.undo = func(e *Editor) {
			after.retainDocumentResources(e)
			e.restoreResourceSnapshot(before)
		}
		change.redo = func(e *Editor) {
			before.retainDocumentResources(e)
			e.restoreResourceSnapshot(after)
		}
	}
	return nil
}

// retainPendingResources 保留未写入包的会话资源及未变化的注册映射，不提前持久化资源
// 入参: ctx 取消上下文, source 修改前的编辑状态
// 返回: error 取消错误
func (e *Editor) retainPendingResources(ctx context.Context, source *Editor) error {
	e.maxID = max(e.maxID, source.maxID)
	if len(source.resources) == 0 && len(source.resourceID) == 0 {
		return ctx.Err()
	}
	used := make(map[string]bool)
	var files map[string]bool
	if len(source.resources) != 0 {
		var fonts, images []editorResource
		var spaces []ColorSpace
		var err error
		fonts, images, spaces, files, _, err = source.usedResources(ctx)
		if err != nil {
			return err
		}
		for _, resource := range fonts {
			used[resource.font.ID] = true
		}
		for _, resource := range images {
			used[resource.image.ID] = true
		}
		for _, space := range spaces {
			used[space.ID] = true
		}
	}
	retained := make(map[string]bool)
	registered := make(map[string]editorResource)
	for _, resource := range source.resources {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := resource.definition()
		switch {
		case resource.font != nil:
			id = resource.font.ID
		case resource.image != nil:
			id = resource.image.ID
		case resource.space != nil:
			id = resource.space.ID
		}
		registered[id] = resource
		if id == "" || used[id] || files[resource.name] {
			continue
		}
		_, exists, err := e.sourceResourceFileContext(ctx, id, "")
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		e.resources = append(e.resources, resource)
		retained[id] = true
		if font := source.fonts[id]; font != nil {
			e.fonts[id] = font
		}
		if size, exists := source.images[id]; exists {
			e.images[id] = size
		}
	}
	checked := make(map[string]bool)
	for key, id := range source.resourceID {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !retained[id] && !checked[id] {
			checked[id] = true
			resource, local := registered[id]
			if !local {
				resource = source.registeredResource(id)
			}
			unchanged, err := e.matchesRegisteredResource(ctx, source, resource, id, local)
			if err != nil {
				return err
			}
			retained[id] = unchanged
		}
		if retained[id] {
			e.resourceID[key] = id
			if font := source.fonts[id]; font != nil {
				e.fonts[id] = font
			}
			if size, exists := source.images[id]; exists {
				e.images[id] = size
			}
		}
	}
	if retained[source.fontRegistration.id] {
		e.fontRegistration = source.fontRegistration
	}
	return ctx.Err()
}

// retainLoadedStates 在重建资源快照后保留已加载页面及注解的内存会话状态
// 入参: ctx 取消上下文, source 修改前的编辑状态
// 返回: error 页面读取或取消错误
func (e *Editor) retainLoadedStates(ctx context.Context, source *Editor) error {
	if source.source != nil && len(source.source.annotationStates) != 0 && e.source != nil {
		cloned := make(map[string]map[string]editorCompositeState, len(source.source.annotationStates))
		for key, states := range source.source.annotationStates {
			cloned[key] = maps.Clone(states)
		}
		e.source.annotationStates = cloned
	}
	pageMap := make(map[string]int, len(e.pages))
	for i, page := range e.pages {
		pageMap[page.ID] = i
	}
	for _, oldPage := range source.pages {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(oldPage.Content.Layer) == 0 {
			continue
		}
		nextIndex, ok := pageMap[oldPage.ID]
		if !ok {
			continue
		}
		saved := make(map[string]GraphicObject)
		for _, layer := range oldPage.Content.Layer {
			for _, obj := range layer.Objects {
				if obj.state != (editorCompositeState{}) || obj.TextObject.layout != nil || len(obj.CompositeGraphicUnit.states) != 0 {
					saved[editorObjectID(obj)] = obj
				}
			}
		}
		if len(saved) == 0 {
			continue
		}
		page, err := e.pageContext(ctx, nextIndex)
		if err != nil {
			return err
		}
		for i := range page.Content.Layer {
			for j := range page.Content.Layer[i].Objects {
				target := &page.Content.Layer[i].Objects[j]
				prev, found := saved[editorObjectID(*target)]
				if !found {
					continue
				}
				target.state = prev.state
				if target.Type == "TextObject" && prev.TextObject.layout != nil && target.TextObject.textDigest() == prev.TextObject.textDigest() {
					target.TextObject.layout = prev.TextObject.layout
				}
				if len(prev.CompositeGraphicUnit.states) != 0 {
					target.CompositeGraphicUnit.states = maps.Clone(prev.CompositeGraphicUnit.states)
				}
			}
		}
	}
	return ctx.Err()
}

// registeredResource 获取已加载的资源定义及文件路径，不扫描页面或其他文档
// 入参: id 资源标识
// 返回: editorResource 资源定义，未找到时为空
func (e *Editor) registeredResource(id string) editorResource {
	if e.source == nil {
		return editorResource{}
	}
	reader := e.source.reader
	if font, ok := e.sourceFont(id); ok && font != nil {
		resource := editorResource{font: font}
		if font.FontFile != "" {
			resource.name = reader.ResPath(font.FontFile)
		}
		return resource
	}
	if media, ok := e.sourceMedia(id); ok && media != nil {
		return editorResource{image: media, name: reader.ResPath(media.MediaFile)}
	}
	if space, ok := e.sourceColorSpace(id); ok && space != nil && space.Profile != "" {
		return editorResource{space: space, name: reader.ResPath(space.Profile)}
	}
	return editorResource{}
}

// matchesRegisteredResource 核对同一注册标识的定义与内容，允许文件迁移但不复用已变化的资源
// 入参: ctx 取消上下文, source 原状态, before 原资源, id 资源标识, local 是否为会话资源
// 返回: bool 是否可保留注册映射, error 取消错误
func (e *Editor) matchesRegisteredResource(ctx context.Context, source *Editor, before editorResource, id string, local bool) (bool, error) {
	after := e.registeredResource(id)
	switch {
	case before.font != nil && after.font != nil:
		x, y := *before.font, *after.font
		if (x.FontFile == "") != (y.FontFile == "") {
			return false, ctx.Err()
		}
		x.FontFile, y.FontFile = "", ""
		if x != y {
			return false, ctx.Err()
		}
		if before.font.FontFile == "" {
			return true, ctx.Err()
		}
	case before.image != nil && after.image != nil:
		x, y := *before.image, *after.image
		x.MediaFile, y.MediaFile = "", ""
		if x != y {
			return false, ctx.Err()
		}
	case before.space != nil && after.space != nil:
		if before.space.Type != after.space.Type {
			return false, ctx.Err()
		}
	default:
		return false, ctx.Err()
	}
	data := before.data
	if !local {
		var err error
		data, err = source.source.reader.readFileViewContext(ctx, before.name)
		if err != nil {
			return false, ctx.Err()
		}
	}
	if len(data) == 0 || after.name == "" {
		return false, ctx.Err()
	}
	matched, err := packageDataEqual(ctx, e.source.reader, after.name, data)
	if err != nil {
		return false, ctx.Err()
	}
	return matched, ctx.Err()
}

// restoreResourceSnapshot 恢复资源修改前后的包状态，保留运行配置与撤销队列
// 入参: state 包快照
func (e *Editor) restoreResourceSnapshot(state Editor) {
	dest := state.pdfDestinations
	state = state.transactionSnapshot()
	if dest != nil {
		state.pdfDestinations = dest.clone()
	} else {
		state.pdfDestinations = e.pdfDestinations.clone()
	}
	state.history, state.historyIndex, state.historyLimit = e.history, e.historyIndex, e.historyLimit
	state.serial, state.revision = e.serial, e.revision
	if !sameBackend(state.backends.FontResources, e.backends.FontResources) {
		state.clearFontResourceCache()
	}
	state.backends, state.fontDirs, state.fontFS = e.backends, e.fontDirs, e.fontFS
	state.SetResources(e.runtimeResources)
	state.encryption, state.output, state.OnWriteProgress = e.encryption, e.output, e.OnWriteProgress
	state.saveTarget, state.sourceFile = e.saveTarget, e.sourceFile
	state.fontRenderer, state.fontSourcesCache = nil, nil
	state.fontMetrics = make(map[string]FontMetrics)
	*e = state
}

// originalBindingFont 提取包内原字体供字形映射核对，不使用外部同名字体代替
// 入参: ctx 取消上下文, scan 文档引用, declaration 字体声明
// 返回: FontFile 独立字体, error 读取、解析或取消错误
func originalBindingFont(ctx context.Context, scan *editorFontDocument, declaration *editorFontDeclaration) (FontFile, error) {
	data, err := scan.fontCache.read(ctx, scan.reader, declaration.location)
	if err != nil {
		return FontFile{}, err
	}
	index := 0
	if bytes.HasPrefix(data, []byte("ttcf")) {
		index = basefont.CollectionIndex(data, []string{declaration.font.FontName, declaration.font.FamilyName}, declaration.font.Bold, declaration.font.Italic)
	}
	data, err = (FontFile{Data: data}).Face(index)
	if cancelErr := ctx.Err(); cancelErr != nil {
		return FontFile{}, cancelErr
	}
	return FontFile{Data: data}, err
}

// fontBindingFile 查找未占用的字体路径，同名内容一致时复用已有文件或本次待写资源
// 入参: ctx 取消上下文, reader 包快照, pending 待写资源, name 期望路径, data 字体内容
// 返回: string 包内路径, bool 是否复用, error 取消错误
func fontBindingFile(ctx context.Context, reader *Reader, pending map[string][]byte, name string, data []byte) (string, bool, error) {
	base, extension := strings.TrimSuffix(name, path.Ext(name)), path.Ext(name)
	for i := 0; ; i++ {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		candidate := name
		if i != 0 {
			candidate = base + "_" + strconv.Itoa(i) + extension
		}
		existing, occupied := pending[candidate]
		if !occupied {
			existing, occupied = reader.files[candidate]
			if !occupied {
				occupied = reader.hasPackageFile(candidate)
				if occupied {
					var err error
					existing, err = reader.readFileViewContext(ctx, candidate)
					if err != nil {
						continue
					}
				}
			}
		}
		if !occupied || bytes.Equal(existing, data) {
			return candidate, occupied, ctx.Err()
		}
	}
}

// sameBindingFont 比较独立字体的全部数据表，忽略重新封装产生的校验和差异
// 入参: source 原字体, target 待嵌入字体
// 返回: bool 是否保留原字体内容及字形编号
func sameBindingFont(source, target []byte) bool {
	a, err := basefont.FileTables(source, 0)
	if err != nil {
		return false
	}
	b, err := basefont.FileTables(target, 0)
	if err != nil || len(a) != len(b) {
		return false
	}
	for tag, data := range a {
		other := b[tag]
		if tag == "head" && len(data) >= 12 && len(other) >= 12 {
			if !bytes.Equal(data[:8], other[:8]) || !bytes.Equal(data[12:], other[12:]) {
				return false
			}
		} else if !bytes.Equal(data, other) {
			return false
		}
	}
	return true
}

// checkFontBinding 核对字符覆盖及字形编号，外置仅允许等价的一对一字符映射
// 入参: ctx 取消上下文, object 文字对象, font 目标字体, mode 保存方式
// 返回: error 字符、字形或取消错误
func checkFontBinding(ctx context.Context, object TextObject, font *FontResource, mode FontMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var chars []rune
	for _, code := range object.TextCode {
		if code.Index != "" {
			return fmt.Errorf("font uses index-encoded text")
		}
		values, err := textCodeRunesContext(ctx, code.Value)
		if err != nil {
			return err
		}
		if len(chars) == 0 {
			chars = values
		} else {
			chars = append(chars, values...)
		}
	}
	transforms := slices.Clone(object.CGTransform)
	slices.SortFunc(transforms, func(a, b CGTransform) int { return cmp.Compare(a.CodePosition, b.CodePosition) })
	end := 0
	for _, transform := range transforms {
		if err := ctx.Err(); err != nil {
			return err
		}
		if transform.CodePosition < 0 || transform.CodeCount < 1 || transform.GlyphCount < 1 || transform.CodePosition > len(chars)-transform.CodeCount {
			return fmt.Errorf("invalid font glyph mapping")
		}
		if transform.CodePosition < end {
			return fmt.Errorf("overlapping font glyph mapping")
		}
		end = transform.CodePosition + transform.CodeCount
		if mode == FontExternal && transform.CodeCount != transform.GlyphCount {
			return fmt.Errorf("font uses non-character glyph mapping")
		}
		count := 0
		for value := range strings.FieldsSeq(transform.Glyphs) {
			if count&4095 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if count >= transform.GlyphCount {
				return fmt.Errorf("invalid font glyph mapping")
			}
			glyph, err := strconv.ParseUint(value, 10, 16)
			if err != nil || glyph == 0 || glyph >= uint64(font.NumGlyphs()) {
				return fmt.Errorf("font glyph is unavailable")
			}
			if mode == FontExternal && uint16(glyph) != font.GlyphIndex(chars[transform.CodePosition+count]) {
				return fmt.Errorf("font uses non-character glyph mapping")
			}
			count++
		}
		if count != transform.GlyphCount {
			return fmt.Errorf("invalid font glyph mapping")
		}
	}
	covered := 0
	for i, char := range chars {
		if i&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if mode == FontExternal && (char >= 0xe000 && char <= 0xf8ff || char >= 0xf0000 && char <= 0x10fffd) {
			return fmt.Errorf("font uses private character mapping")
		}
		for covered < len(transforms) && i >= transforms[covered].CodePosition+transforms[covered].CodeCount {
			covered++
		}
		if (covered == len(transforms) || i < transforms[covered].CodePosition) && font.GlyphIndex(char) == 0 {
			return fmt.Errorf("font has no glyph for U+%04X", char)
		}
	}
	return ctx.Err()
}

// externalFontName 去除PDF字体子集前缀
// 入参: name 字体名称
// 返回: string 外部匹配名称
func externalFontName(name string) string {
	if len(name) > 7 && name[6] == '+' {
		for _, char := range name[:6] {
			if char < 'A' || char > 'Z' {
				return name
			}
		}
		return name[7:]
	}
	return name
}

// pruneUnreferencedFiles 流式核对全包文件引用并同步版本清单，保留附件及无法确认的扩展引用
// 入参: ctx 取消上下文, reader 包快照, candidates 待清理的文件路径
// 返回: error 取消或读取错误
func pruneUnreferencedFiles(ctx context.Context, reader *Reader, candidates ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	removed := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		if key := cleanPackagePath(candidate); key != "" {
			removed[key] = true
		}
	}
	if len(removed) == 0 {
		return nil
	}
	indexes := make([]int, reader.DocumentCount())
	for index := range indexes {
		if err := ctx.Err(); err != nil {
			return err
		}
		indexes[index] = index
	}
	_, structures, err := reader.documentDependencies(ctx, indexes...)
	if err != nil {
		return ctx.Err()
	}
	structures["OFD.xml"] = true
	files := make(map[string]bool)
	versions := make(map[string][]versionEditLink)
	names := slices.Sorted(maps.Keys(structures))
	for _, name := range slices.Sorted(maps.Keys(reader.fileNames)) {
		if !structures[name] && strings.HasSuffix(strings.ToLower(name), ".xml") {
			names = append(names, name)
		}
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if files[name] && !structures[name] {
			continue
		}
		root, uncertain := "", false
		inspect := func(node xml.StartElement) error {
			if root == "" {
				root = node.Name.Local
			}
			if classifyOFDNamespace(node.Name.Space) == ofdXMLUnknown || root == "CustomTags" || slices.Contains([]string{"Extensions", "ExtendData", "Data"}, node.Name.Local) {
				uncertain = true
				return fmt.Errorf("unknown file dependencies in %s", name)
			}
			for _, attr := range node.Attr {
				if attr.Name.Space != "" && attr.Name.Space != "xmlns" && attr.Name.Space != XMLNamespace {
					uncertain = true
					return fmt.Errorf("unknown file dependencies in %s", name)
				}
			}
			return nil
		}
		add := func(location string, kind int) {
			location = cleanPackagePath(location)
			if root == "DocVersion" && kind == packageReferenceData {
				return
			}
			delete(removed, location)
			if kind == packageReferenceData {
				files[location] = true
			}
		}
		record := func(node *editorXML, attribute, target string) {
			if root == "DocVersion" && node.name.Local == "File" && attribute == "" && removed[target] {
				versions[name] = append(versions[name], versionEditLink{node: node, target: target})
			}
		}
		err := reader.scanDocumentFileReferences(ctx, name, true, add, record, inspect)
		if uncertain && (root == "CustomTags" || root == "RichText" || root == "Text") {
			data, readErr := reader.readFileViewContext(ctx, name)
			if readErr != nil {
				return readErr
			}
			refs := &editorResourceRefs{ids: make(map[string]bool), files: make(map[string]bool), fonts: make(map[string]*editorFontUsage)}
			if _, safe := refs.scanContext(ctx, bytes.NewReader(data), name); safe {
				uncertain = false
				err = reader.scanDocumentFileReferences(ctx, name, true, add, record, nil)
			}
		}
		if err != nil {
			var syntax *xml.SyntaxError
			if uncertain || errors.As(err, &syntax) {
				return ctx.Err()
			}
			return err
		}
		if len(removed) == 0 {
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	updates := make(map[string][]byte)
	for name, links := range versions {
		if err := ctx.Err(); err != nil {
			return err
		}
		var patches []editorXMLPatch
		for _, link := range links {
			if err := ctx.Err(); err != nil {
				return err
			}
			if removed[link.target] {
				patches = append(patches, editorXMLPatch{start: link.node.start, end: link.node.end})
			}
		}
		if len(patches) != 0 {
			data, err := reader.readFileViewContext(ctx, name)
			if err != nil {
				return err
			}
			updates[name] = editorPatchXML(data, patches)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	view := *reader
	view.files = maps.Clone(reader.files)
	if view.files == nil {
		view.files = make(map[string][]byte)
	}
	maps.Copy(view.files, updates)
	for name := range view.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if removed[cleanPackagePath(name)] {
			delete(view.files, name)
		}
	}
	if view.Zip != nil {
		view.Zip = &zip.Reader{Comment: view.Zip.Comment, File: slices.DeleteFunc(slices.Clone(view.Zip.File), func(file *zip.File) bool { return removed[cleanPackagePath(file.Name)] })}
	}
	if err := view.initRootContext(ctx); err != nil {
		return err
	}
	*reader = view
	return nil
}
