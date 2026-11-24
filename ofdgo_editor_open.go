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
	"bufio"
	"bytes"
	"compress/flate"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"io"
	"maps"
	"path"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
)

// editorMaxIDFastLimit 限制标识字节快扫在内存中缓存的单文件字节上限
const editorMaxIDFastLimit = 4 << 20

// ObjectCapabilities 已有对象可执行的操作，Reason说明受限原因
// Update表示完整替换内容，ReplaceFont表示可显式替换文字字体，Transform表示保留文字定位的几何变换，Order仅限同一图层或末级页块
// Arrange表示可准确度量对齐与旋转范围；Reflow表示可重排文字，LayoutKnown表示段落选项可恢复
// TextContent表示可修改文字内容，未知段落保持原定位，不自动重排
// FitImage表示可按原始图片比例适应或填充现有边界
// ResetCrop表示有可单独还原的会话裁剪
// Paint表示可独立修改纯色填充和描边，不要求重新排版文字
// ReplaceImage表示可替换图片数据，CropImage表示可裁剪图片；内部裁剪与原裁剪取交集
// MissingGlyphs提供缺字导致操作受限时的结构化诊断
// RewriteText表示可保留原定位修改，具体替换仍需校验长度、字形映射和字体覆盖
// Ungroup表示可移除组合容器并保留内部内容，Stretch表示图片及纯图片组合可独立调整宽高
type ObjectCapabilities struct {
	Update        bool
	TextContent   bool
	RewriteText   bool
	Paint         bool
	ReplaceFont   bool
	ReplaceImage  bool
	CropImage     bool
	FitImage      bool
	ResetCrop     bool
	Reflow        bool
	LayoutKnown   bool
	Transform     bool
	Arrange       bool
	Copy          bool
	Delete        bool
	Order         bool
	Ungroup       bool
	Stretch       bool
	Reason        string
	ReasonCode    EditReason
	MissingGlyphs *MissingGlyphError
}

// PageCapabilities 页面可执行的操作，Insert表示可以添加RGB对象；新增空白页面始终可用
type PageCapabilities struct {
	Insert bool
	Copy   bool
	Delete bool
	Move   bool
	Resize bool
}

// editorSource 保留输入包及按需加载的原始页面
type editorSource struct {
	reader              *Reader
	document            *Document
	info                DocInfo
	fallbackDocID       string
	directory           string
	fromNew             bool
	pages               map[string]*editorSourcePage
	origins             map[string]*editorObjectOrigin
	idsReady            bool
	pageResLoaded       bool
	pageResList         []*parsedRes
	annotationStates    map[string]map[string]editorCompositeState
	annotationPages     map[string]bool
	retiredAttachments  map[string]bool
	removedAttachments  map[string]bool
	structureObjects    map[string]map[string]bool
	structureStarts     map[int]map[string]int
	attachmentIDs       map[string]bool
	richTextTagPath     string
	richTextTagIndex    int
	richTextTagKnown    bool
	customTagCount      int
	customTagCountReady bool
	scanCache           *renderCache[editorSourceScanKey, editorScanValue]
	initialFiles        map[string][]byte
	appendOnlyFiles     map[string][]byte
	mayShrinkResources  bool
}

// editorObjectOrigin 保留原对象的XML语义，供复制和局部更新复用
type editorObjectOrigin struct {
	editor   *Editor
	document editorDocumentIdentity
	page     *editorSourcePage
	data     []byte
	node     *editorXML
	object   GraphicObject
	reason   error
}

// editorSourcePage 保存页面原文、解析快照及图层和页块内的对象位置
type editorSourcePage struct {
	ref              Page
	data             []byte
	root             *editorXML
	original         *PageContent
	initialLayers    []Layer
	initialActions   []Action
	initialTemplates []Template
	initialPageRes   []string
	nodes            map[string]*editorXML
	repaired         bool
}

// editorMaxIDScanner 复用工作协程的解压器与读缓冲以加速包内XML标识扫描
type editorMaxIDScanner struct {
	flate io.ReadCloser
	buf   bytes.Buffer
}

// editorMaxIDChunkResult 汇总单批并行标识扫描的局部最大值与首个错误
type editorMaxIDChunkResult struct {
	maxID      uint64
	archiveMax uint64
	err        error
}

// isNonXMLPackageFile 按扩展名识别明显为二进制媒体、字体或签章数据的非XML条目
// 入参: name 包内文件路径
// 返回: bool 是否应跳过XML标识扫描
func isNonXMLPackageFile(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".bmp", ".tif", ".tiff", ".jb2", ".jbig2", ".jp2", ".jpx", ".gif", ".webp",
		".ttf", ".otf", ".ttc", ".otc", ".cff", ".woff", ".woff2", ".icc", ".icm",
		".mp3", ".wav", ".mp4", ".esl", ".ses", ".dat", ".bin":
		return true
	default:
		return false
	}
}

// cloneEditorReader 复用已验证的只读包索引，独立保存可变文档与读取缓存
// 入参: reader 已加载当前文档的阅读器
// 返回: *Reader 不拥有输入文件的独立阅读器
func cloneEditorReader(reader *Reader) *Reader {
	next := *reader
	reader.cloneMutableState(&next)
	next.closer = nil
	next.encHold = nil
	next.ofd = cloneEditorData(reader.ofd)
	doc := *reader.doc
	pages := doc.Pages.Page
	doc.Pages.Page = nil
	doc = cloneEditorData(doc)
	doc.Pages.Page = slices.Clone(pages)
	next.doc = &doc
	next.files = maps.Clone(reader.files)
	next.pageReady = maps.Clone(reader.pageReady)
	next.fileNames = maps.Clone(reader.fileNames)
	return &next
}

// cloneEditorMap 复制对外可变缓存值，避免快照之间共享嵌套切片或指针
// 入参: values 原缓存
// 返回: map[string]T 独立缓存
func cloneEditorMap[T any](values map[string]T) map[string]T {
	result := maps.Clone(values)
	for key, value := range result {
		result[key] = cloneEditorData(value)
	}
	return result
}

// editorXMLMaxID 按内容识别OFD结构并读取最大标识，跳过二进制及非OFD内容
// 入参: input 包内文件流
// 返回: uint64 最大标识, error 错误信息
func editorXMLMaxID(input io.Reader) (uint64, error) {
	buffer := bufio.NewReader(input)
	if found, err := editorXMLPrefix(buffer); err != nil || !found {
		return 0, err
	}
	decoder := xml.NewDecoder(buffer)
	maximum, started := uint64(0), false
	for {
		token, err := decoder.Token()
		var syntax *xml.SyntaxError
		if err == io.EOF || !started && errors.As(err, &syntax) {
			return maximum, nil
		}
		if err != nil {
			return 0, err
		}
		node, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if !started {
			if !isOFDMaxIDRoot([]byte(node.Name.Local)) {
				return 0, nil
			}
			started = true
		}
		for _, attr := range node.Attr {
			if attr.Name.Space == "" && attr.Name.Local == "ID" {
				id, err := strconv.ParseUint(strings.Trim(attr.Value, " \t\r\n"), 10, 32)
				if err == nil {
					maximum = max(maximum, id)
				}
			}
		}
	}
}

// isDefaultRGBColorSpace 判断颜色空间是否为可直接用于新建RGB对象的8位无配置RGB空间
// 入参: space 颜色空间定义
// 返回: bool 是否为默认8位RGB空间
func isDefaultRGBColorSpace(space *ColorSpace) bool {
	return space != nil && space.Type == "RGB" && space.Profile == "" && (space.BitsPerComponent == 0 || space.BitsPerComponent == 8) && len(space.Palette) == 0
}

// isOFDMaxIDRoot 判断根元素本地名是否属于需要扫描标识的OFD结构文件
// 入参: local 根元素本地名称字节
// 返回: bool 是否属于OFD结构根元素
func isOFDMaxIDRoot(local []byte) bool {
	switch string(local) {
	case "OFD", "Document", "Page", "Res", "PageAnnot", "Annotations", "Attachments", "CustomTags":
		return true
	default:
		return false
	}
}

// isFastXMLSpace 判断字节是否为XML标准空白字符
// 入参: b 待判字节
// 返回: bool 是否为XML空白
func isFastXMLSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

// parseFastUint32 无分配解析ST_ID允许的十进制无符号32位整数
// 入参: data 原始属性值字节
// 返回: uint64 解析值, bool 是否有效
func parseFastUint32(data []byte) (uint64, bool) {
	start, end := 0, len(data)
	for start < end && isFastXMLSpace(data[start]) {
		start++
	}
	for end > start && isFastXMLSpace(data[end-1]) {
		end--
	}
	if start >= end {
		return 0, false
	}
	var value uint64
	for i := start; i < end; i++ {
		ch := data[i]
		if ch < '0' || ch > '9' {
			return 0, false
		}
		value = value*10 + uint64(ch-'0')
		if value > maxUnitID {
			return 0, false
		}
	}
	return value, true
}

// parseFastXMLDeclEncoding 检查XML声明中的编码属性是否兼容UTF-8字节扫描
// 入参: decl XML声明主体内容
// 返回: bool 是否允许继续UTF-8快速扫描
func parseFastXMLDeclEncoding(decl []byte) bool {
	lower := bytes.ToLower(decl)
	pos := bytes.Index(lower, []byte("encoding"))
	if pos < 0 {
		return true
	}
	i := pos + len("encoding")
	for i < len(decl) && isFastXMLSpace(decl[i]) {
		i++
	}
	if i >= len(decl) || decl[i] != '=' {
		return false
	}
	i++
	for i < len(decl) && isFastXMLSpace(decl[i]) {
		i++
	}
	if i >= len(decl) || (decl[i] != '"' && decl[i] != '\'') {
		return false
	}
	quote := decl[i]
	i++
	end := bytes.IndexByte(decl[i:], quote)
	if end < 0 {
		return false
	}
	enc := strings.TrimSpace(string(decl[i : i+end]))
	return strings.EqualFold(enc, "utf-8") || strings.EqualFold(enc, "utf8")
}

