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
	"encoding/xml"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// editorOutlineChildren 获取标准目录子节点
// 入参: node 父节点
// 返回: []*editorXML 子节点
func editorOutlineChildren(node *editorXML) []*editorXML {
	var children []*editorXML
	for _, child := range node.children {
		if child.matchesOFD("OutlineElem") {
			children = append(children, child)
		}
	}
	return children
}

// editorOutlineNode 按层级路径定位目录
// 入参: root 目录根节点, path 层级索引
// 返回: *editorXML 节点, error 错误信息
func editorOutlineNode(root *editorXML, path []int) (*editorXML, error) {
	for _, index := range path {
		children := editorOutlineChildren(root)
		if index < 0 || index >= len(children) {
			return nil, fmt.Errorf("outline index %d out of range", index)
		}
		root = children[index]
	}
	return root, nil
}

// Outlines 获取当前目录层级，目标页码从1开始，无目标时为0
// 返回: []OutlineInfo 目录信息, error 错误信息
func (e *Editor) Outlines() ([]OutlineInfo, error) {
	data, err := e.outlineXML()
	if err != nil {
		return nil, err
	}
	var doc Document
	if err := xml.Unmarshal(data, &doc.Outlines); err != nil {
		return nil, err
	}
	if e.source != nil {
		doc.Bookmarks = e.source.document.Bookmarks
	}
	for _, page := range e.pages {
		doc.Pages.Page = append(doc.Pages.Page, Page{ID: page.ID})
	}
	return doc.OutlineInfos(), nil
}

// AddOutline 在指定父目录末尾添加节点，空路径表示根目录
// 入参: parent 从0开始的层级索引, title 标题, page 目标页面索引，-1表示无目标
// 返回: []int 新节点路径, error 错误信息
func (e *Editor) AddOutline(parent []int, title string, page int) ([]int, error) {
	if err := e.validateOutline(title, page); err != nil {
		return nil, err
	}
	data, err := e.outlineXML()
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	node, err := editorOutlineNode(root, parent)
	if err != nil {
		return nil, err
	}
	attrs := ofdAttrs{}
	attrs.add("Title", title)
	item, err := editorXMLContainer("OutlineElem", attrs, e.outlineAction(page))
	if err != nil {
		return nil, err
	}
	item, err = editorXMLGenerated(item, node.name.Space)
	if err != nil {
		return nil, err
	}
	var content []byte
	if node.open != node.end {
		content = bytes.Clone(data[node.open:node.close])
	}
	content = append(content, item...)
	result := append(slices.Clone(parent), len(editorOutlineChildren(node)))
	err = e.setOutlineXML(editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, node, content)}))
	return result, err
}

// UpdateOutline 修改目录标题和目标，目标未改变时保留原跳转坐标及动作
// 入参: path 从0开始的层级索引, title 标题, page 目标页面索引，-1表示无目标
// 返回: error 错误信息
func (e *Editor) UpdateOutline(path []int, title string, page int) error {
	if err := e.validateOutline(title, page); err != nil {
		return err
	}
	data, err := e.outlineXML()
	if err != nil {
		return err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return err
	}
	node, err := editorOutlineNode(root, path)
	if err != nil {
		return err
	}
	if len(path) == 0 {
		return fmt.Errorf("outline path must not be empty")
	}
	infos, err := e.Outlines()
	if err != nil {
		return err
	}
	var info OutlineInfo
	for _, index := range path {
		info = infos[index]
		infos = info.Children
	}
	var patches []editorXMLPatch
	if info.Page != page+1 {
		actions := node.child("Actions")
		if actions != nil {
			var removals []editorXMLPatch
			for _, action := range actions.children {
				if !action.matchesOFD("Action") {
					continue
				}
				if action.child("Goto") != nil {
					removals = append(removals, editorXMLPatch{action.start - actions.open, action.end - actions.open, nil})
				}
			}
			var content []byte
			if actions.open != actions.end {
				content = editorPatchXML(data[actions.open:actions.close], removals)
			}
			added, err := editorXMLGenerated(e.outlineAction(page), actions.name.Space)
			if err != nil {
				return err
			}
			if len(added) > 0 {
				addRoot, err := parseEditorXML(added)
				if err != nil {
					return err
				}
				action := addRoot.children[0]
				standalone, err := editorXMLStandalone(added[action.start:action.end], action)
				if err != nil {
					return err
				}
				content = append(content, standalone...)
			}
			if page < 0 && len(removals) == len(actions.children) {
				patches = append(patches, editorXMLPatch{actions.start, actions.end, nil})
			} else {
				patches = append(patches, editorXMLContent(data, actions, content))
			}
		} else if page >= 0 {
			content, err := editorXMLGenerated(e.outlineAction(page), node.name.Space)
			if err != nil {
				return err
			}
			if node.open != node.end {
				content = append(content, data[node.open:node.close]...)
			}
			patches = append(patches, editorXMLContent(data, node, content))
		}
	}
	updated := editorPatchXML(data, patches)
	root, err = parseEditorXML(updated)
	if err != nil {
		return err
	}
	node, err = editorOutlineNode(root, path)
	if err != nil {
		return err
	}
	patch, err := editorXMLAttributePatch(updated, node, "Title", title)
	if err != nil {
		return err
	}
	return e.setOutlineXML(editorPatchXML(updated, []editorXMLPatch{patch}))
}

