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
	"fmt"
	"net/url"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/pdf"
)

// pdfPage PDF页面数据
type pdfPage struct {
	Content *PageContent
	Box     Box
}

// pdfNavigation PDF导航信息
type pdfNavigation struct {
	Anchor  map[int][]pdfAnchor
	Link    map[int][]pdfLink
	Outline map[int][]pdfOutline
	nextID  int
}

// pdfAnchor PDF跳转目标
type pdfAnchor struct {
	Name string
	Rect canvas.Rect
}

// pdfLink PDF链接
type pdfLink struct {
	URI  string
	Rect canvas.Rect
}

// pdfOutline PDF大纲
type pdfOutline struct {
	Name  string
	Level int
	Y     float64
}

// pdfActionSource PDF动作来源
type pdfActionSource struct {
	Box     Box
	Actions []Action
}

// newPDFNavigation 创建PDF导航信息
// 入参: renderer 渲染器, doc 文档结构, pages 页面数据
// 返回: *pdfNavigation PDF导航信息
func newPDFNavigation(renderer *Renderer, doc *Document, pages []pdfPage) *pdfNavigation {
	navigation := &pdfNavigation{
		Anchor:  make(map[int][]pdfAnchor),
		Link:    make(map[int][]pdfLink),
		Outline: make(map[int][]pdfOutline),
	}
	pageIndex := make(map[string]int, len(pages))
	for i, page := range pages {
		pageIndex[page.Content.ID] = i
	}
	bookmarks := make(map[string]Dest)
	if doc != nil {
		for _, bookmark := range doc.Bookmarks.Bookmark {
			bookmarks[bookmark.Name] = bookmark.Dest
		}
	}
	for i, page := range pages {
		sources := pageActionSources(page)
		if renderer.RenderAnnotations {
			sources = append(sources, annotationActionSources(renderer.Reader.Annots[page.Content.ID])...)
		}
		for _, source := range sources {
			rect := pdfSourceRect(source.Box, page.Box.H)
			for _, action := range source.Actions {
				if action.Event != "CLICK" {
					continue
				}
				navigation.addAction(i, rect, action, bookmarks, pageIndex, pages)
			}
		}
	}
	if doc != nil {
		navigation.addOutlines(doc.Outlines.OutlineElem, 0, bookmarks, pageIndex, pages)
	}
	return navigation
}

// addAction 添加PDF动作
// 入参: page 页面索引, rect 动作区域, action 动作, bookmarks 书签, pageIndex 页面索引表, pages 页面数据
func (n *pdfNavigation) addAction(page int, rect canvas.Rect, action Action, bookmarks map[string]Dest, pageIndex map[string]int, pages []pdfPage) {
	if action.Goto != nil {
		dest := gotoDest(action.Goto, bookmarks)
		if dest == nil {
			return
		}
		target, ok := pageIndex[dest.PageID]
		if !ok {
			return
		}
		targetRect, ok := pdfDestRect(*dest, pages[target].Box.H)
		if !ok {
			return
		}
		name := fmt.Sprintf("ofdgo-dest-%d", n.nextID)
		n.nextID++
		n.Anchor[target] = append(n.Anchor[target], pdfAnchor{Name: name, Rect: targetRect})
		n.Link[page] = append(n.Link[page], pdfLink{URI: "#" + name, Rect: rect})
		return
	}
	if action.URI != nil && action.URI.URI != "" {
		n.Link[page] = append(n.Link[page], pdfLink{URI: resolveActionURI(*action.URI), Rect: rect})
	}
}

// addOutlines 添加PDF大纲
// 入参: outlines 大纲节点, level 节点层级, bookmarks 书签, pageIndex 页面索引表, pages 页面数据
func (n *pdfNavigation) addOutlines(outlines []OutlineElem, level int, bookmarks map[string]Dest, pageIndex map[string]int, pages []pdfPage) {
	for _, outline := range outlines {
		if dest := outlineDest(outline, bookmarks); dest != nil {
			if page, ok := pageIndex[dest.PageID]; ok {
				n.Outline[page] = append(n.Outline[page], pdfOutline{
					Name:  outline.Title,
					Level: level,
					Y:     pdfDestY(*dest, pages[page].Box.H),
				})
			}
		}
		n.addOutlines(outline.OutlineElem, level+1, bookmarks, pageIndex, pages)
	}
}

// apply 应用PDF导航信息
// 入参: renderer PDF渲染器, page 页面索引
func (n *pdfNavigation) apply(renderer *pdf.PDF, page int) {
	for _, anchor := range n.Anchor[page] {
		renderer.AddAnchor(anchor.Name, anchor.Rect)
	}
	for _, link := range n.Link[page] {
		renderer.AddLink(link.URI, link.Rect)
	}
	for _, outline := range n.Outline[page] {
		renderer.AddOutline(outline.Name, outline.Level, outline.Y)
	}
}

// pageActionSources 获取页面动作来源
// 入参: page 页面数据
// 返回: []pdfActionSource 动作来源
func pageActionSources(page pdfPage) []pdfActionSource {
	sources := make([]pdfActionSource, 0)
	if len(page.Content.Actions) > 0 {
		sources = append(sources, pdfActionSource{
			Box:     Box{W: page.Box.W, H: page.Box.H},
			Actions: page.Content.Actions,
		})
	}
	for _, layer := range page.Content.Content.Layer {
		for _, object := range layer.Objects {
			sources = appendGraphicActionSources(sources, object, nil)
		}
	}
	return sources
}

