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
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
)

// SignatureStampCapabilities 返回签章外观的编辑能力，不以签名有效性限制编辑
// 共享历史签名无法独立改写时保留原入口，避免改变其他版本
// 入参: page 页面索引, path 签名及外观标识
// 返回: ObjectCapabilities 移动缩放及删除能力, error 外观或读取错误
func (e *Editor) SignatureStampCapabilities(page int, path ObjectPath) (ObjectCapabilities, error) {
	if _, err := e.signatureStamp(page, path); err != nil {
		return ObjectCapabilities{}, err
	}
	reader, err := e.reader(nil)
	if err != nil {
		return ObjectCapabilities{}, err
	}
	defer reader.Close()
	doc, err := reader.Doc()
	if err != nil {
		return ObjectCapabilities{}, err
	}
	if err := reader.signatureStampVersionDependency(context.Background(), reader.ResPath(doc.Signatures)); err != nil {
		if !errors.Is(err, ErrSignatureDependency) {
			return ObjectCapabilities{}, err
		}
		return ObjectCapabilities{Reason: err.Error(), ReasonCode: EditUnsupportedObject}, nil
	}
	return ObjectCapabilities{Transform: true, Delete: true, Stretch: true}, nil
}

// SignaturePageStamps 生成所选页面相同毫米坐标的印章位置并检查页内范围
// 入参: pages 从1开始且不重复的页码, box 完整印章区域
// 返回: []SignatureStamp 可传入签署选项的位置, error 错误信息
func (r *Reader) SignaturePageStamps(pages []int, box Box) ([]SignatureStamp, error) {
	if r == nil || len(pages) == 0 || !signatureWriteBoxValid(box) {
		return nil, fmt.Errorf("invalid page stamp parameters")
	}
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	seen := make(map[int]bool)
	stamps := make([]SignatureStamp, 0, len(pages))
	for _, page := range pages {
		if page < 1 || page > len(doc.Pages.Page) || seen[page] {
			return nil, fmt.Errorf("invalid stamp page: %d", page)
		}
		seen[page] = true
		stamps = append(stamps, SignatureStamp{PageRef: doc.Pages.Page[page-1].ID, Boundary: signatureWriteBox(box)})
	}
	return signatureWriteStamps(r, stamps, true)
}

// SignatureSeamStamps 生成页面右侧的等宽骑缝印章位置
// 页码从1开始，按传入顺序分割同一印章；box.X为右侧内缩，Y为顶部偏移，W/H为完整印章尺寸
// 入参: pages 至少两页且不重复, box 印章参数，单位毫米
// 返回: []SignatureStamp 可传入签署选项的位置, error 错误信息
func (r *Reader) SignatureSeamStamps(pages []int, box Box) ([]SignatureStamp, error) {
	if r == nil || len(pages) < 2 || !signatureWriteBoxValid(box) || box.X < 0 || box.Y < 0 {
		return nil, fmt.Errorf("invalid seam stamp parameters")
	}
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	seen := make(map[int]bool)
	stamps := make([]SignatureStamp, 0, len(pages))
	width := box.W / float64(len(pages))
	for i, page := range pages {
		if page < 1 || page > len(doc.Pages.Page) || seen[page] {
			return nil, fmt.Errorf("invalid seam stamp page: %d", page)
		}
		seen[page] = true
		area, err := r.PageArea(doc.Pages.Page[page-1])
		if err != nil {
			return nil, err
		}
		physical, err := parsePageAreaBox(area)
		if err != nil {
			return nil, err
		}
		clip := Box{X: float64(i) * width, W: width, H: box.H}
		boundary := Box{X: physical.X + physical.W - box.X - width - clip.X, Y: physical.Y + box.Y, W: box.W, H: box.H}
		stamps = append(stamps, SignatureStamp{PageRef: doc.Pages.Page[page-1].ID, Boundary: signatureWriteBox(boundary), Clip: signatureWriteBox(clip)})
	}
	return signatureWriteStamps(r, stamps, true)
}

