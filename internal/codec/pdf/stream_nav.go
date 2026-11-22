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

package pdf

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/xiaoqidun/pdfgo"
)

// NavLink 描述单页链接注解的矩形、可选多边形点击区与动作序列
type NavLink struct {
	Rect    pdfgo.Rectangle
	Region  *pdfgo.LinkRegion
	Actions []pdfgo.NavigationAction
}

// NavOutline 描述文档大纲的先序节点
type NavOutline struct {
	Title   string
	Level   int
	Actions []pdfgo.NavigationAction
}

// NavWriter 管理附件、音频、屏幕呈现、链接注解与大纲树的原生PDF对象写出与共享去重
type NavWriter struct {
	writer      *StreamWriter
	files       map[*pdfgo.FileSpecification]pdfgo.Reference
	clips       map[*pdfgo.MediaClip]pdfgo.Reference
	renditions  map[*pdfgo.Rendition]pdfgo.Reference
	screens     map[*pdfgo.ScreenAnnotation]pdfgo.Reference
	emptyForm   pdfgo.Reference
	attachments map[string]pdfgo.Reference
}

// NewNavWriter 创建原生PDF导航写出器
// 入参: writer 流式PDF写出器
// 返回: *NavWriter 导航写出器
func NewNavWriter(writer *StreamWriter) *NavWriter {
	return &NavWriter{
		writer:      writer,
		files:       make(map[*pdfgo.FileSpecification]pdfgo.Reference),
		clips:       make(map[*pdfgo.MediaClip]pdfgo.Reference),
		renditions:  make(map[*pdfgo.Rendition]pdfgo.Reference),
		screens:     make(map[*pdfgo.ScreenAnnotation]pdfgo.Reference),
		attachments: make(map[string]pdfgo.Reference),
	}
}

// destinationArray 将PDF跳转目标转换为标准显式目标数组
// 入参: dest 跳转目标
// 返回: pdfgo.Array 目标数组
func destinationArray(dest pdfgo.Destination) pdfgo.Array {
	mode := dest.Mode
	if mode == "" {
		mode = "Fit"
	}
	arr := make(pdfgo.Array, 0, 2+len(dest.Parameters))
	arr = append(arr, dest.Page, mode)
	arr = append(arr, dest.Parameters...)
	return arr
}

// rectArray 将点空间矩形转换为四元素PDF数组
// 入参: r 矩形范围
// 返回: pdfgo.Array 四元素坐标数组
func rectArray(r pdfgo.Rectangle) pdfgo.Array {
	return pdfgo.Array{pdfgo.Real(r.XMin), pdfgo.Real(r.YMin), pdfgo.Real(r.XMax), pdfgo.Real(r.YMax)}
}

// asciiFallback 将含非ASCII字符的文件名转为兼容旧版阅读器的F字段ASCII文本
// 入参: s 原始UTF-8文件名
// 返回: string ASCII文件名
func asciiFallback(s string) string {
	out := make([]byte, 0, len(s))
	for i := range len(s) {
		if s[i] >= 0x20 && s[i] <= 0x7E {
			out = append(out, s[i])
		} else {
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "file"
	}
	return string(out)
}

// encodeURIString 将URI中的非ASCII或控制字符按RFC 3986百分号转义为ASCII
// 入参: raw 原始URI文本
// 返回: string ASCII转义后URI
func encodeURIString(raw string) string {
	const hex = "0123456789ABCDEF"
	out := make([]byte, 0, len(raw))
	for i := range len(raw) {
		c := raw[i]
		if c >= 0x21 && c <= 0x7E {
			out = append(out, c)
		} else {
			out = append(out, '%', hex[c>>4], hex[c&0x0F])
		}
	}
	return string(out)
}

// WriteAttachments 写出文档内嵌附件并返回EmbeddedFiles名称树字典
// 入参: ctx 取消上下文, attachments 附件映射
// 返回: pdfgo.Dictionary 名称树节点字典, error 写入错误
func (n *NavWriter) WriteAttachments(ctx context.Context, attachments map[string]*pdfgo.EmbeddedFile) (pdfgo.Dictionary, error) {
	if len(attachments) == 0 {
		return nil, nil
	}
	keys := slices.Sorted(maps.Keys(attachments))
	names := make(pdfgo.Array, 0, len(keys)*2)
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file := attachments[key]
		if file == nil {
			continue
		}
		efRef := n.writer.AllocRef()
		efDict := pdfgo.Dictionary{
			"Type": pdfgo.Name("EmbeddedFile"),
			"Params": pdfgo.Dictionary{
				"Size": pdfgo.Integer(len(file.Data)),
			},
		}
		if file.MediaType != "" {
			efDict["Subtype"] = pdfgo.Name(file.MediaType)
		}
		if err := n.writer.WriteStream(efRef, efDict, file.Data, true); err != nil {
			return nil, err
		}
		uf, err := pdfgo.EncodeTextString(file.Name)
		if err != nil {
			return nil, err
		}
		fsDict := pdfgo.Dictionary{
			"Type": pdfgo.Name("Filespec"),
			"F":    pdfgo.String(asciiFallback(file.Name)),
			"UF":   uf,
			"EF": pdfgo.Dictionary{
				"F":  efRef,
				"UF": efRef,
			},
		}
		if file.Description != "" {
			desc, err := pdfgo.EncodeTextString(file.Description)
			if err != nil {
				return nil, err
			}
			fsDict["Desc"] = desc
		}
		fsRef := n.writer.AllocRef()
		if err := n.writer.WriteObject(fsRef, fsDict); err != nil {
			return nil, err
		}
		n.attachments[key] = fsRef
		names = append(names, pdfgo.String(key), fsRef)
	}
	if len(names) == 0 {
		return nil, nil
	}
	return pdfgo.Dictionary{"Names": names}, nil
}

