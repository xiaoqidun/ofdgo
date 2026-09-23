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

// PageLinks 获取页面、模板和可见注释的点击链接，包含复杂区域、组合图元和附件动作
// 入参: page 页面内容
// 返回: []PageLink 页面链接, error 错误信息
func (r *Renderer) PageLinks(page *PageContent) ([]PageLink, error) {
	box, err := r.GetPageBox(page)
	if err != nil {
		return nil, err
	}
	sources := r.pageActionSources(page, box)
	if r.RenderAnnotations {
		sources = append(sources, r.annotationActionSources(r.Reader.Annots[page.ID])...)
	}
	var links []PageLink
	var bookmarks map[string]Dest
	for sourceIndex, source := range sources {
		start := len(links)
		for _, action := range source.Actions {
			if action.Event != "CLICK" {
				continue
			}
			box, path, err := r.actionLinkRegion(source, action)
			if err != nil {
				return nil, err
			}
			if box.W <= 0 || box.H <= 0 {
				continue
			}
			link := PageLink{Box: box, Path: path}
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
				links = append(links, link)
			} else if action.GotoA != nil {
				link.Attachment = action.GotoA.AttachID
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