// scanFastOFDXMLMaxID 对标准UTF-8编码OFD XML执行字节级ID快扫，遇复杂或异常语法回退标准解码器
// 入参: data 完整文件字节
// 返回: uint64 最大标识, bool 是否完成快速扫描，为false时需回退xml.Decoder
func scanFastOFDXMLMaxID(data []byte) (uint64, bool) {
	n := len(data)
	i := 0
	if n >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
		i = 3
	}
	for i < n && isFastXMLSpace(data[i]) {
		i++
	}
	if i >= n || data[i] != '<' {
		return 0, true
	}
	if i+5 < n && data[i+1] == '?' && data[i+2] == 'x' && data[i+3] == 'm' && data[i+4] == 'l' && (isFastXMLSpace(data[i+5]) || data[i+5] == '?') {
		end := bytes.Index(data[i+5:], []byte("?>"))
		if end < 0 {
			return 0, false
		}
		if !parseFastXMLDeclEncoding(data[i+5 : i+5+end]) {
			return 0, false
		}
		i += 5 + end + 2
		for i < n && isFastXMLSpace(data[i]) {
			i++
		}
		if i >= n || data[i] != '<' {
			return 0, false
		}
	}
	var stack [][]byte
	var maximum uint64
	started := false
	for i < n {
		rel := bytes.IndexByte(data[i:], '<')
		if rel < 0 {
			break
		}
		i += rel
		if i+1 >= n {
			return 0, false
		}
		next := data[i+1]
		if next == '!' || next == '?' {
			return 0, false
		}
		if next == '/' {
			i += 2
			nameStart := i
			for i < n && !isFastXMLSpace(data[i]) && data[i] != '>' {
				if data[i] == '<' || data[i] == '/' {
					return 0, false
				}
				i++
			}
			name := data[nameStart:i]
			for i < n && isFastXMLSpace(data[i]) {
				i++
			}
			if i >= n || data[i] != '>' || len(stack) == 0 || !bytes.Equal(stack[len(stack)-1], name) {
				return 0, false
			}
			stack = stack[:len(stack)-1]
			i++
			if len(stack) == 0 {
				for i < n && isFastXMLSpace(data[i]) {
					i++
				}
				if i != n {
					return 0, false
				}
				return maximum, true
			}
			continue
		}
		i++
		nameStart := i
		for i < n && !isFastXMLSpace(data[i]) && data[i] != '>' && data[i] != '/' {
			ch := data[i]
			if ch == '<' || ch == '=' || ch == '"' || ch == '\'' {
				return 0, false
			}
			i++
		}
		fullName := data[nameStart:i]
		if len(fullName) == 0 {
			return 0, false
		}
		if !started {
			local := fullName
			if colon := bytes.IndexByte(fullName, ':'); colon >= 0 {
				if colon == 0 || colon == len(fullName)-1 || bytes.IndexByte(fullName[colon+1:], ':') >= 0 {
					return 0, false
				}
				local = fullName[colon+1:]
			}
			if !isOFDMaxIDRoot(local) {
				return 0, true
			}
			started = true
		}
		selfClosing := false
		for {
			hadSpace := false
			for i < n && isFastXMLSpace(data[i]) {
				hadSpace = true
				i++
			}
			if i >= n {
				return 0, false
			}
			if data[i] == '>' {
				i++
				break
			}
			if data[i] == '/' {
				if i+1 >= n || data[i+1] != '>' {
					return 0, false
				}
				selfClosing = true
				i += 2
				break
			}
			if !hadSpace {
				return 0, false
			}
			attrStart := i
			for i < n && !isFastXMLSpace(data[i]) && data[i] != '=' {
				ch := data[i]
				if ch == '<' || ch == '>' || ch == '/' || ch == '"' || ch == '\'' {
					return 0, false
				}
				i++
			}
			attrName := data[attrStart:i]
			if len(attrName) == 0 {
				return 0, false
			}
			for i < n && isFastXMLSpace(data[i]) {
				i++
			}
			if i >= n || data[i] != '=' {
				return 0, false
			}
			i++
			for i < n && isFastXMLSpace(data[i]) {
				i++
			}
			if i >= n || (data[i] != '"' && data[i] != '\'') {
				return 0, false
			}
			quote := data[i]
			i++
			valEnd := bytes.IndexByte(data[i:], quote)
			if valEnd < 0 {
				return 0, false
			}
			val := data[i : i+valEnd]
			if bytes.IndexByte(val, '<') >= 0 {
				return 0, false
			}
			if len(attrName) == 2 && attrName[0] == 'I' && attrName[1] == 'D' {
				if bytes.IndexByte(val, '&') >= 0 {
					return 0, false
				}
				if id, ok := parseFastUint32(val); ok && id > maximum {
					maximum = id
				}
			}
			i += valEnd + 1
		}
		if !selfClosing {
			stack = append(stack, fullName)
		} else if len(stack) == 0 {
			for i < n && isFastXMLSpace(data[i]) {
				i++
			}
			if i != n {
				return 0, false
			}
			return maximum, true
		}
	}
	return 0, false
}

// sourcePageUntouched 按写时复制切片头快速判断已加载页面是否保持初始未改状态
// 入参: page 当前页面内容, source 原始页面记录
// 返回: bool 是否完全未改动
func sourcePageUntouched(page PageContent, source *editorSourcePage) bool {
	if source == nil || source.original == nil || page.Area != source.original.Area {
		return false
	}
	if len(page.Template) != len(source.initialTemplates) || len(page.Template) > 0 && &page.Template[0] != &source.initialTemplates[0] {
		return false
	}
	if len(page.PageRes) != len(source.initialPageRes) || len(page.PageRes) > 0 && &page.PageRes[0] != &source.initialPageRes[0] {
		return false
	}
	if len(page.Actions) != len(source.initialActions) || len(page.Actions) > 0 && &page.Actions[0] != &source.initialActions[0] {
		return false
	}
	if len(page.Content.Layer) != len(source.initialLayers) || len(page.Content.Layer) > 0 && &page.Content.Layer[0] != &source.initialLayers[0] {
		return false
	}
	return true
}

// editorPreservedObject 判断对象是否仅改变标识、几何和可独立写回的外观字段
// 入参: before 原始快照, after 新快照
// 返回: bool 是否保留原内容
func editorPreservedObject(before, after GraphicObject) bool {
	if before.Type != after.Type {
		return false
	}
	before = cloneEditorData(before)
	before.state = after.state
	before.richText = after.richText
	switch before.Type {
	case "TextObject":
		a, b := &before.TextObject, after.TextObject
		a.ID, a.Boundary, a.CTM, a.Alpha = b.ID, b.Boundary, b.CTM, b.Alpha
		a.Fill, a.Stroke, a.FillColor, a.StrokeColor, a.LineWidth = b.Fill, b.Stroke, b.FillColor, b.StrokeColor, b.LineWidth
		a.LineWidthSet = b.LineWidthSet
		a.MiterLimit, a.MiterLimitSet = b.MiterLimit, b.MiterLimitSet
		a.DashOffset, a.DashPattern, a.Cap, a.Join = b.DashOffset, b.DashPattern, b.Cap, b.Join
		a.DashPatternSet = b.DashPatternSet
	case "PathObject":
		a, b := &before.PathObject, after.PathObject
		a.ID, a.Boundary, a.CTM, a.Alpha = b.ID, b.Boundary, b.CTM, b.Alpha
		a.Fill, a.Stroke, a.FillColor, a.StrokeColor, a.LineWidth = b.Fill, b.Stroke, b.FillColor, b.StrokeColor, b.LineWidth
		a.LineWidthSet = b.LineWidthSet
		a.MiterLimit, a.MiterLimitSet = b.MiterLimit, b.MiterLimitSet
		a.DashOffset, a.DashPattern, a.Cap, a.Join = b.DashOffset, b.DashPattern, b.Cap, b.Join
		a.DashPatternSet = b.DashPatternSet
	case "ImageObject":
		a, b := &before.ImageObject, after.ImageObject
		a.ID, a.Boundary, a.CTM, a.Alpha = b.ID, b.Boundary, b.CTM, b.Alpha
	case "CompositeObject":
		a, b := &before.CompositeObject, after.CompositeObject
		a.ID, a.Boundary, a.CTM, a.Alpha = b.ID, b.Boundary, b.CTM, b.Alpha
	default:
		return false
	}
	a, b := *editorObjectClips(&before), *editorObjectClips(&after)
	if a == nil {
		*editorObjectClips(&before) = b
	} else if b != nil && len(a.Clip) <= len(b.Clip) {
		a.Clip = append(a.Clip, b.Clip[len(a.Clip):]...)
		for i := range a.Clip {
			if len(a.Clip[i].Area) == len(b.Clip[i].Area) {
				for j := range a.Clip[i].Area {
					a.Clip[i].Area[j].CTM = b.Clip[i].Area[j].CTM
				}
			}
		}
	}
	return reflect.DeepEqual(before, after)
}