// ReserveScreenAnnotations 为全部屏幕注解预分配间接对象引用，供跨页呈现动作引用
// 入参: screensByPage 按页索引分组的屏幕注解列表
func (n *NavWriter) ReserveScreenAnnotations(screensByPage map[int][]*pdfgo.ScreenAnnotation) {
	for _, list := range screensByPage {
		for _, screen := range list {
			if screen == nil {
				continue
			}
			if _, exists := n.screens[screen]; !exists {
				n.screens[screen] = n.writer.AllocRef()
			}
		}
	}
}

// WriteScreenAnnotations 写出单页的全部屏幕注解并返回注解引用列表
// 入参: ctx 取消上下文, pageRef 所属页面引用, screens 屏幕注解列表
// 返回: pdfgo.Array 注解引用数组, error 写入错误
func (n *NavWriter) WriteScreenAnnotations(ctx context.Context, pageRef pdfgo.Reference, screens []*pdfgo.ScreenAnnotation) (pdfgo.Array, error) {
	if len(screens) == 0 {
		return nil, nil
	}
	refs := make(pdfgo.Array, 0, len(screens))
	for _, screen := range screens {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if screen == nil {
			continue
		}
		ref, ok := n.screens[screen]
		if !ok {
			ref = n.writer.AllocRef()
			n.screens[screen] = ref
		}
		dict := pdfgo.Dictionary{
			"Type":    pdfgo.Name("Annot"),
			"Subtype": pdfgo.Name("Screen"),
			"P":       pageRef,
			"Rect":    rectArray(screen.Rect),
		}
		if screen.Title != "" {
			title, err := pdfgo.EncodeTextString(screen.Title)
			if err != nil {
				return nil, err
			}
			dict["T"] = title
		}
		if screen.Hidden {
			dict["F"] = pdfgo.Integer(2)
		}
		apRef, err := n.ensureEmptyForm()
		if err != nil {
			return nil, err
		}
		dict["AP"] = pdfgo.Dictionary{"N": apRef}
		if len(screen.Actions) > 0 {
			act, err := n.buildActionChain(ctx, screen.Actions)
			if err != nil {
				return nil, err
			}
			if act != nil {
				dict["A"] = act
			}
		}
		if err := n.writer.WriteObject(ref, dict); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// WritePageLinks 写出单页全部链接注解并返回注解引用列表
// 入参: ctx 取消上下文, links 链接定义列表
// 返回: pdfgo.Array 链接注解引用数组, error 写入错误
func (n *NavWriter) WritePageLinks(ctx context.Context, links []NavLink) (pdfgo.Array, error) {
	if len(links) == 0 {
		return nil, nil
	}
	refs := make(pdfgo.Array, 0, len(links))
	for _, link := range links {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rect := link.Rect
		if link.Region != nil {
			rect = link.Region.Rect
		}
		dict := pdfgo.Dictionary{
			"Type":    pdfgo.Name("Annot"),
			"Subtype": pdfgo.Name("Link"),
			"Border":  pdfgo.Array{pdfgo.Integer(0), pdfgo.Integer(0), pdfgo.Integer(0)},
			"Rect":    rectArray(rect),
		}
		if link.Region != nil && len(link.Region.Quads) > 0 {
			quads := make(pdfgo.Array, 0, len(link.Region.Quads)*8)
			for _, q := range link.Region.Quads {
				for _, pt := range q {
					quads = append(quads, pdfgo.Real(pt.X), pdfgo.Real(pt.Y))
				}
			}
			dict["QuadPoints"] = quads
		}
		if len(link.Actions) == 1 && link.Actions[0].Destination != nil {
			dict["Dest"] = destinationArray(*link.Actions[0].Destination)
		} else if len(link.Actions) > 0 {
			act, err := n.buildActionChain(ctx, link.Actions)
			if err != nil {
				return nil, err
			}
			if act != nil {
				dict["A"] = act
			}
		}
		ref := n.writer.AllocRef()
		if err := n.writer.WriteObject(ref, dict); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// WriteOutlines 按先序层级构建并写出标准PDF大纲树，返回Outlines根引用
// 入参: ctx 取消上下文, items 先序大纲节点列表
// 返回: pdfgo.Reference 大纲根引用, bool 是否存在有效大纲, error 写入错误
func (n *NavWriter) WriteOutlines(ctx context.Context, items []NavOutline) (pdfgo.Reference, bool, error) {
	if len(items) == 0 {
		return pdfgo.Reference{}, false, nil
	}
	type nodeInfo struct {
		ref    pdfgo.Reference
		parent int
		prev   int
		next   int
		first  int
		last   int
		count  int
		valid  bool
	}
	rootRef := n.writer.AllocRef()
	nodes := make([]nodeInfo, len(items))
	for i := range nodes {
		nodes[i] = nodeInfo{parent: -1, prev: -1, next: -1, first: -1, last: -1}
	}
	firstTop, lastTop, topCount := -1, -1, 0
	var stack []int
	for i, item := range items {
		level := item.Level
		if level < 0 {
			level = 0
		}
		for len(stack) > 0 && (level == 0 || level <= items[stack[len(stack)-1]].Level) {
			child := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if len(stack) > 0 {
				nodes[stack[len(stack)-1]].count += nodes[child].count
			}
		}
		nodes[i].ref = n.writer.AllocRef()
		nodes[i].valid = true
		if len(stack) == 0 {
			nodes[i].prev = lastTop
			if lastTop != -1 {
				nodes[lastTop].next = i
			} else {
				firstTop = i
			}
			lastTop = i
			topCount++
			stack = append(stack, i)
			continue
		}
		parent := stack[len(stack)-1]
		nodes[i].parent = parent
		if nodes[parent].first == -1 {
			nodes[parent].first = i
		} else {
			prev := nodes[parent].last
			nodes[i].prev = prev
			nodes[prev].next = i
		}
		nodes[parent].last = i
		nodes[parent].count++
		topCount++
		stack = append(stack, i)
	}
	for len(stack) > 1 {
		child := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		nodes[stack[len(stack)-1]].count += nodes[child].count
	}
	if firstTop == -1 {
		return pdfgo.Reference{}, false, nil
	}
	for i, item := range items {
		if err := ctx.Err(); err != nil {
			return pdfgo.Reference{}, false, err
		}
		if !nodes[i].valid {
			continue
		}
		title, err := pdfgo.EncodeTextString(item.Title)
		if err != nil {
			return pdfgo.Reference{}, false, err
		}
		dict := pdfgo.Dictionary{"Title": title}
		if nodes[i].parent == -1 {
			dict["Parent"] = rootRef
		} else {
			dict["Parent"] = nodes[nodes[i].parent].ref
		}
		if nodes[i].prev != -1 {
			dict["Prev"] = nodes[nodes[i].prev].ref
		}
		if nodes[i].next != -1 {
			dict["Next"] = nodes[nodes[i].next].ref
		}
		if nodes[i].first != -1 {
			dict["First"] = nodes[nodes[i].first].ref
		}
		if nodes[i].last != -1 {
			dict["Last"] = nodes[nodes[i].last].ref
		}
		if nodes[i].count != 0 {
			dict["Count"] = pdfgo.Integer(nodes[i].count)
		}
		if len(item.Actions) == 1 && item.Actions[0].Destination != nil {
			dict["Dest"] = destinationArray(*item.Actions[0].Destination)
		} else if len(item.Actions) > 0 {
			act, err := n.buildActionChain(ctx, item.Actions)
			if err != nil {
				return pdfgo.Reference{}, false, err
			}
			if act != nil {
				dict["A"] = act
			}
		}
		if err := n.writer.WriteObject(nodes[i].ref, dict); err != nil {
			return pdfgo.Reference{}, false, err
		}
	}
	rootDict := pdfgo.Dictionary{
		"Type":  pdfgo.Name("Outlines"),
		"First": nodes[firstTop].ref,
		"Last":  nodes[lastTop].ref,
		"Count": pdfgo.Integer(topCount),
	}
	if err := n.writer.WriteObject(rootRef, rootDict); err != nil {
		return pdfgo.Reference{}, false, err
	}
	return rootRef, true, nil
}

// BuildActionChain 将导航动作序列编码为标准PDF动作字典或Next链表
// 入参: ctx 取消上下文, actions 导航动作列表
// 返回: pdfgo.Dictionary 首动作字典, error 编码或写入错误
func (n *NavWriter) BuildActionChain(ctx context.Context, actions []pdfgo.NavigationAction) (pdfgo.Dictionary, error) {
	return n.buildActionChain(ctx, actions)
}

// buildActionChain 逐项编码动作并通过Next键按序串联
// 入参: ctx 取消上下文, actions 导航动作列表
// 返回: pdfgo.Dictionary 链首动作字典, error 编码或写入错误
func (n *NavWriter) buildActionChain(ctx context.Context, actions []pdfgo.NavigationAction) (pdfgo.Dictionary, error) {
	var first, prev pdfgo.Dictionary
	for _, action := range actions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dict, err := n.buildSingleAction(ctx, action)
		if err != nil {
			return nil, err
		}
		if dict == nil {
			continue
		}
		if first == nil {
			first = dict
		} else {
			prev["Next"] = dict
		}
		prev = dict
	}
	return first, nil
}

// buildSingleAction 构造单个导航动作对应的PDF字典，按需写出共享音频或媒体对象
// 入参: ctx 取消上下文, action 单个导航动作
// 返回: pdfgo.Dictionary 动作字典, error 编码或写入错误
func (n *NavWriter) buildSingleAction(ctx context.Context, action pdfgo.NavigationAction) (pdfgo.Dictionary, error) {
	if action.Destination != nil {
		return pdfgo.Dictionary{
			"Type": pdfgo.Name("Action"),
			"S":    pdfgo.Name("GoTo"),
			"D":    destinationArray(*action.Destination),
		}, nil
	}
	if action.URI != "" {
		return pdfgo.Dictionary{
			"Type": pdfgo.Name("Action"),
			"S":    pdfgo.Name("URI"),
			"URI":  pdfgo.String(encodeURIString(action.URI)),
		}, nil
	}
	if action.Attachment != nil {
		dict := pdfgo.Dictionary{
			"Type": pdfgo.Name("Action"),
			"S":    pdfgo.Name("GoToE"),
			"T": pdfgo.Dictionary{
				"R": pdfgo.Name("C"),
				"N": pdfgo.String(action.Attachment.Key),
			},
		}
		if action.Attachment.NewWindow != nil {
			dict["NewWindow"] = pdfgo.Boolean(*action.Attachment.NewWindow)
		}
		return dict, nil
	}
	if action.Sound != nil {
		soundRef, err := n.writeSound(ctx, action.Sound.Sound)
		if err != nil {
			return nil, err
		}
		dict := pdfgo.Dictionary{
			"Type":  pdfgo.Name("Action"),
			"S":     pdfgo.Name("Sound"),
			"Sound": soundRef,
		}
		if action.Sound.Volume != 1 {
			dict["Volume"] = pdfgo.Real(action.Sound.Volume)
		}
		if action.Sound.Repeat {
			dict["Repeat"] = pdfgo.Boolean(true)
		} else if action.Sound.Synchronous {
			dict["Synchronous"] = pdfgo.Boolean(true)
		}
		if action.Sound.Mix {
			dict["Mix"] = pdfgo.Boolean(true)
		}
		return dict, nil
	}
	if action.Rendition != nil {
		anRef := action.Rendition.Annotation
		if action.Rendition.Target != nil {
			ref, ok := n.screens[action.Rendition.Target]
			if !ok {
				return nil, fmt.Errorf("missing PDF screen annotation reference")
			}
			anRef = ref
		}
		dict := pdfgo.Dictionary{
			"Type": pdfgo.Name("Action"),
			"S":    pdfgo.Name("Rendition"),
			"OP":   pdfgo.Integer(action.Rendition.Operation),
			"AN":   anRef,
		}
		if action.Rendition.Rendition != nil {
			rRef, err := n.writeRendition(ctx, action.Rendition.Rendition)
			if err != nil {
				return nil, err
			}
			dict["R"] = rRef
		}
		return dict, nil
	}
	return nil, nil
}

// writeSound 写出自描述音频流对象并返回对象引用
// 入参: ctx 取消上下文, sound 音频定义
// 返回: pdfgo.Reference 音频流引用, error 写入错误
func (n *NavWriter) writeSound(ctx context.Context, sound pdfgo.Sound) (pdfgo.Reference, error) {
	if sound.File != nil {
		fsRef, err := n.writeFileSpec(ctx, sound.File)
		if err != nil {
			return pdfgo.Reference{}, err
		}
		ref := n.writer.AllocRef()
		dict := pdfgo.Dictionary{
			"Type": pdfgo.Name("Sound"),
			"F":    fsRef,
		}
		if err := n.writer.WriteStream(ref, dict, nil, false); err != nil {
			return pdfgo.Reference{}, err
		}
		return ref, nil
	}
	ref := n.writer.AllocRef()
	dict := pdfgo.Dictionary{"Type": pdfgo.Name("Sound")}
	if sound.Rate > 0 {
		dict["R"] = pdfgo.Real(sound.Rate)
	}
	if sound.Channels > 0 {
		dict["C"] = pdfgo.Integer(sound.Channels)
	}
	if sound.Bits > 0 {
		dict["B"] = pdfgo.Integer(sound.Bits)
	}
	if sound.Encoding != "" {
		dict["E"] = sound.Encoding
	}
	var data []byte
	if sound.Stream != nil {
		data = sound.Stream.Data
	}
	if err := n.writer.WriteStream(ref, dict, data, true); err != nil {
		return pdfgo.Reference{}, err
	}
	return ref, nil
}

// writeRendition 按指针去重写出媒体呈现对象及其引用片段
// 入参: ctx 取消上下文, r 媒体呈现定义
// 返回: pdfgo.Reference 呈现对象引用, error 写入错误
func (n *NavWriter) writeRendition(ctx context.Context, r *pdfgo.Rendition) (pdfgo.Reference, error) {
	if ref, ok := n.renditions[r]; ok {
		return ref, nil
	}
	subtype := r.Subtype
	if subtype == "" {
		subtype = "MR"
	}
	dict := pdfgo.Dictionary{"S": subtype}
	if r.Name != "" {
		name, err := pdfgo.EncodeTextString(r.Name)
		if err != nil {
			return pdfgo.Reference{}, err
		}
		dict["N"] = name
	}
	if r.Clip != nil {
		clipRef, err := n.writeMediaClip(ctx, r.Clip)
		if err != nil {
			return pdfgo.Reference{}, err
		}
		dict["C"] = clipRef
	}
	if len(r.Play) > 0 {
		dict["P"] = r.Play
	}
	if len(r.Screen) > 0 {
		dict["SP"] = r.Screen
	}
	if len(r.MustHonor) > 0 {
		dict["MH"] = r.MustHonor
	}
	if len(r.BestEffort) > 0 {
		dict["BE"] = r.BestEffort
	}
	ref := n.writer.AllocRef()
	if err := n.writer.WriteObject(ref, dict); err != nil {
		return pdfgo.Reference{}, err
	}
	n.renditions[r] = ref
	return ref, nil
}

// writeMediaClip 按指针去重写出媒体片段对象及其内嵌文件说明
// 入参: ctx 取消上下文, clip 媒体片段定义
// 返回: pdfgo.Reference 片段对象引用, error 写入错误
func (n *NavWriter) writeMediaClip(ctx context.Context, clip *pdfgo.MediaClip) (pdfgo.Reference, error) {
	if ref, ok := n.clips[clip]; ok {
		return ref, nil
	}
	subtype := clip.Subtype
	if subtype == "" {
		subtype = "MCD"
	}
	dict := pdfgo.Dictionary{
		"Type": pdfgo.Name("MediaClip"),
		"S":    subtype,
	}
	if clip.Name != "" {
		name, err := pdfgo.EncodeTextString(clip.Name)
		if err != nil {
			return pdfgo.Reference{}, err
		}
		dict["N"] = name
	}
	if clip.ContentType != "" {
		dict["CT"] = pdfgo.String(clip.ContentType)
	}
	if clip.File != nil {
		fsRef, err := n.writeFileSpec(ctx, clip.File)
		if err != nil {
			return pdfgo.Reference{}, err
		}
		dict["D"] = fsRef
	}
	if len(clip.Permissions) > 0 {
		dict["P"] = clip.Permissions
	}
	if len(clip.MustHonor) > 0 {
		dict["MH"] = clip.MustHonor
	}
	if len(clip.BestEffort) > 0 {
		dict["BE"] = clip.BestEffort
	}
	ref := n.writer.AllocRef()
	if err := n.writer.WriteObject(ref, dict); err != nil {
		return pdfgo.Reference{}, err
	}
	n.clips[clip] = ref
	return ref, nil
}

// writeFileSpec 按指针去重写出文件说明对象及其内嵌媒体流
// 入参: ctx 取消上下文, file 文件说明
// 返回: pdfgo.Reference 文件说明引用, error 写入错误
func (n *NavWriter) writeFileSpec(ctx context.Context, file *pdfgo.FileSpecification) (pdfgo.Reference, error) {
	if ref, ok := n.files[file]; ok {
		return ref, nil
	}
	if err := ctx.Err(); err != nil {
		return pdfgo.Reference{}, err
	}
	uf, err := pdfgo.EncodeTextString(file.Name)
	if err != nil {
		return pdfgo.Reference{}, err
	}
	dict := pdfgo.Dictionary{
		"Type": pdfgo.Name("Filespec"),
		"F":    pdfgo.String(asciiFallback(file.Name)),
		"UF":   uf,
	}
	if file.FileSystem != "" {
		dict["FS"] = file.FileSystem
	}
	if file.Description != "" {
		desc, err := pdfgo.EncodeTextString(file.Description)
		if err != nil {
			return pdfgo.Reference{}, err
		}
		dict["Desc"] = desc
	}
	if file.Embedded != nil {
		efRef := n.writer.AllocRef()
		efDict := pdfgo.Dictionary{
			"Type": pdfgo.Name("EmbeddedFile"),
			"Params": pdfgo.Dictionary{
				"Size": pdfgo.Integer(len(file.Embedded.Data)),
			},
		}
		if err := n.writer.WriteStream(efRef, efDict, file.Embedded.Data, true); err != nil {
			return pdfgo.Reference{}, err
		}
		dict["EF"] = pdfgo.Dictionary{"F": efRef, "UF": efRef}
	}
	ref := n.writer.AllocRef()
	if err := n.writer.WriteObject(ref, dict); err != nil {
		return pdfgo.Reference{}, err
	}
	n.files[file] = ref
	return ref, nil
}

// ensureEmptyForm 惰性写出共享空白外观Form供隐藏屏幕注解引用
// 返回: pdfgo.Reference 空白外观流引用, error 写入错误
func (n *NavWriter) ensureEmptyForm() (pdfgo.Reference, error) {
	if n.emptyForm.Number != 0 {
		return n.emptyForm, nil
	}
	ref := n.writer.AllocRef()
	dict := pdfgo.Dictionary{
		"Type":     pdfgo.Name("XObject"),
		"Subtype":  pdfgo.Name("Form"),
		"FormType": pdfgo.Integer(1),
		"BBox":     pdfgo.Array{pdfgo.Integer(0), pdfgo.Integer(0), pdfgo.Integer(0), pdfgo.Integer(0)},
	}
	if err := n.writer.WriteStream(ref, dict, nil, false); err != nil {
		return pdfgo.Reference{}, err
	}
	n.emptyForm = ref
	return ref, nil
}