// SetOutlineActions 替换目录节点的动作序列，空列表清除动作，不改变子目录
// 入参: path 从0开始的层级索引, actions 按激活顺序执行的动作
// 返回: error 路径、动作或提交错误
func (e *Editor) SetOutlineActions(path []int, actions []Action) error {
	if len(path) == 0 {
		return fmt.Errorf("outline path must not be empty")
	}
	if err := validateActions(actions); err != nil {
		return err
	}
	data, err := e.outlineXML()
	if err != nil {
		return err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return err
	}
	node, err := editorOutlineNode(root, path)
	if err != nil {
		return err
	}
	encoded, err := editorActionsXML(actions)
	if err != nil {
		return err
	}
	ctx := node
	if existing := node.child("Actions"); existing != nil {
		ctx = existing
	}
	encoded, err = editorXMLGenerated(encoded, ctx.name.Space)
	if err != nil {
		return err
	}
	var patch editorXMLPatch
	if ctx != node {
		patch = editorXMLPatch{ctx.start, ctx.end, encoded}
	} else {
		if node.open != node.end {
			encoded = append(encoded, data[node.open:node.close]...)
		}
		patch = editorXMLContent(data, node, encoded)
	}
	return e.setOutlineXML(editorPatchXML(data, []editorXMLPatch{patch}))
}

// DeleteOutline 删除目录节点及其子节点
// 入参: path 从0开始的层级索引
// 返回: error 错误信息
func (e *Editor) DeleteOutline(path []int) error {
	if len(path) == 0 {
		return fmt.Errorf("outline path must not be empty")
	}
	data, err := e.outlineXML()
	if err != nil {
		return err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return err
	}
	node, err := editorOutlineNode(root, path)
	if err != nil {
		return err
	}
	return e.setOutlineXML(editorPatchXML(data, []editorXMLPatch{{node.start, node.end, nil}}))
}

// MoveOutline 移动目录及其子树，保留原始动作和扩展字段，一次撤销恢复
// 入参: path 节点路径, parent 移动前的目标父路径，空路径表示根, index 移除源节点后在目标父节点中的插入索引
// 返回: []int 移动后的路径, error 错误信息
func (e *Editor) MoveOutline(path, parent []int, index int) ([]int, error) {
	if len(path) == 0 || len(parent) >= len(path) && slices.Equal(path, parent[:len(path)]) {
		return nil, fmt.Errorf("cannot move an outline into itself")
	}
	data, err := e.outlineXML()
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	node, err := editorOutlineNode(root, path)
	if err != nil {
		return nil, err
	}
	target, err := editorOutlineNode(root, parent)
	if err != nil {
		return nil, err
	}
	children := editorOutlineChildren(target)
	same := slices.Equal(path[:len(path)-1], parent)
	if same {
		children = slices.Delete(children, path[len(path)-1], path[len(path)-1]+1)
	}
	if index < 0 || index > len(children) {
		return nil, fmt.Errorf("outline insertion index %d out of range", index)
	}
	result := slices.Clone(parent)
	if len(result) >= len(path) && slices.Equal(path[:len(path)-1], result[:len(path)-1]) && result[len(path)-1] > path[len(path)-1] {
		result[len(path)-1]--
	}
	result = append(result, index)
	if same && path[len(path)-1] == index {
		return result, nil
	}
	value, err := editorXMLStandalone(data[node.start:node.end], node)
	if err != nil {
		return nil, err
	}
	patches := []editorXMLPatch{{node.start, node.end, nil}}
	if target.open == target.end {
		patches = append(patches, editorXMLContent(data, target, value))
	} else {
		at := target.close
		if index < len(children) {
			at = children[index].start
		}
		patches = append(patches, editorXMLPatch{at, at, value})
	}
	return result, e.setOutlineXML(editorPatchXML(data, patches))
}

// validateOutline 校验目录标题和目标页面
// 入参: title 标题, page 页面索引
// 返回: error 错误信息
func (e *Editor) validateOutline(title string, page int) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("outline title must not be empty")
	}
	if page < -1 || page >= len(e.pages) {
		return fmt.Errorf("page index %d out of range", page)
	}
	return nil
}