// signatureStampEdit 保存同一签名描述中的外观修改，原签名值及扩展原样保留
type signatureStampEdit struct {
	data    []byte
	root    *editorXML
	patches []editorXMLPatch
}

// signatureStampVersionDependency 判断文档级签名是否被其他历史入口共用，不改写版本语义
// 入参: ctx 取消上下文, name 签名列表路径
// 返回: error 历史依赖或读取错误
func (r *Reader) signatureStampVersionDependency(ctx context.Context, name string) error {
	if r.versionInfo != nil && r.OFD.DocBody[r.documentIndex].DocRoot != "" {
		return fmt.Errorf("%w: signature appearance is shared with the main document", ErrSignatureDependency)
	}
	versions, err := r.DocumentVersionsContext(ctx)
	if err != nil {
		return err
	}
	for _, version := range versions {
		if r.versionInfo != nil && version.ID == r.versionInfo.ID {
			continue
		}
		for _, file := range version.Files {
			if file.Location == name {
				return fmt.Errorf("%w: version %s shares signature appearances", ErrSignatureDependency, version.ID)
			}
		}
	}
	return nil
}

// signatureStampChanges 隔离其他签名及文档共用的描述文件和列表，不移动签名值与印章数据
// 入参: ctx 取消上下文, name 当前签名列表, changes 按签名标识保存的外观修改
// 返回: map[string][]byte 当前入口的独立修改, error 引用或XML错误
func (r *Reader) signatureStampChanges(ctx context.Context, name string, changes map[string][]byte) (map[string][]byte, error) {
	if err := r.signatureStampVersionDependency(ctx, name); err != nil {
		return nil, err
	}
	var indexes []int
	for index := range r.DocumentCount() {
		if index != r.documentIndex {
			indexes = append(indexes, index)
		}
	}
	shared, err := r.documentFiles(ctx, indexes...)
	if err != nil {
		return nil, err
	}
	data, err := r.readFileViewContext(ctx, name)
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return nil, err
	}
	references := make(map[string]int)
	for _, node := range root.children {
		if node.matchesOFD("Signature") {
			references[signatureRefPath(name, node.attr("BaseLoc"))]++
		}
	}
	parts := make(map[string][]byte)
	var patches []editorXMLPatch
	for _, node := range root.children {
		if !node.matchesOFD("Signature") {
			continue
		}
		changed, ok := changes[node.attr("ID")]
		if !ok {
			continue
		}
		source := signatureRefPath(name, node.attr("BaseLoc"))
		target := source
		if shared[source] || references[source] > 1 {
			target = packageAvailableName(r, parts, source)
		}
		parts[target] = changed
		if target == source {
			continue
		}
		patch, err := editorXMLAttributePatch(data, node, "BaseLoc", "/"+target)
		if err != nil {
			return nil, err
		}
		patches = append(patches, patch)
	}
	if len(patches) == 0 {
		return parts, nil
	}
	target := name
	if shared[name] {
		target = packageAvailableName(r, parts, name)
		entry, err := r.readFileViewContext(ctx, "OFD.xml")
		if err != nil {
			return nil, err
		}
		ofd, err := parseEditorXMLContext(ctx, entry)
		if err != nil {
			return nil, err
		}
		node := ofd.childAt("DocBody", r.documentIndex).child("Signatures")
		parts["OFD.xml"] = editorPatchXML(entry, []editorXMLPatch{editorXMLContent(entry, node, editorFontXMLText("/"+target))})
	}
	parts[target] = editorPatchXML(data, patches)
	return parts, nil
}