// annotationActionSources 获取注释动作来源
// 入参: annotations 页面注释
// 返回: []pdfActionSource 动作来源
func annotationActionSources(annotations []Annotation) []pdfActionSource {
	sources := make([]pdfActionSource, 0)
	for _, annotation := range annotations {
		box, err := ParseBox(annotation.Appearance.Boundary)
		if err != nil {
			continue
		}
		for _, object := range annotation.Appearance.Objects {
			sources = appendGraphicActionSources(sources, object, &box)
		}
	}
	return sources
}

// appendGraphicActionSources 添加图形对象动作来源
// 入参: sources 动作来源, object 图形对象, box 指定动作区域
// 返回: []pdfActionSource 动作来源
func appendGraphicActionSources(sources []pdfActionSource, object GraphicObject, box *Box) []pdfActionSource {
	var boundary string
	var actions []Action
	var children []GraphicObject
	switch object.Type {
	case "TextObject":
		boundary = object.TextObject.Boundary
		actions = object.TextObject.Actions
	case "PathObject":
		boundary = object.PathObject.Boundary
		actions = object.PathObject.Actions
	case "ImageObject":
		boundary = object.ImageObject.Boundary
		actions = object.ImageObject.Actions
	case "CompositeGraphicUnit", "CompositeObject":
		boundary = object.CompositeGraphicUnit.Boundary
		actions = object.CompositeGraphicUnit.Actions
		children = object.CompositeGraphicUnit.Objects
	}
	sourceBox := box
	if sourceBox == nil && boundary != "" {
		if value, err := ParseBox(boundary); err == nil {
			sourceBox = &value
		}
	}
	if sourceBox != nil && len(actions) > 0 {
		sources = append(sources, pdfActionSource{Box: *sourceBox, Actions: actions})
	}
	for _, child := range children {
		sources = appendGraphicActionSources(sources, child, box)
	}
	return sources
}

// gotoDest 获取文档内跳转目标
// 入参: action 跳转动作, bookmarks 书签
// 返回: *Dest 跳转目标
func gotoDest(action *Goto, bookmarks map[string]Dest) *Dest {
	if action.Dest != nil {
		return action.Dest
	}
	if action.Bookmark != nil {
		if dest, ok := bookmarks[action.Bookmark.Name]; ok {
			return &dest
		}
	}
	return nil
}

// outlineDest 获取大纲跳转目标
// 入参: outline 大纲节点, bookmarks 书签
// 返回: *Dest 跳转目标
func outlineDest(outline OutlineElem, bookmarks map[string]Dest) *Dest {
	for _, action := range outline.Actions {
		if action.Goto != nil {
			if dest := gotoDest(action.Goto, bookmarks); dest != nil {
				return dest
			}
		}
	}
	for _, child := range outline.OutlineElem {
		if dest := outlineDest(child, bookmarks); dest != nil {
			return dest
		}
	}
	return nil
}

// resolveActionURI 解析URI动作地址
// 入参: action URI动作
// 返回: string URI地址
func resolveActionURI(action URI) string {
	if action.Base == "" {
		return action.URI
	}
	base, err := url.Parse(action.Base)
	if err != nil {
		return action.URI
	}
	target, err := url.Parse(action.URI)
	if err != nil {
		return action.URI
	}
	return base.ResolveReference(target).String()
}

// pdfSourceRect 转换PDF动作区域
// 入参: box OFD区域, pageH 页面高度
// 返回: canvas.Rect PDF区域
func pdfSourceRect(box Box, pageH float64) canvas.Rect {
	return canvas.RectFromSize(box.X, pageH-box.Y-box.H, box.W, box.H)
}

// pdfDestRect 转换PDF跳转目标
// 入参: dest OFD跳转目标, pageH 页面高度
// 返回: canvas.Rect PDF目标区域, bool 是否支持
func pdfDestRect(dest Dest, pageH float64) (canvas.Rect, bool) {
	switch dest.Type {
	case "XYZ":
		return canvas.Rect{X0: dest.Left, Y0: pageH - dest.Top, X1: dest.Left, Y1: pageH - dest.Top}, true
	case "Fit":
		return canvas.Rect{}, true
	case "FitH":
		return canvas.Rect{Y0: pageH - dest.Top, Y1: pageH - dest.Top}, true
	case "FitV":
		return canvas.Rect{X0: dest.Left, X1: dest.Left}, true
	case "FitR":
		return canvas.Rect{X0: dest.Left, Y0: pageH - dest.Bottom, X1: dest.Right, Y1: pageH - dest.Top}, true
	}
	return canvas.Rect{}, false
}

// pdfDestY 获取PDF大纲目标位置
// 入参: dest OFD跳转目标, pageH 页面高度
// 返回: float64 PDF纵坐标
func pdfDestY(dest Dest, pageH float64) float64 {
	switch dest.Type {
	case "XYZ", "FitH", "FitR":
		return pageH - dest.Top
	}
	return 0
}