// outlineXML 获取保留命名空间的目录原文
// 返回: []byte XML内容, error 错误信息
func (e *Editor) outlineXML() ([]byte, error) {
	if e.outlines != nil {
		return e.outlines, nil
	}
	if e.source != nil {
		reader := e.source.reader
		data, err := reader.readFile(reader.documentRoot)
		if err != nil {
			return nil, err
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		if node := root.child("Outlines"); node != nil {
			return editorXMLStandalone(data[node.start:node.end], node)
		}
		return root.containerXML("Outlines", nil, nil)
	}
	return editorXMLContainer("Outlines", nil, nil)
}

// outlineAction 编码页面适配跳转，负数表示无目标
// 入参: page 页面索引
// 返回: []byte 动作集合
func (e *Editor) outlineAction(page int) []byte {
	if page < 0 {
		return nil
	}
	data, _ := encodeOFDXML(func(x *ofdXML) {
		x.root("Actions", nil)
		x.start("Action", ofdAttrs{{Name: xml.Name{Local: "Event"}, Value: "CLICK"}})
		x.start("Goto", nil)
		x.start("Dest", ofdAttrs{{Name: xml.Name{Local: "Type"}, Value: "Fit"}, {Name: xml.Name{Local: "PageID"}, Value: e.pages[page].ID}})
		x.end("Dest")
		x.end("Goto")
		x.end("Action")
		x.end("Actions")
	})
	return bytes.TrimPrefix(data, []byte(xml.Header))
}

// setOutlineXML 更新目录及可选叶节点计数，保存一次撤销记录
// 入参: data 新目录原文
// 返回: error 错误信息
func (e *Editor) setOutlineXML(data []byte) error {
	root, err := parseEditorXML(data)
	if err != nil {
		return err
	}
	var patches []editorXMLPatch
	var walk func(*editorXML) (int, error)
	walk = func(node *editorXML) (int, error) {
		count := 0
		for _, child := range node.children {
			if !child.matchesOFD("OutlineElem") {
				continue
			}
			leaves, err := walk(child)
			if err != nil {
				return 0, err
			}
			count += leaves
		}
		if value := strconv.Itoa(count); node.attr("Count") != "" && node.attr("Count") != value {
			patch, err := editorXMLAttributePatch(data, node, "Count", value)
			if err != nil {
				return 0, err
			}
			patches = append(patches, patch)
		}
		return max(1, count), nil
	}
	if _, err := walk(root); err != nil {
		return err
	}
	if len(patches) > 0 {
		data = editorPatchXML(data, patches)
	}
	current, err := e.outlineXML()
	if err != nil {
		return err
	}
	if bytes.Equal(current, data) {
		return nil
	}
	before := e.outlines
	e.outlines = data
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { e.outlines = before }
		change.redo = func(e *Editor) { e.outlines = data }
	}
	return nil
}

// withOutlines 将修改后的目录写入文档，清除指向已删除页面的新增跳转
// 入参: data 文档原文
// 返回: []byte 更新文档, error 解析或取消错误
func (e *Editor) withOutlines(data []byte) ([]byte, error) {
	if e.outlines == nil {
		return data, nil
	}
	ctx := context.Background()
	if e.output != nil {
		ctx = e.output.ctx
	}
	outline, err := parseEditorXMLContext(ctx, e.outlines)
	if err != nil {
		return nil, err
	}
	valid := make(map[string]bool, len(e.pages))
	for _, page := range e.pages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if id := editorResourceID(page.ID); id != "" {
			valid[id] = true
		}
	}
	var removals []editorXMLPatch
	var walk func(*editorXML) error
	walk = func(node *editorXML) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if actions := node.child("Actions"); actions != nil {
			start := len(removals)
			for _, action := range actions.children {
				if err := ctx.Err(); err != nil {
					return err
				}
				if !action.matchesOFD("Action") {
					continue
				}
				if target := action.child("Goto"); target != nil {
					if dest := target.child("Dest"); dest != nil {
						if !valid[editorResourceID(dest.attr("PageID"))] {
							removals = append(removals, editorXMLPatch{action.start, action.end, nil})
						}
					}
				}
			}
			if len(removals) > start && len(removals)-start == len(actions.children) {
				removals = append(removals[:start], editorXMLPatch{actions.start, actions.end, nil})
			}
		}
		for _, child := range editorOutlineChildren(node) {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(outline); err != nil {
		return nil, err
	}
	value := e.outlines
	if len(outline.children) == 0 {
		value = nil
	} else if len(removals) > 0 {
		value = editorPatchXML(value, removals)
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return nil, err
	}
	var patch editorXMLPatch
	if node := root.child("Outlines"); node != nil {
		patch = editorXMLPatch{node.start, node.end, value}
	} else if len(value) > 0 {
		pages := root.child("Pages")
		if pages == nil {
			return nil, fmt.Errorf("document has no Pages")
		}
		patch = editorXMLPatch{pages.end, pages.end, value}
	} else {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return data, nil
	}
	data = editorPatchXML(data, []editorXMLPatch{patch})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}