// editorContainerOrderable 判断容器的直接对象能否在保留页块的前提下排序
// 入参: container 图层或页块
// 返回: bool 是否可排序
func editorContainerOrderable(container *editorXML) bool {
	for _, child := range container.children {
		if !editorXMLTransformable(child) {
			return false
		}
	}
	return true
}

// editorGeometry 获取基本对象的边界与变换
// 入参: object 对象
// 返回: string 边界, string 变换矩阵
func editorGeometry(object GraphicObject) (string, string) {
	switch object.Type {
	case "TextObject":
		return object.TextObject.Boundary, object.TextObject.CTM
	case "PathObject":
		return object.PathObject.Boundary, object.PathObject.CTM
	case "ImageObject":
		return object.ImageObject.Boundary, object.ImageObject.CTM
	case "CompositeObject":
		return object.CompositeObject.Boundary, object.CompositeObject.CTM
	}
	return "", ""
}

// editorObjectXML 编码可编辑对象，包含独立命名空间声明
// 入参: object 对象
// 返回: []byte XML片段, error 错误信息
func editorObjectXML(object GraphicObject) ([]byte, error) {
	data, err := encodeOFDXML(func(x *ofdXML) { x.object(object, true) })
	return bytes.TrimPrefix(data, []byte(xml.Header)), err
}