// signatureStamp 读取当前页的指定签章外观，不借用其他文档或版本的缓存
// 入参: page 页面索引, path 签名及外观标识
// 返回: Stamp 当前外观, error 页面、签名或读取错误
func (e *Editor) signatureStamp(page int, path ObjectPath) (Stamp, error) {
	reader, err := e.reader(nil)
	if err != nil {
		return Stamp{}, err
	}
	defer reader.Close()
	doc, err := reader.Doc()
	if err != nil {
		return Stamp{}, err
	}
	if page < 0 || page >= len(doc.Pages.Page) {
		return Stamp{}, fmt.Errorf("page index %d out of range", page)
	}
	stamps, err := reader.PageStamps(doc.Pages.Page[page].ID)
	if err != nil {
		return Stamp{}, err
	}
	for _, stamp := range stamps {
		if stamp.ID == path.ID && stamp.SignatureID == path.Signature {
			return stamp, nil
		}
	}
	return Stamp{}, fmt.Errorf("signature appearance not found: %s/%s", path.Signature, path.ID)
}

// changeSignatureStamps 原子修改或移除签章外观，保留签名值，由验签反映修改后的真实状态
// 入参: page 页面索引, paths 签章路径, matrix 平移或正向缩放，nil删除外观
// 返回: error 路径、变换、读取或提交错误
func (e *Editor) changeSignatureStamps(page int, paths []ObjectPath, matrix *Matrix) error {
	if matrix != nil && (matrix.b != 0 || matrix.c != 0 || matrix.a <= 0 || matrix.d <= 0) {
		return fmt.Errorf("signature appearances support translation and positive scaling")
	}
	ctx := context.Background()
	reader, err := e.reader(nil)
	if err != nil {
		return err
	}
	defer reader.Close()
	doc, err := reader.Doc()
	if err != nil {
		return err
	}
	if page < 0 || page >= len(doc.Pages.Page) {
		return fmt.Errorf("page index %d out of range", page)
	}
	listName := reader.ResPath(doc.Signatures)
	data, err := reader.readFile(listName)
	if err != nil {
		return err
	}
	var list Signatures
	if err := xml.Unmarshal(data, &list); err != nil {
		return err
	}
	files := make(map[string]*signatureStampEdit)
	for _, path := range paths {
		index := slices.IndexFunc(list.List, func(signature Signature) bool { return signature.ID == path.Signature })
		if index < 0 {
			return fmt.Errorf("signature not found: %s", path.Signature)
		}
		name := signatureRefPath(listName, list.List[index].BaseLoc)
		file := files[path.Signature]
		if file == nil {
			data, err := reader.readFile(name)
			if err != nil {
				return err
			}
			root, err := parseEditorXML(data)
			if err != nil {
				return err
			}
			file = &signatureStampEdit{data: data, root: root}
			files[path.Signature] = file
		}
		info := file.root.child("SignedInfo")
		if info == nil {
			return fmt.Errorf("missing SignedInfo")
		}
		var target *editorXML
		for _, node := range info.children {
			if node.matchesOFD("StampAnnot") && node.attr("ID") == path.ID && sameResourceID(node.attr("PageRef"), doc.Pages.Page[page].ID) {
				target = node
				break
			}
		}
		if target == nil {
			return fmt.Errorf("signature appearance not found: %s/%s", path.Signature, path.ID)
		}
		if matrix == nil {
			file.patches = append(file.patches, editorXMLPatch{start: target.start, end: target.end})
			continue
		}
		box, err := parseSignatureStampBox(target.attr("Boundary"))
		if err != nil {
			return err
		}
		box = matrix.TransformBox(box)
		if !signatureWriteBoxValid(box) {
			return fmt.Errorf("invalid signature appearance boundary")
		}
		if *matrix == IdentityMatrix {
			continue
		}
		patch, err := editorXMLAttributePatch(file.data, target, "Boundary", signatureWriteBox(box))
		if err != nil {
			return err
		}
		if value := target.attr("Clip"); value != "" {
			clip, err := parseSignatureStampBox(value)
			if err != nil {
				return err
			}
			clip = Box{X: clip.X * matrix.a, Y: clip.Y * matrix.d, W: clip.W * matrix.a, H: clip.H * matrix.d}
			header := *target
			header.start, header.open, header.end = 0, len(patch.data), -1
			if target.open == target.end {
				header.end = header.open
			}
			clipPatch, err := editorXMLAttributePatch(patch.data, &header, "Clip", signatureWriteBox(clip))
			if err != nil {
				return err
			}
			patch.data = clipPatch.data
		}
		file.patches = append(file.patches, patch)
	}
	changes := make(map[string][]byte)
	for id, file := range files {
		if len(file.patches) != 0 {
			changes[id] = editorPatchXML(file.data, file.patches)
		}
	}
	if len(changes) == 0 {
		return nil
	}
	changes, err = reader.signatureStampChanges(ctx, listName, changes)
	if err != nil {
		return err
	}
	scan := &editorFontDocument{reader: reader, index: reader.DocumentIndex()}
	if err := scan.apply(ctx, changes); err != nil {
		return err
	}
	return e.commitResourceSnapshot(ctx, reader)
}

