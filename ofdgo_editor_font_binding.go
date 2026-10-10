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
	"context"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// FontMode 指定字体资源的保存方式，不改变文字位置或压缩选项
type FontMode uint8

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

// changeFont 在独立包快照中修改单个字体，成功后一次提交
// 入参: ctx 取消上下文, id 字体标识, mode 保存方式, file 字体数据, index 集合索引
// 返回: error 检查或提交错误
func (e *Editor) changeFont(ctx context.Context, id string, mode FontMode, file FontFile, index int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reader, err := e.Reader()
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
	if mode == FontExternal {
		if err := pruneExternalFontFiles(ctx, reader, scan.fonts[editorResourceID(id)].location); err != nil {
			return err
		}
	}
	return e.commitFontSnapshot(ctx, reader)
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
	reader, err := e.Reader()
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
		ids := slices.Sorted(maps.Keys(scan.fonts))
		ids = slices.DeleteFunc(ids, func(id string) bool {
			return (mode == FontEmbed) == (scan.fonts[id].location != "")
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
			item.Changed, err = e.changeFontSnapshot(ctx, scan, id, mode, FontFile{}, 0)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				item.Reason = err.Error()
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
				scan, err = scanEditorFonts(ctx, reader, document)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	if err := progress("fonts", len(report), len(report)); err != nil {
		return nil, err
	}
	if changed {
		if err := progress("commit", 0, 1); err != nil {
			return nil, err
		}
		if err := pruneExternalFontFiles(ctx, reader, externalFiles...); err != nil {
			return nil, err
		}
		err = e.commitFontSnapshot(ctx, reader)
	}
	return report, err
}

// changeFontSnapshot 校验字体全部文字引用并修改独立阅读器，不改写正文定位
// 入参: ctx 取消上下文, scan 文档引用, id 字体标识, mode 保存方式, file 字体文件, index 集合索引
// 返回: bool 是否修改, error 字体或结构错误
func (e *Editor) changeFontSnapshot(ctx context.Context, scan *editorFontDocument, id string, mode FontMode, file FontFile, index int) (bool, error) {
	declaration := scan.fonts[editorResourceID(id)]
	if declaration == nil {
		return false, fmt.Errorf("font not found: %s", id)
	}
	if (mode == FontEmbed) == (declaration.location != "") {
		return false, nil
	}
	if scan.unsafe {
		return false, fmt.Errorf("font references include unsupported content")
	}
	if e.backends.FontResources == nil {
		return false, ErrBackendUnavailable
	}
	var data []byte
	var err error
	explicit := mode == FontEmbed && len(file.Data) != 0
	if mode == FontExternal {
		data, err = scan.reader.readFile(declaration.location)
		if err != nil {
			return false, err
		}
		file = FontFile{Data: data}
		if bytes.HasPrefix(data, []byte("ttcf")) {
			index = fontCollectionIndex(data, []string{declaration.font.FontName, declaration.font.FamilyName}, declaration.font.Bold, declaration.font.Italic)
		}
	} else if len(file.Data) == 0 {
		file, err = e.resolveBindingFont(scan, declaration)
		if err != nil {
			return false, err
		}
		index = 0
	}
	parsed, err := e.backends.FontResources.OpenFontResource(file, index)
	if err != nil {
		return false, err
	}
	if mode == FontEmbed {
		if err := validateFontEmbedding(parsed.Data); err != nil {
			return false, err
		}
	}
	changes := make(map[string][]byte)
	verifiedGlyphs := false
	for _, name := range slices.Sorted(maps.Keys(scan.files)) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		file := scan.files[name]
		var patches []editorXMLPatch
		for _, node := range file.texts {
			if editorResourceID(node.attr("Font")) != editorResourceID(id) {
				continue
			}
			var object TextObject
			if err := xml.Unmarshal(file.data[node.start:node.end], &object); err != nil {
				return false, err
			}
			if err := checkFontBinding(object, parsed, mode); err != nil {
				return false, err
			}
			if explicit && len(object.CGTransform) != 0 && !verifiedGlyphs {
				source, err := e.resolveBindingFont(scan, declaration)
				if err != nil || !sameBindingFont(source.Data, parsed.Data) {
					return false, fmt.Errorf("font glyph mapping requires the original font")
				}
				verifiedGlyphs = true
			}
			if mode == FontExternal {
				for _, child := range node.children {
					if child.matchesOFD("CGTransform") {
						patches = append(patches, editorXMLPatch{start: child.start, end: child.end})
					}
				}
			}
		}
		if len(patches) != 0 {
			changes[name] = editorPatchXML(file.data, patches)
		}
	}
	data = declaration.file.data
	node := declaration.node
	if modified, ok := changes[declaration.file.name]; ok {
		data = modified
		node, err = fontBindingNode(data, id)
		if err != nil {
			return false, err
		}
	}
	fontFile := node.child("FontFile")
	if mode == FontEmbed {
		digest := sha256.Sum256(parsed.Data)
		name := fmt.Sprintf("Fonts/%x%s", digest, parsed.Extension)
		if existing, err := scan.reader.readFile(name); err != nil || !bytes.Equal(existing, parsed.Data) {
			name = packageAvailableName(scan.reader, changes, name)
			changes[name] = bytes.Clone(parsed.Data)
		}
		content := node.textXML("FontFile", "/"+name)
		if fontFile != nil {
			data = editorPatchXML(data, []editorXMLPatch{{fontFile.start, fontFile.end, content}})
		} else if node.open == node.end {
			data = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, node, content)})
		} else {
			data = editorPatchXML(data, []editorXMLPatch{{node.close, node.close, content}})
		}
	} else {
		if fontFile == nil {
			return false, fmt.Errorf("font file declaration is missing")
		}
		fragment := editorPatchXML(data[node.start:node.end], []editorXMLPatch{{fontFile.start - node.start, fontFile.end - node.start, nil}})
		for _, field := range []string{"FontName", "FamilyName"} {
			root, err := parseEditorXML(fragment)
			if err != nil {
				return false, err
			}
			value := root.attr(field)
			if stripped := externalFontName(value); stripped != value {
				fragment, err = editorXMLAttribute(fragment, root, field, stripped)
				if err != nil {
					return false, err
				}
			}
		}
		data = editorPatchXML(data, []editorXMLPatch{{node.start, node.end, fragment}})
	}
	if explicit {
		for _, field := range []struct{ name, value string }{{"FontName", parsed.Font.FontName}, {"FamilyName", parsed.Font.FamilyName}, {"Charset", "unicode"}} {
			node, err := fontBindingNode(data, id)
			if err != nil {
				return false, err
			}
			fragment, err := editorXMLAttribute(data, node, field.name, field.value)
			if err != nil {
				return false, err
			}
			data = editorPatchXML(data, []editorXMLPatch{{node.start, node.end, fragment}})
		}
	}
	changes[declaration.file.name] = data
	working := *scan.reader
	working.files = maps.Clone(scan.reader.files)
	next := *scan
	next.reader = &working
	if err := next.apply(ctx, changes); err != nil {
		return false, err
	}
	if err := working.initRoot(); err != nil {
		return false, err
	}
	*scan.reader = working
	return true, nil
}