// validateEditorGeometry 校验几何操作产生的数值，不重新排版或替换字体
// 入参: object 变换后的对象
// 返回: error 错误信息
func validateEditorGeometry(object GraphicObject) error {
	boundary, ctm := editorGeometry(object)
	if _, err := creationBox(boundary); err != nil {
		return err
	}
	if ctm != "" {
		if _, err := creationNumbers(ctm, 6); err != nil {
			return err
		}
	}
	if clips := *editorObjectClips(&object); clips != nil {
		for _, clip := range clips.Clip {
			for _, area := range clip.Area {
				if area.CTM != "" {
					if _, err := creationNumbers(area.CTM, 6); err != nil {
						return fmt.Errorf("invalid clip transform: %w", err)
					}
				}
			}
		}
	}
	var alpha *int
	switch object.Type {
	case "TextObject":
		alpha = object.TextObject.Alpha
	case "PathObject":
		alpha = object.PathObject.Alpha
	case "ImageObject":
		alpha = object.ImageObject.Alpha
	case "CompositeObject":
		alpha = object.CompositeObject.Alpha
	}
	if alpha != nil && (*alpha < 0 || *alpha > 255) {
		return fmt.Errorf("alpha must be between 0 and 255")
	}
	if object.Type == "TextObject" {
		obj := object.TextObject
		if !finite(obj.Size) || obj.Size <= 0 || !finite(obj.LineWidth) || obj.LineWidth < 0 {
			return fmt.Errorf("invalid text geometry")
		}
		for _, code := range obj.TextCode {
			for _, value := range []string{code.X, code.Y} {
				if value != "" {
					if _, err := creationNumbers(value, 1); err != nil {
						return err
					}
				}
			}
			for _, value := range []string{code.DeltaX, code.DeltaY} {
				if value != "" {
					if err := creationDeltas(value); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// parsedHasResource 判断单个已解析资源文件是否包含指定类型与标识的资源定义
// 入参: parsed 已解析资源文件, id 资源标识, kind 资源类型，空字符串匹配任意类型
// 返回: bool 是否包含
func parsedHasResource(parsed *parsedRes, id, kind string) bool {
	if parsed == nil {
		return false
	}
	switch kind {
	case "DrawParam":
		_, ok := parsed.drawParams.get(id)
		return ok
	case "CompositeGraphicUnit":
		_, ok := parsed.composites.get(id)
		return ok
	case "ColorSpace":
		_, ok := parsed.colorSpaces.get(id)
		return ok
	case "Font":
		_, ok := parsed.fonts.get(id)
		return ok
	case "MultiMedia":
		_, ok := parsed.medias.get(id)
		return ok
	default:
		if _, ok := parsed.drawParams.get(id); ok {
			return true
		}
		if _, ok := parsed.composites.get(id); ok {
			return true
		}
		if _, ok := parsed.colorSpaces.get(id); ok {
			return true
		}
		if _, ok := parsed.fonts.get(id); ok {
			return true
		}
		_, ok := parsed.medias.get(id)
		return ok
	}
}

// Editor 打开当前文档进行保真编辑，不修改输入包，不以权限声明或签名限制编辑
// 原权限和签名数据保留，修改受保护内容会使原签名失效，不重新签名
// 页面按需解析，未修改条目直接保留；编辑器及其Reader快照使用期间不得关闭输入Reader
// 版本共享文件在提交修改时隔离，保留其他版本及其文件清单
// 返回: *Editor 编辑器, error 错误信息
func (r *Reader) Editor() (*Editor, error) {
	return r.EditorContext(context.Background())
}

// EditorContext 打开当前文档进行保真编辑，读取及构建页面索引时响应取消
// 编辑器及其快照使用期间不得关闭输入Reader，不修改输入包或限制编辑权限
// 入参: ctx 取消上下文
// 返回: *Editor 编辑器, error 初始化或取消错误
func (r *Reader) EditorContext(ctx context.Context) (*Editor, error) {
	if r.pageReady != nil {
		return nil, ErrIncompleteDocument
	}
	_, err := r.docContext(ctx)
	if err != nil {
		return nil, err
	}
	reader := cloneEditorReader(r)
	doc := reader.doc
	maximum := doc.CommonData.MaxUnitID
	info, err := reader.DocInfo()
	if err != nil {
		return nil, err
	}
	if info.CustomDatas != nil {
		for i := range info.CustomDatas.CustomData {
			info.CustomDatas.CustomData[i].sourceIndex = i + 1
		}
	}
	e := NewEditor()
	e.runtimeResources = r.resources
	e.sourceFile = r.sourceFile
	e.pdfDestinations = r.pdfDestinations.clone()
	directory := cleanPackagePath(reader.rootDir)
	e.source = &editorSource{reader: reader, document: doc, info: cloneEditorData(*info), fallbackDocID: e.Info.DocID, directory: directory, pages: make(map[string]*editorSourcePage, len(doc.Pages.Page)), origins: make(map[string]*editorObjectOrigin), initialFiles: maps.Clone(reader.files)}
	e.Info, e.maxID = cloneEditorData(*info), maximum
	e.ownedReader, e.ownedOwner = reader, e
	e.pages = make([]PageContent, len(doc.Pages.Page))
	sourcePages := make([]editorSourcePage, len(doc.Pages.Page))
	for i, page := range doc.Pages.Page {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if page.ID == "" || e.source.pages[page.ID] != nil {
			return nil, fmt.Errorf("missing or duplicate page ID %q", page.ID)
		}
		sourcePages[i].ref = page
		e.source.pages[page.ID] = &sourcePages[i]
		e.pages[i] = PageContent{ID: page.ID}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.encryption != nil {
		e.encryption = cloneEncryptionState(r.encryption)
		e.ownedReader.encryption = e.encryption
	}
	e.syncEncryptionHold()
	if e.encryption != nil {
		e.encryption.lease.release()
	}
	return e, nil
}

// PageCapabilities 获取页面操作范围，复制和删除由库统一维护标准引用
// 入参: index 页面索引
// 返回: PageCapabilities 操作能力, error 错误信息
func (e *Editor) PageCapabilities(index int) (PageCapabilities, error) {
	return e.PageCapabilitiesContext(context.Background(), index)
}

// PageCapabilitiesContext 获取页面操作范围并响应取消，默认RGB或文档级颜色空间命中时常数时间返回
// 入参: ctx 取消上下文, index 页面索引
// 返回: PageCapabilities 操作能力, error 索引、读取或取消错误
func (e *Editor) PageCapabilitiesContext(ctx context.Context, index int) (PageCapabilities, error) {
	if err := ctx.Err(); err != nil {
		return PageCapabilities{}, err
	}
	if index < 0 || index >= len(e.pages) {
		return PageCapabilities{}, fmt.Errorf("page index %d out of range", index)
	}
	if e.source == nil || e.source.document.CommonData.DefaultCS == 0 {
		return PageCapabilities{Insert: true, Copy: true, Delete: true, Move: true, Resize: true}, nil
	}
	if e.pageNoLocalRes(index) {
		if insert, resolved := e.docDefaultRGB(); resolved {
			return PageCapabilities{Insert: insert, Copy: true, Delete: true, Move: true, Resize: true}, nil
		}
	}
	restore, err := e.bindPageIndexContext(ctx, index)
	if err != nil {
		return PageCapabilities{}, err
	}
	defer restore()
	insert, err := e.sourceRGBContext(ctx)
	if err != nil {
		return PageCapabilities{}, err
	}
	return PageCapabilities{Insert: insert, Copy: true, Delete: true, Move: true, Resize: true}, nil
}

// PageCapabilitiesBatch 批量获取页面操作范围
// 入参: indexes 页面索引列表
// 返回: []PageCapabilities 对应页面的操作能力列表, error 索引或读取错误
func (e *Editor) PageCapabilitiesBatch(indexes []int) ([]PageCapabilities, error) {
	return e.PageCapabilitiesBatchContext(context.Background(), indexes)
}

// PageCapabilitiesBatchContext 批量获取页面操作范围并响应取消，默认无DefaultCS时直接批量返回
// 入参: ctx 取消上下文, indexes 页面索引列表
// 返回: []PageCapabilities 对应页面的操作能力列表, error 索引、读取或取消错误
func (e *Editor) PageCapabilitiesBatchContext(ctx context.Context, indexes []int) ([]PageCapabilities, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(indexes) == 0 {
		return nil, nil
	}
	if e.source == nil || e.source.document.CommonData.DefaultCS == 0 {
		capability := PageCapabilities{Insert: true, Copy: true, Delete: true, Move: true, Resize: true}
		result := make([]PageCapabilities, len(indexes))
		for i, index := range indexes {
			if i&4095 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if index < 0 || index >= len(e.pages) {
				return nil, fmt.Errorf("page index %d out of range", index)
			}
			result[i] = capability
		}
		return result, ctx.Err()
	}
	for i, index := range indexes {
		if i&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if index < 0 || index >= len(e.pages) {
			return nil, fmt.Errorf("page index %d out of range", index)
		}
	}
	if e.source != nil && len(indexes) >= 32 {
		refs := make([]Page, 0, len(indexes))
		for _, index := range indexes {
			if len(e.pages[index].pageResources) > 0 {
				continue
			}
			if source := e.source.pages[e.pages[index].ID]; source != nil && source.original == nil {
				refs = append(refs, source.ref)
			}
		}
		if len(refs) >= 32 {
			if err := e.source.reader.preloadPageHeadersContext(ctx, refs); err != nil {
				return nil, err
			}
		}
	}
	result := make([]PageCapabilities, len(indexes))
	for i, index := range indexes {
		capability, err := e.PageCapabilitiesContext(ctx, index)
		if err != nil {
			return nil, err
		}
		result[i] = capability
	}
	return result, ctx.Err()
}

// ObjectCapabilities 获取对象可执行的操作，不支持的内容保持原文，不强制转换
// 字体缺失或子集不包含原文时仍可保真移动、复制和删除，不自动重新排版
// 显式提供可用字体后可通过UpdateObject替换；原文布局未知时不自动推断段落选项
// 入参: page 页面索引, id 对象标识
// 返回: ObjectCapabilities 操作能力, error 错误信息
func (e *Editor) ObjectCapabilities(page int, id string) (ObjectCapabilities, error) {
	layer, index, err := e.findObject(page, id)
	if err != nil {
		return ObjectCapabilities{}, err
	}
	defer e.bindPageIndex(page)()
	return e.objectCapabilities(layer.Objects[index], make(map[*editorXML]bool)), nil
}

// FontData 获取编辑使用的字体数据，外部字体不写入文档资源
// 入参: id 字体资源标识
// 返回: []byte 独立字体数据, error 错误信息
func (e *Editor) FontData(id string) ([]byte, error) {
	if key := editorResourceID(id); key != "" {
		id = key
	}
	for _, resource := range e.resources {
		if resource.font != nil && resource.font.ID == id && len(resource.data) != 0 {
			return bytes.Clone(resource.data), nil
		}
	}
	if e.source != nil {
		if definition, ok := e.sourceFont(id); ok && definition != nil && definition.FontFile != "" {
			return e.source.reader.readFile(e.source.reader.ResPath(definition.FontFile))
		}
	}
	sfnt, err := e.editorFont(id)
	if err != nil {
		return nil, err
	}
	return sfnt.Write(), nil
}

// close 释放复用的解压器资源
func (s *editorMaxIDScanner) close() {
	if s.flate != nil {
		_ = s.flate.Close()
		s.flate = nil
	}
}

// scan 扫描单个包内条目的最大标识，标准UTF-8 XML走字节快扫并自动回退标准解码器
// 入参: reader 来源阅读器, name 包内路径, progress 进度回调，并行工作协程传nil
// 返回: uint64 最大标识, error 读取或解析错误
func (s *editorMaxIDScanner) scan(reader *Reader, name string, progress editorProgress) (uint64, error) {
	clean := cleanPackagePath(name)
	if data, ok := reader.files[clean]; ok {
		if id, fast := scanFastOFDXMLMaxID(data); fast {
			return id, nil
		}
		return editorXMLMaxID(bytes.NewReader(data))
	}
	limit := reader.entryLimit()
	if zf, ok := reader.packageFile(clean); ok && (zf.Method == zip.Deflate || zf.Method == zip.Store) && zf.UncompressedSize64 <= editorMaxIDFastLimit && (limit <= 0 || zf.UncompressedSize64 <= uint64(limit)) {
		if raw, openErr := zf.OpenRaw(); openErr == nil {
			var src io.Reader = raw
			if zf.Method == zip.Deflate {
				if s.flate == nil {
					s.flate = flate.NewReader(raw)
				} else {
					_ = s.flate.(flate.Resetter).Reset(raw, nil)
				}
				src = s.flate
			}
			if s.buf.Cap() > 1<<20 {
				s.buf = bytes.Buffer{}
			} else {
				s.buf.Reset()
			}
			maxRead := int64(editorMaxIDFastLimit)
			if limit > 0 && limit < maxRead {
				maxRead = limit
			}
			n, copyErr := io.Copy(&s.buf, io.LimitReader(src, maxRead+1))
			if copyErr == nil && n <= maxRead {
				data := s.buf.Bytes()
				if id, fast := scanFastOFDXMLMaxID(data); fast {
					return id, nil
				}
				return editorXMLMaxID(bytes.NewReader(data))
			}
			if limit > 0 && n > limit {
				return 0, fmt.Errorf("package entry %s (%d bytes): %w", clean, limit, ErrEntryTooLarge)
			}
		}
	}
	input, err := reader.openFile(clean)
	if err != nil {
		return 0, err
	}
	defer input.Close()
	var stream io.Reader = input
	if src := reader.encryptedSource(); src != nil && src.has(clean) {
		if sz, ok := src.size(clean); ok && sz <= editorMaxIDFastLimit && (limit <= 0 || sz <= uint64(limit)) {
			if s.buf.Cap() > 1<<20 {
				s.buf = bytes.Buffer{}
			} else {
				s.buf.Reset()
			}
			maxRead := int64(editorMaxIDFastLimit)
			if limit > 0 && limit < maxRead {
				maxRead = limit
			}
			n, copyErr := io.Copy(&s.buf, io.LimitReader(input, maxRead+1))
			if copyErr != nil {
				return 0, copyErr
			}
			if n <= maxRead {
				data := s.buf.Bytes()
				if id, fast := scanFastOFDXMLMaxID(data); fast {
					return id, nil
				}
				return editorXMLMaxID(bytes.NewReader(data))
			}
			if limit > 0 && n > limit {
				return 0, fmt.Errorf("package entry %s (%d bytes): %w", clean, limit, ErrEntryTooLarge)
			}
			stream = io.MultiReader(bytes.NewReader(s.buf.Bytes()), input)
		}
	}
	return editorXMLMaxID(&editorProgressReader{Reader: stream, progress: progress, stage: "ids"})
}

// editError 将操作受限原因转为错误，保留可供调用方识别的缺字诊断
// 返回: error 受限原因
func (c ObjectCapabilities) editError() error {
	var err error = errors.New(c.Reason)
	if c.MissingGlyphs != nil {
		err = c.MissingGlyphs
	}
	return &EditError{Code: c.ReasonCode, Err: err}
}

// prepareSourceIDs 首次新增内容前流式核对原包标识，不依赖可能缺失或过期的MaxUnitID
// 返回: error 错误信息
func (e *Editor) prepareSourceIDs() error {
	return e.prepareSourceIDsContext(context.Background())
}

// prepareSourceIDsContext 在可取消的扫描成功后更新最大标识，不保留未完成结果
// 入参: ctx 取消上下文
// 返回: error 扫描或取消错误
func (e *Editor) prepareSourceIDsContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	maximum, err := e.sourceMaxID(editorProgress(nil).withContext(ctx))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.maxID = maximum
	if e.source != nil {
		e.source.idsReady = true
	}
	return nil
}

// sourceMaxID 核对最大标识，不修改文档或缓存，允许导入准备阶段取消
// 入参: progress 进度回调
// 返回: uint64 最大标识, error 错误信息
func (e *Editor) sourceMaxID(progress editorProgress) (uint64, error) {
	if e.source == nil || e.source.idsReady {
		return e.maxID, nil
	}
	reader := e.source.reader
	maximum := e.maxID
	enc := reader.encryptedSource()
	cachedArchiveMax, archiveCached := reader.maxIDCache.get(reader.archiveID, enc)
	if archiveCached {
		maximum = max(maximum, cachedArchiveMax)
		var scanner editorMaxIDScanner
		defer scanner.close()
		for name := range reader.files {
			if err := progress.report("ids", 0, 0); err != nil {
				return 0, err
			}
			if strings.HasSuffix(name, "/") || isNonXMLPackageFile(name) {
				continue
			}
			id, err := scanner.scan(reader, name, progress)
			if err != nil {
				return 0, fmt.Errorf("scan IDs in %s: %w", name, err)
			}
			maximum = max(maximum, id)
		}
		return maximum, nil
	}
	names := make(map[string]bool, len(reader.fileIndex)+len(reader.files))
	for name := range reader.fileIndex {
		names[name] = true
	}
	for name := range reader.files {
		names[name] = true
	}
	if enc != nil {
		for _, name := range enc.names() {
			names[name] = true
		}
	}
	pending := make([]string, 0, len(names))
	for name := range names {
		if strings.HasSuffix(name, "/") || isNonXMLPackageFile(name) {
			continue
		}
		pending = append(pending, name)
	}
	cacheableArchive := reader.archiveID != 0 || enc != nil
	if cacheableArchive {
		for name := range reader.files {
			clean := cleanPackagePath(name)
			if _, inZip := reader.fileIndex[clean]; inZip || enc.has(clean) {
				cacheableArchive = false
				break
			}
		}
	}
	workers := runtime.GOMAXPROCS(0)
	if e.runtimeResources != nil && e.runtimeResources.options.Parallelism > 0 && e.runtimeResources.options.Parallelism < workers {
		workers = e.runtimeResources.options.Parallelism
	}
	archiveMax := uint64(0)
	if workers <= 1 || len(pending) < 32 {
		var scanner editorMaxIDScanner
		defer scanner.close()
		for _, name := range pending {
			if err := progress.report("ids", 0, 0); err != nil {
				return 0, err
			}
			id, err := scanner.scan(reader, name, progress)
			if err != nil {
				return 0, fmt.Errorf("scan IDs in %s: %w", name, err)
			}
			maximum = max(maximum, id)
			if cacheableArchive {
				if _, inFiles := reader.files[cleanPackagePath(name)]; !inFiles {
					archiveMax = max(archiveMax, id)
				}
			}
		}
	} else {
		if err := progress.report("ids", 0, 0); err != nil {
			return 0, err
		}
		if workers > len(pending)/16 {
			workers = max(2, len(pending)/16)
		}
		const chunkSize = 32
		totalChunks := (len(pending) + chunkSize - 1) / chunkSize
		results := make(chan editorMaxIDChunkResult, workers*2)
		var next atomic.Int64
		var canceled atomic.Bool
		var wg sync.WaitGroup
		wg.Add(workers)
		for range workers {
			go func() {
				defer wg.Done()
				var scanner editorMaxIDScanner
				defer scanner.close()
				for {
					if canceled.Load() {
						return
					}
					start := int(next.Add(chunkSize) - chunkSize)
					if start >= len(pending) {
						return
					}
					end := min(start+chunkSize, len(pending))
					var chunkMax, chunkArchMax uint64
					var chunkErr error
					for idx := start; idx < end; idx++ {
						if canceled.Load() {
							return
						}
						name := pending[idx]
						id, err := scanner.scan(reader, name, nil)
						if err != nil {
							chunkErr = fmt.Errorf("scan IDs in %s: %w", name, err)
							break
						}
						chunkMax = max(chunkMax, id)
						if cacheableArchive {
							if _, inFiles := reader.files[cleanPackagePath(name)]; !inFiles {
								chunkArchMax = max(chunkArchMax, id)
							}
						}
					}
					results <- editorMaxIDChunkResult{maxID: chunkMax, archiveMax: chunkArchMax, err: chunkErr}
					if chunkErr != nil {
						return
					}
				}
			}()
		}
		go func() {
			wg.Wait()
			close(results)
		}()
		var firstErr error
		received := 0
		for res := range results {
			received++
			if firstErr == nil {
				if res.err != nil {
					firstErr = res.err
					canceled.Store(true)
				} else {
					maximum = max(maximum, res.maxID)
					archiveMax = max(archiveMax, res.archiveMax)
					if received < totalChunks {
						if err := progress.report("ids", 0, 0); err != nil {
							firstErr = err
							canceled.Store(true)
						}
					}
				}
			}
		}
		if firstErr != nil {
			return 0, firstErr
		}
		if err := progress.report("ids", 0, 0); err != nil {
			return 0, err
		}
	}
	if cacheableArchive {
		if reader.maxIDCache == nil {
			reader.maxIDCache = &archiveMaxIDCache{}
		}
		reader.maxIDCache.put(reader.archiveID, enc, archiveMax)
	}
	return maximum, nil
}

// originalPage 判断索引是否指向原文档页面
// 入参: index 页面索引
// 返回: bool 是否为原始页面
func (e *Editor) originalPage(index int) bool {
	return e.source != nil && index >= 0 && index < len(e.pages) && e.source.pages[e.pages[index].ID] != nil
}

// pageNoLocalRes 基于已加载页或缓存页头判断目标页是否确定无页面级PageRes声明
// 入参: index 页面索引
// 返回: bool 是否确定不存在页面级资源
func (e *Editor) pageNoLocalRes(index int) bool {
	if len(e.pages[index].pageResources) > 0 || len(e.pages[index].PageRes) > 0 {
		return false
	}
	if e.source == nil {
		return true
	}
	source := e.source.pages[e.pages[index].ID]
	if source == nil {
		return true
	}
	if source.original != nil {
		return len(source.original.pageResources) == 0 && len(source.original.PageRes) == 0
	}
	reader := e.source.reader
	name := reader.ResPath(source.ref.BaseLoc)
	if hdr, ok := reader.pageHeaderCache[name]; ok {
		return len(hdr.PageRes) == 0
	}
	fileData, inFiles := reader.files[name]
	if hdr, ok := reader.pageHeaders.get(name, fileData, inFiles); ok {
		return len(hdr.PageRes) == 0
	}
	return false
}

// docDefaultRGB 常数时间判定文档默认颜色空间是否已在文档级解析为8位RGB
// 返回: bool 是否为8位RGB, bool 是否已在文档级确定
func (e *Editor) docDefaultRGB() (bool, bool) {
	if e.source == nil || e.source.document.CommonData.DefaultCS == 0 {
		return true, true
	}
	id := formatUnitID(e.source.document.CommonData.DefaultCS)
	if space, ok := e.source.reader.resolveColorSpaceScoped(nil, id); ok {
		return isDefaultRGBColorSpace(space), true
	}
	return false, false
}

// sourceRGB 判断新建RGB对象能否直接使用文档默认颜色空间
// 返回: bool 是否为8位RGB文档
func (e *Editor) sourceRGB() bool {
	if e.source == nil || e.source.document.CommonData.DefaultCS == 0 {
		return true
	}
	space, _ := e.sourceColorSpace(formatUnitID(e.source.document.CommonData.DefaultCS))
	return isDefaultRGBColorSpace(space)
}

// sourceRGBContext 按当前作用域与文档页级资源判断默认颜色空间是否为8位RGB并响应取消
// 入参: ctx 取消上下文
// 返回: bool 是否为8位RGB文档, error 读取或取消错误
func (e *Editor) sourceRGBContext(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if e.source == nil || e.source.document.CommonData.DefaultCS == 0 {
		return true, nil
	}
	id := formatUnitID(e.source.document.CommonData.DefaultCS)
	if space, ok := e.source.reader.resolveColorSpaceScoped(e.pageScope, id); ok {
		return isDefaultRGBColorSpace(space), nil
	}
	list, err := e.ensureSourcePageResourcesContext(ctx)
	if err != nil {
		return false, err
	}
	for _, parsed := range list {
		if space, ok := parsed.colorSpaces.get(id); ok {
			return isDefaultRGBColorSpace(space), nil
		}
	}
	return false, nil
}

// repairSourceObjectIDs 按实际节点区分重复对象，只修复没有引用歧义的编号
// 入参: ctx 取消上下文, data 页面原文, root 页面节点
// 返回: []byte 编辑副本, bool 是否修复, error 错误信息
func (e *Editor) repairSourceObjectIDs(ctx context.Context, data []byte, root *editorXML) ([]byte, bool, error) {
	seen := make(map[string]bool)
	duplicates := make(map[string]bool)
	var nodes []*editorXML
	var visit func(*editorXML) error
	visit = func(parent *editorXML) error {
		for _, node := range parent.children {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !ofdXMLChild(parent.name, node.name) {
				continue
			}
			id := node.attr("ID")
			if id != "" && seen[id] {
				switch node.name.Local {
				case "TextObject", "PathObject", "ImageObject", "CompositeObject":
					nodes = append(nodes, node)
					duplicates[id] = true
				default:
					return fmt.Errorf("duplicate container ID %q", id)
				}
			}
			seen[id] = true
			if node.name.Local == "PageBlock" {
				if err := visit(node); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if content := root.child("Content"); content != nil {
		for _, layer := range content.children {
			if layer.matchesOFD("Layer") {
				if err := visit(layer); err != nil {
					return nil, false, err
				}
			}
		}
	}
	if len(nodes) == 0 {
		return data, false, nil
	}
	if err := e.checkDuplicateObjectReferences(ctx, duplicates); err != nil {
		return nil, false, err
	}
	if err := e.prepareSourceIDsContext(ctx); err != nil {
		return nil, false, err
	}
	maximum := e.maxID
	var patches []editorXMLPatch
	for _, node := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		maximum++
		if maximum > maxUnitID {
			return nil, false, fmt.Errorf("MaxUnitID %d exceeds uint32 range", maximum)
		}
		encoded, err := editorXMLAttribute(data, node, "ID", formatUnitID(maximum))
		if err != nil {
			return nil, false, err
		}
		patches = append(patches, editorXMLPatch{node.start, node.end, encoded})
	}
	e.maxID = maximum
	return editorPatchXML(data, patches), true, nil
}

// checkDuplicateObjectReferences 检查编号引用的目标歧义
// 入参: ctx 取消上下文, duplicates 重复编号
// 返回: error 引用歧义或读取错误
func (e *Editor) checkDuplicateObjectReferences(ctx context.Context, duplicates map[string]bool) error {
	reader := e.source.reader
	for name := range reader.fileNames {
		if !strings.EqualFold(path.Ext(name), ".xml") {
			continue
		}
		input, err := reader.openFile(name)
		if err != nil {
			return err
		}
		decoder := xml.NewDecoder(input)
		for {
			if err := ctx.Err(); err != nil {
				input.Close()
				return err
			}
			token, readErr := decoder.Token()
			if readErr != nil {
				input.Close()
				if readErr != io.EOF {
					return readErr
				}
				break
			}
			if node, ok := token.(xml.StartElement); ok {
				for _, attr := range node.Attr {
					if editorObjectReference(attr.Name.Local) && duplicates[editorResourceID(attr.Value)] {
						input.Close()
						return fmt.Errorf("ambiguous reference to duplicate object ID %q in %s", attr.Value, name)
					}
				}
			}
		}
	}
	return nil
}

// loadSourcePage 首次访问时解析原始页面，不加载图片或字体二进制数据
// 入参: index 页面索引
// 返回: error 错误信息
func (e *Editor) loadSourcePage(index int) error {
	return e.loadSourcePageContext(context.Background(), index)
}

// loadSourcePageContext 解析原始页面，取消时不提交未完成页面
// 入参: ctx 取消上下文, index 页面索引
// 返回: error 读取、解析或取消错误
func (e *Editor) loadSourcePageContext(ctx context.Context, index int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source := e.source.pages[e.pages[index].ID]
	if source == nil || source.original != nil {
		return nil
	}
	loaded := *source
	source = &loaded
	reader := e.source.reader
	data, err := reader.readFileViewContext(ctx, reader.ResPath(source.ref.BaseLoc))
	if err != nil {
		return err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return err
	}
	page, err := reader.PageContentContext(ctx, source.ref)
	if err != nil {
		return err
	}
	data, repaired, err := e.repairSourceObjectIDs(ctx, data, root)
	if err != nil {
		return err
	}
	if repaired {
		root, err = parseEditorXMLContext(ctx, data)
		if err != nil {
			return err
		}
		var updated PageContent
		if err := xml.Unmarshal(data, &updated); err != nil {
			return err
		}
		page.Content = updated.Content
	}
	e.restorePageTextLayouts(page)
	nodes := make(map[string]*editorXML)
	if content := root.child("Content"); content != nil {
		for _, layer := range content.children {
			if !layer.matchesOFD("Layer") {
				continue
			}
			if err := editorXMLObjects(layer, nodes); err != nil {
				return err
			}
		}
	}
	source.data, source.root, source.nodes = data, root, nodes
	source.repaired = repaired
	origins := make(map[string]*editorObjectOrigin)
	restoreScope := e.bindPageResources(page)
	for i := range page.Content.Layer {
		layer := &page.Content.Layer[i]
		for j, object := range layer.Objects {
			if err := ctx.Err(); err != nil {
				restoreScope()
				return err
			}
			id := editorObjectID(object)
			if node := nodes[id]; node != nil {
				if object.Type == "ImageObject" {
					object.ImageObject.LineWidthSet = node.hasAttr("LineWidth") && object.ImageObject.LineWidth == 0
					object.ImageObject.MiterLimitSet = node.hasAttr("MiterLimit") && object.ImageObject.MiterLimit == 0
					object.ImageObject.DashPatternSet = node.hasAttr("DashPattern") && object.ImageObject.DashPattern == ""
					layer.Objects[j] = object
				}
				origin := &editorObjectOrigin{page: source, data: data, node: node, object: object}
				origins[id] = origin
				if editorXMLTransformable(node) {
					resolved, err := e.resolveEditorStyle(object, layer.DrawParam)
					if err != nil {
						origin.reason = err
					} else {
						layer.Objects[j] = resolved
					}
				}
			}
		}
	}
	restoreScope()
	if err := ctx.Err(); err != nil {
		return err
	}
	maps.Copy(e.source.origins, origins)
	copied := copyEditorPage(*page)
	source.initialLayers = copied.Content.Layer
	source.initialActions = copied.Actions
	source.initialTemplates = copied.Template
	source.initialPageRes = copied.PageRes
	source.original = page
	e.source.pages[source.ref.ID] = source
	e.pages[index] = copied
	return nil
}

// objectCapabilities 检查对象能力，在同次遍历中复用原容器排序检查
// 入参: object 当前对象, orderable 原容器排序能力
// 返回: ObjectCapabilities 操作能力
func (e *Editor) objectCapabilities(object GraphicObject, orderable map[*editorXML]bool) ObjectCapabilities {
	id := editorObjectID(object)
	all := ObjectCapabilities{Update: true, Paint: object.Type == "TextObject" || object.Type == "PathObject", ReplaceFont: object.Type == "TextObject", Reflow: object.Type == "TextObject" && object.TextObject.ReadDirection == 0 && object.TextObject.CharDirection == 0, LayoutKnown: object.Type == "TextObject" && (e.objectOrigin(id) == nil || object.TextObject.layout != nil), Transform: true, Arrange: true, Copy: true, Delete: true, Order: true}
	all.ResetCrop = object.Type == "ImageObject" && object.state.crop != nil
	origin := e.objectOrigin(id)
	if origin == nil {
		all.TextContent = all.Reflow
		all.RewriteText = object.Type == "TextObject"
		all.Stretch = object.Type == "ImageObject"
		all.ReplaceImage = object.Type == "ImageObject"
		all.CropImage = all.ReplaceImage
		if all.ReplaceImage {
			_, err := imageFitBasis(object.ImageObject.CTM)
			all.FitImage = err == nil
		}
		return all
	}
	node := origin.node
	if !editorXMLTransformable(node) {
		return ObjectCapabilities{Reason: "object uses unsupported editing features", ReasonCode: EditUnsupportedObject}
	}
	if origin.reason != nil && editReason(origin.reason) == EditUnsupportedStyle {
		return ObjectCapabilities{Reason: origin.reason.Error(), ReasonCode: EditUnsupportedStyle}
	}
	if !editorXMLContainersSupported(node) {
		return ObjectCapabilities{Reason: "object container uses unsupported editing features", ReasonCode: EditUnsupportedContainer}
	}
	if err := validateEditorGeometry(object); err != nil {
		return ObjectCapabilities{Reason: err.Error(), ReasonCode: EditInvalidObject}
	}
	var checked bool
	if all.Order, checked = orderable[node.parent]; !checked {
		all.Order = editorContainerOrderable(node.parent)
		orderable[node.parent] = all.Order
	}
	all.Ungroup = all.Order && e.compositeUngroupable(node) && object.CompositeObject.ResourceID != ""
	all.Copy = editorXMLCopyable(node) && (origin.reason == nil || object.Type == "PathObject" && editReason(origin.reason) == EditUnsupportedColor)
	if origin.reason != nil {
		all.Update, all.Reflow, all.ReplaceFont, all.Paint = false, false, false, false
		all.Paint = (object.Type == "TextObject" || object.Type == "PathObject") && editReason(origin.reason) == EditUnsupportedColor
		all.Reason, all.ReasonCode = origin.reason.Error(), editReason(origin.reason)
	} else if !editorXMLSupported(node) {
		all.Update, all.Reflow, all.ReplaceFont = false, false, false
		all.Reason, all.ReasonCode = "content can only be edited with its original structure preserved", EditUnsupportedObject
	} else if _, err := e.prepareObject(id, object); err != nil {
		all.Update, all.Reflow, all.Reason = false, false, err.Error()
		all.ReasonCode = editReason(err)
		errors.As(err, &all.MissingGlyphs)
	}
	all.Paint = all.Paint && e.editorPaintable(object)
	all.ReplaceImage = all.Update && object.Type == "ImageObject"
	all.CropImage = all.ReplaceImage
	if all.ReplaceImage {
		_, err := imageFitBasis(object.ImageObject.CTM)
		all.FitImage = err == nil
	}
	if object.Type == "TextObject" {
		all.TextContent = all.Update && object.TextObject.ReadDirection == 0 && object.TextObject.CharDirection == 0 && (all.LayoutKnown || len(object.TextObject.CGTransform) == 0)
		all.RewriteText = all.TextContent
		if !all.RewriteText {
			text := object.TextObject
			all.RewriteText = e.RewriteText(&text, text.Text()) == nil
		}
		all.Arrange = all.Update || e.editorTextMeasurable(object.TextObject)
		if _, err := object.TextObject.TextFrame(); err != nil {
			all.Reflow = false
		}
	}
	all.Stretch = all.Transform && e.objectStretchable(object, make(map[string]bool))
	return all
}

// objectOrigin 获取原对象或其副本的保真来源
// 入参: id 对象标识
// 返回: *editorObjectOrigin 原始对象，无来源时为nil
func (e *Editor) objectOrigin(id string) *editorObjectOrigin {
	if origin := e.origins[id]; origin != nil {
		return origin
	}
	if e.source == nil {
		return nil
	}
	return e.source.origins[id]
}

// setObjectOrigin 保存当前编辑器的对象原文，支持新建文档中的组合对象
// 入参: id 对象标识, origin 原文来源，nil恢复原始来源
func (e *Editor) setObjectOrigin(id string, origin *editorObjectOrigin) {
	if e.origins == nil {
		e.origins = make(map[string]*editorObjectOrigin)
	}
	if origin != nil && origin.page == nil && origin.editor == nil {
		copy := *origin
		copy.editor = e
		copy.document = e.documentIdentity()
		origin = &copy
	}
	e.origins[id] = origin
}

// prepareCopiedObject 保真复制原对象，不重新生成字形或丢弃未修改字段
// 入参: id 新标识, object 对象快照
// 返回: GraphicObject 独立副本, error 错误信息
func (v *editorValidation) prepareCopiedObject(id string, object GraphicObject) (GraphicObject, error) {
	e := v.Editor
	origin := e.snapshotOrigin(object)
	if origin == nil && object.origin != nil {
		references := make(map[string]bool)
		collectObjectReferences(object, references)
		delete(references, "")
		if len(references) != 0 {
			return GraphicObject{}, fmt.Errorf("copying a foreign snapshot requires importing its resources")
		}
	}
	object.origin = nil
	if origin == nil {
		return v.prepareObjectCopy(id, object)
	}
	if !editorXMLTransformable(origin.node) || !editorXMLContainersSupported(origin.node) || !editorXMLCopyable(origin.node) || origin.reason != nil && (object.Type != "PathObject" || editReason(origin.reason) != EditUnsupportedColor) {
		return GraphicObject{}, fmt.Errorf("object cannot be copied without changing its original structure")
	}
	var drawParam string
	for parent := origin.node.parent; parent != nil; parent = parent.parent {
		if parent.name.Local == "Layer" {
			drawParam = parent.attr("DrawParam")
			break
		}
	}
	before, err := e.copiedObjectStyle(origin.object, drawParam)
	if err != nil {
		return GraphicObject{}, err
	}
	if !editorPreservedObject(before, object) {
		if !editorXMLSupported(origin.node) {
			return GraphicObject{}, fmt.Errorf("copy would replace unsupported object content")
		}
		return v.prepareObjectCopy(id, object)
	}
	if err := validateEditorGeometry(object); err != nil {
		return GraphicObject{}, err
	}
	if err := e.validatePreservedAppearance(before, object); err != nil {
		return GraphicObject{}, err
	}
	result := cloneEditorData(object)
	switch result.Type {
	case "TextObject":
		result.TextObject.ID = id
	case "PathObject":
		result.PathObject.ID = id
	case "ImageObject":
		result.ImageObject.ID = id
	case "CompositeObject":
		result.CompositeObject.ID = id
	}
	return result, nil
}

// validatePreservedAppearance 校验保真对象实际修改的颜色、描边和裁剪，不重新解释未修改字段
// 入参: before 原对象, after 新对象
// 返回: error 错误信息
func (e *Editor) validatePreservedAppearance(before, after GraphicObject) error {
	old := []*FillColor{before.TextObject.FillColor, (*FillColor)(before.TextObject.StrokeColor), before.PathObject.FillColor, (*FillColor)(before.PathObject.StrokeColor)}
	next := []*FillColor{after.TextObject.FillColor, (*FillColor)(after.TextObject.StrokeColor), after.PathObject.FillColor, (*FillColor)(after.PathObject.StrokeColor)}
	for i, value := range next {
		if !reflect.DeepEqual(old[i], value) {
			if err := e.editorColor(value); err != nil {
				return err
			}
		}
	}
	a, b := *editorObjectClips(&before), *editorObjectClips(&after)
	if b != nil {
		count := 0
		if a != nil {
			count = len(a.Clip)
		}
		for i, clip := range b.Clip {
			if i >= count {
				if err := e.validateObjectClips(&Clips{Clip: []Clip{clip}}); err != nil {
					return err
				}
				continue
			}
			for j, area := range clip.Area {
				if area.CTM != a.Clip[i].Area[j].CTM && area.CTM != "" {
					if _, err := creationNumbers(area.CTM, 6); err != nil {
						return err
					}
				}
			}
		}
	}
	if after.Type == "PathObject" {
		return validateEditorStroke(after.PathObject)
	}
	return nil
}

// editorFont 按需解析内嵌字体或名称匹配的外部字体，不使用无关回退字体
// 入参: id 字体资源标识
// 返回: FontMetrics 字体度量, error 错误信息
func (e *Editor) editorFont(id string) (FontMetrics, error) {
	if key := editorResourceID(id); key != "" {
		id = key
	}
	var data []byte
	var external *Font
	for _, resource := range e.resources {
		if resource.font != nil && resource.font.ID == id {
			data = resource.data
			if resource.font.FontFile == "" {
				external = resource.font
			}
			break
		}
	}
	cacheKey := id
	if data == nil && external == nil {
		if localFont, ok := e.pageScopeFont(id); ok && localFont != nil {
			cacheKey = id + "\x00" + localFont.FontFile + "\x00" + localFont.FontName
		}
	}
	if metrics := e.fontMetrics[cacheKey]; metrics != nil {
		return metrics, nil
	}
	if e.backends.Fonts == nil {
		return nil, &EditError{Code: EditFontUnavailable, Err: fmt.Errorf("fonts: %w", ErrBackendUnavailable)}
	}
	if data == nil && e.source != nil && external == nil {
		if len(e.pageScope) == 0 && e.source.reader.fontDefinition(id) == nil {
			e.ensureSourcePageResources()
		}
		if e.fontRenderer == nil {
			e.fontRenderer = e.newRenderer(e.source.reader)
		}
		restore := e.fontRenderer.bindParsedResources(e.pageScope)
		resolved, err := e.fontRenderer.ResolveFont(id, true)
		restore()
		if err != nil {
			return nil, &EditError{Code: EditFontUnavailable, Err: err}
		}
		data = resolved.Data
	}
	if data == nil && external != nil {
		reader := &Reader{doc: &Document{}, fontResourcesRead: true, fontCache: map[string]*Font{id: external}}
		resolved, err := e.newRenderer(reader).ResolveFont(id, true)
		if err != nil {
			return nil, &EditError{Code: EditFontUnavailable, Err: err}
		}
		data = resolved.Data
	}
	if data == nil {
		return nil, &EditError{Code: EditFontUnavailable, Err: fmt.Errorf("font %q not found", id)}
	}
	metrics, err := e.backends.Fonts.OpenFont(data)
	if err != nil {
		return nil, &EditError{Code: EditFontUnavailable, Err: err}
	}
	e.fontMetrics[cacheKey] = metrics
	return metrics, nil
}

// editorTextMeasurable 判断原字形能否在不使用回退字体的情况下度量
// 入参: text 原文字对象
// 返回: bool 是否可准确度量
func (e *Editor) editorTextMeasurable(text TextObject) bool {
	sfnt, err := e.editorFont(text.Font)
	if err != nil {
		return false
	}
	transforms := make(map[int]CGTransform, len(text.CGTransform))
	for _, transform := range text.CGTransform {
		transforms[transform.CodePosition] = transform
	}
	offset := 0
	for _, code := range text.TextCode {
		runes := textCodeRunes(code.Value)
		for i := 0; i < len(runes); {
			if transform, ok := transforms[offset+i]; ok {
				ids := parseInts(transform.Glyphs)
				count := max(1, transform.CodeCount)
				if len(ids) == 0 || count > len(runes)-i {
					return false
				}
				for _, id := range ids {
					if id <= 0 || id >= int(sfnt.NumGlyphs()) {
						return false
					}
				}
				i += count
			} else {
				if sfnt.GlyphIndex(runes[i]) == 0 {
					return false
				}
				i++
			}
		}
		offset += len(runes)
	}
	return true
}

// editorImage 按需读取原图片尺寸，不解码完整像素
// 入参: id 图片资源标识
// 返回: image.Point 图片尺寸, error 错误信息
func (e *Editor) editorImage(id string) (image.Point, error) {
	added := slices.ContainsFunc(e.resources, func(resource editorResource) bool {
		return resource.image != nil && sameResourceID(resource.image.ID, id)
	})
	if added || len(e.pageScope) == 0 || e.source == nil {
		if size, ok := resourceValue(e.images, id); ok && size.X > 0 {
			return size, nil
		}
	}
	if e.source == nil {
		return image.Point{}, fmt.Errorf("image resource %q not found", id)
	}
	reader := e.source.reader
	fullPath, ok := e.sourceResPath(id)
	if !ok {
		return image.Point{}, fmt.Errorf("image resource %q not found", id)
	}
	cacheKey := "path:" + fullPath
	if size, ok := e.images[cacheKey]; ok && size.X > 0 {
		return size, nil
	}
	input, err := reader.openFile(fullPath)
	if err != nil {
		return image.Point{}, err
	}
	defer input.Close()
	config, _, err := baseimage.DecodeConfig(input)
	if errors.Is(err, baseimage.ErrTIFFRandomAccess) {
		data, readErr := reader.readFileView(fullPath)
		if readErr != nil {
			return image.Point{}, readErr
		}
		config, _, err = baseimage.DecodeConfigData(data)
	}
	if err != nil {
		return image.Point{}, err
	}
	size := image.Pt(config.Width, config.Height)
	e.images[cacheKey] = size
	if len(e.pageScope) == 0 {
		e.images[id] = size
	}
	return size, nil
}

// validateImageMask 校验蒙版与原图尺寸一致且仅包含不透明黑白像素
// 入参: id 蒙版资源标识, size 原图尺寸
// 返回: error 资源、尺寸或像素错误
func (e *Editor) validateImageMask(id string, size image.Point) error {
	var data []byte
	for _, resource := range e.resources {
		if resource.image != nil && sameResourceID(resource.image.ID, id) {
			data = resource.data
			break
		}
	}
	if data == nil && e.source != nil {
		fullPath, ok := e.sourceResPath(id)
		if !ok {
			return fmt.Errorf("image mask resource %q not found", id)
		}
		var err error
		data, err = e.source.reader.readFileView(fullPath)
		if err != nil {
			return err
		}
	}
	mask, _, err := baseimage.Decode(data)
	if err != nil {
		return err
	}
	return validateImageMask(context.Background(), mask, size)
}

// pageAreaBox 解析目标页面的有效区域框并按标准顺序回退
// 入参: page 页面索引
// 返回: Box 页面区域框, error 页面加载或区域解析错误
func (e *Editor) pageAreaBox(page int) (Box, error) {
	p, err := e.page(page)
	if err != nil {
		return Box{}, err
	}
	area := p.Area
	if e.source != nil {
		area, err = e.source.reader.resolvePageAreaContext(context.Background(), p.Area, p.Template)
		if err != nil {
			return Box{}, err
		}
	}
	return parsePageAreaBox(area)
}

// bindPageResources 为当前编辑操作绑定页面级资源作用域并返回恢复函数
// 入参: page 页面内容
// 返回: func() 恢复原作用域的函数
func (e *Editor) bindPageResources(page *PageContent) func() {
	if page == nil || len(page.pageResources) == 0 {
		return func() {}
	}
	return e.bindParsedScope(page.pageResources)
}

// bindParsedScope 绑定已解析的局部资源列表并返回恢复函数
// 入参: local 局部资源列表
// 返回: func() 恢复原作用域的函数
func (e *Editor) bindParsedScope(local []*parsedRes) func() {
	if len(local) == 0 {
		return func() {}
	}
	prev := e.pageScope
	e.pageScope = local
	return func() { e.pageScope = prev }
}

// bindPageIndex 按页面索引绑定所属页面的局部资源作用域
// 入参: index 页面索引
// 返回: func() 恢复原作用域的函数
func (e *Editor) bindPageIndex(index int) func() {
	restore, _ := e.bindPageIndexContext(context.Background(), index)
	return restore
}

// bindPageIndexContext 按页面索引绑定所属页面的局部资源作用域并响应取消
// 入参: ctx 取消上下文, index 页面索引
// 返回: func() 恢复原作用域的函数, error 取消错误
func (e *Editor) bindPageIndexContext(ctx context.Context, index int) (func(), error) {
	if err := ctx.Err(); err != nil {
		return func() {}, err
	}
	if index < 0 || index >= len(e.pages) {
		return func() {}, nil
	}
	if len(e.pages[index].pageResources) > 0 {
		return e.bindParsedScope(e.pages[index].pageResources), nil
	}
	if e.originalPage(index) {
		if source := e.source.pages[e.pages[index].ID]; source != nil {
			if source.original != nil && len(source.original.pageResources) > 0 {
				return e.bindParsedScope(source.original.pageResources), nil
			}
			reader := e.source.reader
			header, err := reader.readPageHeaderContext(ctx, source.ref)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return func() {}, ctxErr
				}
				return func() {}, nil
			}
			pageRes, resErr := reader.collectPageResourcesContext(ctx, reader.ResPath(source.ref.BaseLoc), &header)
			if resErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return func() {}, ctxErr
				}
				return func() {}, nil
			}
			if len(pageRes) > 0 {
				return e.bindParsedScope(pageRes), nil
			}
		}
	}
	return func() {}, nil
}

// ensureSourcePageResourcesContext 按需收集源文档全部模板页与页面头部的PageRes解析结果
// 入参: ctx 取消上下文
// 返回: []*parsedRes 去重后的页级资源列表, error 读取、解析或取消错误
func (e *Editor) ensureSourcePageResourcesContext(ctx context.Context) ([]*parsedRes, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.source == nil {
		return nil, nil
	}
	if e.source.pageResLoaded {
		return e.source.pageResList, nil
	}
	reader := e.source.reader
	doc := e.source.document
	seen := make(map[string]bool)
	var list []*parsedRes
	appendPage := func(baseLoc string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if baseLoc == "" {
			return nil
		}
		fullPath := reader.ResPath(baseLoc)
		header, err := reader.readPageHeaderContext(ctx, Page{BaseLoc: baseLoc})
		if err != nil {
			return err
		}
		for _, loc := range header.PageRes {
			resFull := resolveResourcePath(fullPath, "", loc)
			if seen[resFull] {
				continue
			}
			parsed, err := reader.parseResFileContext(ctx, resFull)
			if err != nil {
				return err
			}
			seen[resFull] = true
			if parsed != nil {
				list = append(list, parsed)
			}
		}
		return nil
	}
	for _, tpl := range doc.CommonData.TemplatePage {
		if err := appendPage(tpl.BaseLoc); err != nil {
			return nil, err
		}
	}
	for _, page := range doc.Pages.Page {
		if err := appendPage(page.BaseLoc); err != nil {
			return nil, err
		}
	}
	e.source.pageResList = list
	e.source.pageResLoaded = true
	return list, nil
}

// ensureSourcePageResources 收集源文档全部页级资源供同步资源查询回退使用
// 返回: []*parsedRes 页级资源列表
func (e *Editor) ensureSourcePageResources() []*parsedRes {
	ctx := context.Background()
	if e.output != nil && e.output.ctx != nil {
		ctx = e.output.ctx
	}
	list, _ := e.ensureSourcePageResourcesContext(ctx)
	return list
}

// pageScopeFont 在当前页面作用域中查找字体声明
// 入参: id 字体资源标识
// 返回: *Font 字体声明, bool 是否命中局部作用域
func (e *Editor) pageScopeFont(id string) (*Font, bool) {
	for i := len(e.pageScope) - 1; i >= 0; i-- {
		if v, ok := e.pageScope[i].fonts.get(id); ok {
			return v, true
		}
	}
	return nil, false
}

// sourceColorSpace 按页面作用域、文档级缓存及全文档页级资源顺序查找颜色空间
// 入参: id 颜色空间标识
// 返回: *ColorSpace 颜色空间, bool 是否找到
func (e *Editor) sourceColorSpace(id string) (*ColorSpace, bool) {
	if e.source == nil {
		return nil, false
	}
	if v, ok := e.source.reader.resolveColorSpaceScoped(e.pageScope, id); ok {
		return v, true
	}
	for _, parsed := range e.ensureSourcePageResources() {
		if v, ok := parsed.colorSpaces.get(id); ok {
			return v, true
		}
	}
	return nil, false
}

// sourceResPath 按页面作用域、文档级缓存及全文档页级资源顺序查找资源包内路径
// 入参: id 资源标识
// 返回: string 包内路径, bool 是否找到
func (e *Editor) sourceResPath(id string) (string, bool) {
	if e.source == nil {
		return "", false
	}
	reader := e.source.reader
	if rel, ok := reader.resolveResPathScoped(e.pageScope, id); ok {
		return reader.ResPath(rel), true
	}
	for _, parsed := range e.ensureSourcePageResources() {
		if rel, ok := parsed.resPaths.get(id); ok {
			return reader.ResPath(rel), true
		}
	}
	return "", false
}

// sourceMedia 按页面作用域、文档级缓存及全文档页级资源顺序查找多媒体定义
// 入参: id 多媒体标识
// 返回: *MultiMedia 多媒体定义, bool 是否找到
func (e *Editor) sourceMedia(id string) (*MultiMedia, bool) {
	if e.source == nil {
		return nil, false
	}
	if v, ok := e.source.reader.resolveMediaScoped(e.pageScope, id); ok {
		return &v, true
	}
	for _, parsed := range e.ensureSourcePageResources() {
		if v, ok := parsed.medias.get(id); ok {
			return &v, true
		}
	}
	return nil, false
}

// sourceFont 按页面作用域、文档级缓存及全文档页级资源顺序查找字体声明
// 入参: id 字体标识
// 返回: *Font 字体声明, bool 是否找到
func (e *Editor) sourceFont(id string) (*Font, bool) {
	if e.source == nil {
		return nil, false
	}
	if v, ok := e.source.reader.resolveFontScoped(e.pageScope, id); ok {
		return v, true
	}
	for _, parsed := range e.ensureSourcePageResources() {
		if v, ok := parsed.fonts.get(id); ok {
			return v, true
		}
	}
	return nil, false
}

// sourceResourceFileContext 定位源文档中定义指定资源的XML文件路径，响应取消
// 入参: ctx 取消上下文, id 资源标识, kind 资源类型，空字符串匹配任意类型
// 返回: string 资源XML包内路径, bool 是否找到, error 读取或取消错误
func (e *Editor) sourceResourceFileContext(ctx context.Context, id, kind string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if e.source == nil {
		return "", false, nil
	}
	reader := e.source.reader
	for i := len(e.pageScope) - 1; i >= 0; i-- {
		if parsed := e.pageScope[i]; parsed.path != "" && parsedHasResource(parsed, id, kind) {
			return parsed.path, true, nil
		}
	}
	if name, ok := reader.resourceFileByKind(kind, id); ok && name != "" {
		if kind != "Image" && kind != "Audio" && kind != "Video" {
			return name, true, nil
		}
		if parsed := reader.resFileCache[name]; parsed == nil || parsedHasResource(parsed, id, kind) {
			return name, true, nil
		}
	}
	list, err := e.ensureSourcePageResourcesContext(ctx)
	if err != nil {
		return "", false, err
	}
	for _, parsed := range list {
		if parsed.path != "" && parsedHasResource(parsed, id, kind) {
			return parsed.path, true, nil
		}
	}
	return "", false, nil
}

// sourceResourceFile 定位源文档中定义指定资源的XML文件路径
// 入参: id 资源标识, kind 资源类型，空字符串匹配任意类型
// 返回: string 资源XML包内路径, bool 是否找到
func (e *Editor) sourceResourceFile(id, kind string) (string, bool) {
	ctx := context.Background()
	if e.output != nil && e.output.ctx != nil {
		ctx = e.output.ctx
	}
	name, ok, _ := e.sourceResourceFileContext(ctx, id, kind)
	return name, ok
}