// signatureWriteStamps 复用Reader定位并检查外观几何参数
// 入参: r 阅读器, stamps 位置, seal 是否电子印章
// 返回: []SignatureStamp 独立位置列表, error 错误信息
func signatureWriteStamps(r *Reader, stamps []SignatureStamp, seal bool) ([]SignatureStamp, error) {
	positions, err := r.SignatureStampPositions(stamps)
	if err != nil {
		return nil, err
	}
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	result := append([]SignatureStamp(nil), stamps...)
	if len(result) != 0 {
		pages := make(map[string]bool)
		for _, page := range doc.Pages.Page {
			id := editorResourceID(page.ID)
			if id == "" || pages[id] {
				return nil, fmt.Errorf("ambiguous stamp page ID: %s", page.ID)
			}
			pages[id] = true
		}
	}
	ids := make(map[string]bool)
	for _, stamp := range stamps {
		if stamp.ID == "" {
			continue
		}
		if !ofdXMLIDValid(stamp.ID) || ids[stamp.ID] {
			return nil, fmt.Errorf("invalid or duplicate stamp ID: %s", stamp.ID)
		}
		ids[stamp.ID] = true
	}
	for _, position := range positions {
		box := position.Box
		if !signatureWriteBoxValid(box) {
			return nil, fmt.Errorf("invalid stamp boundary")
		}
		visible := box
		if clip := position.ClipBox; clip != nil {
			if !signatureWriteBoxValid(*clip) || clip.X < 0 || clip.Y < 0 || clip.X+clip.W > box.W+1e-9 || clip.Y+clip.H > box.H+1e-9 {
				return nil, fmt.Errorf("invalid stamp clip")
			}
			visible = Box{X: box.X + clip.X, Y: box.Y + clip.Y, W: clip.W, H: clip.H}
		}
		area, err := r.PageArea(doc.Pages.Page[position.Page-1])
		if err != nil {
			return nil, err
		}
		page, err := parsePageAreaBox(area)
		if err != nil {
			return nil, err
		}
		if visible.X < page.X-1e-9 || visible.Y < page.Y-1e-9 || visible.X+visible.W > page.X+page.W+1e-9 || visible.Y+visible.H > page.Y+page.H+1e-9 {
			return nil, fmt.Errorf("stamp is outside page %d", position.Page)
		}
	}
	return result, nil
}

// signatureWriteBoxValid 判断印章区域是否有限且尺寸为正
// 入参: box 区域
// 返回: bool 是否有效
func signatureWriteBoxValid(box Box) bool {
	for _, value := range []float64{box.X, box.Y, box.W, box.H} {
		if math.IsInf(value, 0) || math.IsNaN(value) {
			return false
		}
	}
	return box.W > 0 && box.H > 0
}

// signatureWriteBox 编码印章区域
// 入参: box 区域
// 返回: string 区域文本
func signatureWriteBox(box Box) string {
	return strconv.FormatFloat(box.X, 'f', -1, 64) + " " + strconv.FormatFloat(box.Y, 'f', -1, 64) + " " + strconv.FormatFloat(box.W, 'f', -1, 64) + " " + strconv.FormatFloat(box.H, 'f', -1, 64)
}