// fontBindingNode 在修改后的资源XML中重新定位字体声明
// 入参: data 资源XML, id 字体标识
// 返回: *editorXML 字体节点, error 解析或查找错误
func fontBindingNode(data []byte, id string) (*editorXML, error) {
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	if fonts := root.child("Fonts"); fonts != nil {
		for _, node := range fonts.children {
			if node.matchesOFD("Font") && editorResourceID(node.attr("ID")) == editorResourceID(id) {
				return node, nil
			}
		}
	}
	return nil, fmt.Errorf("font declaration is missing")
}

// sameBindingFont 比较独立字体的全部数据表，忽略重新封装产生的校验和差异
// 入参: source 原字体, target 待嵌入字体
// 返回: bool 是否保留原字体内容及字形编号
func sameBindingFont(source, target []byte) bool {
	a, err := fontFileTables(source, 0)
	if err != nil {
		return false
	}
	b, err := fontFileTables(target, 0)
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

// resolveBindingFont 精确匹配当前字体并提取独立字体数据，集合索引不传递到提取结果
// 入参: scan 文档引用, declaration 字体声明
// 返回: FontFile 独立字体, error 匹配或解析错误
func (e *Editor) resolveBindingFont(scan *editorFontDocument, declaration *editorFontDeclaration) (FontFile, error) {
	reader := &Reader{Zip: scan.reader.Zip, files: maps.Clone(scan.reader.files), documentIndex: scan.index}
	if scan.index == scan.reader.documentIndex {
		reader.selectedVersion = scan.reader.selectedVersion
	}
	if err := reader.initRoot(); err != nil {
		return FontFile{}, err
	}
	if _, err := reader.Doc(); err != nil {
		return FontFile{}, err
	}
	definition := declaration.font
	definition.FontFile = ""
	reader.fontCache[definition.ID] = &definition
	resolved, err := e.newRenderer(reader).ResolveFont(definition.ID, true)
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
	return FontFile{Data: data}, err
}

// checkFontBinding 核对字符覆盖及字形编号，外置仅允许等价的一对一字符映射
// 入参: object 文字对象, font 目标字体, mode 保存方式
// 返回: error 字符或字形无法保留的原因
func checkFontBinding(object TextObject, font *FontResource, mode FontMode) error {
	var chars []rune
	for _, code := range object.TextCode {
		if code.Index != "" {
			return fmt.Errorf("font uses index-encoded text")
		}
		chars = append(chars, textCodeRunes(code.Value)...)
	}
	covered := make(map[int]bool)
	for _, transform := range object.CGTransform {
		values := strings.Fields(transform.Glyphs)
		if transform.CodePosition < 0 || transform.CodeCount < 1 || transform.GlyphCount < 1 || transform.CodePosition > len(chars)-transform.CodeCount || transform.GlyphCount != len(values) {
			return fmt.Errorf("invalid font glyph mapping")
		}
		if mode == FontExternal && transform.CodeCount != transform.GlyphCount {
			return fmt.Errorf("font uses non-character glyph mapping")
		}
		for i, value := range values {
			glyph, err := strconv.ParseUint(value, 10, 16)
			if err != nil || glyph == 0 || glyph >= uint64(font.NumGlyphs()) {
				return fmt.Errorf("font glyph is unavailable")
			}
			if mode == FontExternal && uint16(glyph) != font.GlyphIndex(chars[transform.CodePosition+i]) {
				return fmt.Errorf("font uses non-character glyph mapping")
			}
		}
		for i := transform.CodePosition; i < transform.CodePosition+transform.CodeCount; i++ {
			if covered[i] {
				return fmt.Errorf("overlapping font glyph mapping")
			}
			covered[i] = true
		}
	}
	for i, char := range chars {
		if mode == FontExternal && (char >= 0xe000 && char <= 0xf8ff || char >= 0xf0000 && char <= 0x10fffd) {
			return fmt.Errorf("font uses private character mapping")
		}
		if !covered[i] && font.GlyphIndex(char) == 0 {
			return fmt.Errorf("font has no glyph for U+%04X", char)
		}
	}
	return nil
}

// externalFontName 去除PDF字体子集前缀，不猜测或改写其他名称
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

// commitFontSnapshot 重新建立资源缓存并记录包快照，避免旧字体缓存跨撤销或重做复用
// 入参: ctx 取消上下文, reader 修改后的独立快照
// 返回: error 重建错误
func (e *Editor) commitFontSnapshot(ctx context.Context, reader *Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	clean := &Reader{Zip: reader.Zip, files: maps.Clone(reader.files), documentIndex: reader.DocumentIndex(), encryption: e.encryption, selectedVersion: reader.selectedVersion}
	if err := clean.initRoot(); err != nil {
		return err
	}
	next, err := clean.Editor()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	before := e.transactionSnapshot()
	e.restoreFontSnapshot(*next)
	if change := e.recordChange(); change != nil {
		after := e.transactionSnapshot()
		change.undo = func(e *Editor) { e.restoreFontSnapshot(before) }
		change.redo = func(e *Editor) { e.restoreFontSnapshot(after) }
	}
	return nil
}

// restoreFontSnapshot 恢复字体修改前后的包状态，保留运行配置与撤销队列
// 入参: state 包快照
func (e *Editor) restoreFontSnapshot(state Editor) {
	state = state.transactionSnapshot()
	state.history, state.historyIndex, state.historyLimit = e.history, e.historyIndex, e.historyLimit
	state.serial, state.revision = e.serial, e.revision
	state.backends, state.fontDirs, state.fontFS = e.backends, e.fontDirs, e.fontFS
	state.encryption, state.output, state.OnWriteProgress = e.encryption, e.output, e.OnWriteProgress
	state.fontRenderer, state.fontSourcesCache = nil, nil
	state.fontMetrics = make(map[string]FontMetrics)
	*e = state
}

// pruneExternalFontFiles 清理全包不再引用的字体文件，遇到无法识别的扩展时保留资源
// 入参: ctx 取消上下文, reader 包快照, candidates 待清理的字体路径
// 返回: error 取消或读取错误
func pruneExternalFontFiles(ctx context.Context, reader *Reader, candidates ...string) error {
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
	for i := range reader.DocumentCount() {
		files, err := reader.documentFiles(ctx, i)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return nil
		}
		for name := range files {
			delete(removed, name)
		}
		if len(removed) == 0 {
			return nil
		}
	}
	for name := range reader.fileNames {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !strings.HasSuffix(strings.ToLower(name), ".xml") {
			continue
		}
		data, err := reader.readFile(name)
		if err != nil {
			return err
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return nil
		}
		base := ""
		if root.matchesOFD("Res") {
			base = root.attr("BaseLoc")
		}
		var retains func(*editorXML) bool
		retains = func(node *editorXML) bool {
			if classifyOFDNamespace(node.name.Space) == ofdXMLUnknown || slices.Contains([]string{"CustomTags", "Extensions", "ExtendData", "Data"}, node.name.Local) {
				return true
			}
			for _, attr := range node.attrs {
				if attr.Name.Space != "" && attr.Name.Space != "xmlns" && attr.Name.Space != "http://www.w3.org/XML/1998/namespace" {
					return true
				}
			}
			if node.matchesOFD("FontFile") {
				location := resolveResourcePath(name, base, strings.TrimSpace(editorImportText(data, node)))
				delete(removed, cleanPackagePath(location))
			}
			return slices.ContainsFunc(node.children, retains)
		}
		if retains(root) || len(removed) == 0 {
			return nil
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for name := range reader.files {
		if removed[cleanPackagePath(name)] {
			delete(reader.files, name)
		}
	}
	if reader.Zip != nil {
		reader.Zip = &zip.Reader{Comment: reader.Zip.Comment, File: slices.DeleteFunc(slices.Clone(reader.Zip.File), func(file *zip.File) bool { return removed[cleanPackagePath(file.Name)] })}
	}
	return reader.initRoot()
}
