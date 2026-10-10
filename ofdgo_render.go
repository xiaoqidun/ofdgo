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

import "fmt"

// RenderPage 编译页面为与绘图库无关的只读绘制数据
// 入参: page 页面内容
// 返回: *RasterPage 绘制页面, error 错误信息
func (r *Renderer) RenderPage(page *PageContent) (*RasterPage, error) {
	return r.CompilePage(page)
}

// GetPageBox 获取页面物理区域
// 入参: page 页面内容
// 返回: Box 区域, error 错误信息
func (r *Renderer) GetPageBox(page *PageContent) (Box, error) {
	area, err := r.Reader.resolvePageArea(page.Area, page.Template)
	if err != nil {
		return Box{}, err
	}
	boxStr := area.PhysicalBox
	if boxStr == "" {
		boxStr = area.ApplicationBox
	}
	if boxStr == "" {
		boxStr = area.ContentBox
	}
	if boxStr == "" {
		boxStr = "0 0 210 297"
	}
	return ParseBox(boxStr)
}

// PageLinks 获取页面、模板和可见注释中图元的点击动作，保留复杂区域和组合图元
// 入参: page 页面内容
// 返回: []PageLink 页面链接, error 错误信息
func (r *Renderer) PageLinks(page *PageContent) ([]PageLink, error) {
	sources := r.pageActionSources(page)
	if r.RenderAnnotations {
		sources = append(sources, r.annotationActionSources(r.Reader.Annots[page.ID])...)
	}
	return r.sourceLinks(sources, "CLICK")
}

// DocumentOpenLinks 获取文档打开动作的有序目标，不执行动作
// 返回: []PageLink 动作目标, error 文档解析错误
func (r *Renderer) DocumentOpenLinks() ([]PageLink, error) {
	doc, err := r.Reader.Doc()
	if err != nil {
		return nil, err
	}
	return r.sourceLinks([]actionSource{{Actions: doc.Actions}}, "DO")
}

// PageOpenLinks 获取页面及模板打开动作的有序目标，不遍历图元或执行动作
// 入参: page 页面内容
// 返回: []PageLink 动作目标, error 目标解析错误
func (r *Renderer) PageOpenLinks(page *PageContent) ([]PageLink, error) {
	return r.sourceLinks(r.pageOpenActionSources(page), "PO")
}

// OutlineLinks 获取目录节点的有序动作目标，不执行动作
// 入参: path 从根节点开始的各级索引
// 返回: []PageLink 动作目标，不包含页面点击区域, error 目录路径或解析错误
func (r *Renderer) OutlineLinks(path []int) ([]PageLink, error) {
	doc, err := r.Reader.Doc()
	if err != nil {
		return nil, err
	}
	if len(path) == 0 {
		return nil, fmt.Errorf("empty outline path")
	}
	nodes := doc.Outlines.OutlineElem
	var node OutlineElem
	for _, index := range path {
		if index < 0 || index >= len(nodes) {
			return nil, fmt.Errorf("invalid outline index: %d", index)
		}
		node = nodes[index]
		nodes = node.OutlineElem
	}
	return r.sourceLinks([]actionSource{{Actions: node.Actions}}, "")
}

// sourceLinks 解析动作目标，保留动作顺序及独立目标数据
// 入参: sources 动作来源, event 事件类型，空值表示目录激活动作
// 返回: []PageLink 动作目标, error 区域或文档解析错误
func (r *Renderer) sourceLinks(sources []actionSource, event string) ([]PageLink, error) {
	var links []PageLink
	var bookmarks map[string]Dest
	for sourceIndex, source := range sources {
		start := len(links)
		for _, action := range source.Actions {
			if event != "" && action.Event != event {
				continue
			}
			var link PageLink
			if event == "CLICK" {
				box, path, err := r.actionLinkRegion(source, action)
				if err != nil {
					return nil, err
				}
				if box.W <= 0 || box.H <= 0 {
					continue
				}
				link.Box, link.Path = box, path
			}
			if action.Goto != nil {
				if bookmarks == nil {
					doc, err := r.Reader.Doc()
					if err != nil {
						return nil, err
					}
					bookmarks = make(map[string]Dest, len(doc.Bookmarks.Bookmark))
					for _, bookmark := range doc.Bookmarks.Bookmark {
						bookmarks[bookmark.Name] = bookmark.Dest
					}
				}
				if dest := gotoDest(action.Goto, bookmarks); dest != nil {
					value := *dest
					link.Dest = &value
					links = append(links, link)
				}
			} else if action.URI != nil && action.URI.URI != "" {
				link.URI = resolveActionURI(*action.URI)
				link.Target = action.URI.Target
				links = append(links, link)
			} else if action.GotoA != nil {
				link.Attachment = action.GotoA.AttachID
				if action.GotoA.NewWindow != nil {
					window := *action.GotoA.NewWindow
					link.NewWindow = &window
				}
				links = append(links, link)
			} else if action.Sound != nil {
				value := *action.Sound
				if value.Volume != nil {
					volume := *value.Volume
					value.Volume = &volume
				}
				link.Sound = &value
				links = append(links, link)
			} else if action.Movie != nil {
				value := *action.Movie
				if value.Operator == "" {
					value.Operator = "Play"
				}
				link.Movie = &value
				if source.Graphic {
					playback := source.Box
					link.PlaybackBox = &playback
				}
				links = append(links, link)
			}
		}
		if len(links)-start > 1 {
			for i := start; i < len(links); i++ {
				links[i].Group = sourceIndex + 1
			}
		}
	}
	return links, nil
}

// RenderPageByIndex 按索引渲染页面
// 入参: index 页面索引，从0开始
// 返回: *RasterPage 绘制页面, error 错误信息
func (r *Renderer) RenderPageByIndex(index int) (*RasterPage, error) {
	page, err := r.Reader.PageContentByIndex(index)
	if err != nil {
		return nil, err
	}
	return r.RenderPage(page)
}
