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
)

// ObjectContour 页面坐标中的填充轮廓，Path为SVG路径，EvenOdd表示奇偶填充
type ObjectContour struct {
	Path    string `json:"path"`
	EvenOdd bool   `json:"evenOdd,omitempty"`
}

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
	area := page.Area
	if r != nil && r.Reader != nil {
		if r.Reader.doc == nil && r.Reader.OFD != nil {
			_, _ = r.Reader.docStructureContext(r.outputContext())
		}
		var err error
		area, err = r.Reader.resolvePageAreaContext(r.outputContext(), page.Area, page.Template)
		if err != nil {
			return Box{}, err
		}
	}
	return parsePageAreaBox(area)
}

// PageLinks 获取页面、模板和可见注释中图元的点击动作，保留复杂区域和组合图元
// 入参: page 页面内容
// 返回: []PageLink 页面链接, error 错误信息
func (r *Renderer) PageLinks(page *PageContent) ([]PageLink, error) {
	ctx := r.pageContext
	if ctx == nil {
		ctx = r.outputContext()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.Reader != nil && r.Reader.doc == nil && r.Reader.OFD != nil {
		if _, err := r.Reader.docStructureContext(ctx); err != nil {
			return nil, err
		}
	}
	defer r.bindPageResources(page)()
	var annotations []Annotation
	if r.RenderAnnotations && r.Reader != nil {
		var err error
		annotations, err = r.Reader.pageAnnotationsContext(ctx, page.ID)
		if err != nil {
			return nil, err
		}
	}
	sources := r.pageActionSources(page)
	if r.RenderAnnotations && len(annotations) > 0 {
		if r.printing {
			filtered := make([]Annotation, 0, len(annotations))
			for _, annot := range annotations {
				if annot.Print != nil && !*annot.Print {
					continue
				}
				filtered = append(filtered, annot)
			}
			annotations = filtered
		}
		sources = append(sources, r.annotationActionSources(page, annotations)...)
	}
	sources = r.translateActionSources(page, sources)
	return r.sourceLinks(sources, "CLICK")
}

// DocumentOpenLinks 获取文档打开动作的有序目标，不执行动作
// 返回: []PageLink 动作目标, error 文档解析错误
func (r *Renderer) DocumentOpenLinks() ([]PageLink, error) {
	doc, err := r.Reader.docStructure()
	if err != nil {
		return nil, err
	}
	return r.sourceLinks([]actionSource{{Actions: doc.Actions}}, "DO")
}

// PageOpenLinks 获取页面及模板打开动作的有序目标，不遍历图元或执行动作
// 入参: page 页面内容
// 返回: []PageLink 动作目标, error 目标解析错误
func (r *Renderer) PageOpenLinks(page *PageContent) ([]PageLink, error) {
	if r.Reader != nil && r.Reader.doc == nil && r.Reader.OFD != nil {
		if _, err := r.Reader.docStructureContext(r.outputContext()); err != nil {
			return nil, err
		}
	}
	defer r.bindPageResources(page)()
	return r.sourceLinks(r.pageOpenActionSources(page), "PO")
}

// OutlineLinks 获取目录节点的有序动作目标，不执行动作
// 入参: path 从根节点开始的各级索引
// 返回: []PageLink 动作目标，不包含页面点击区域, error 目录路径或解析错误
func (r *Renderer) OutlineLinks(path []int) ([]PageLink, error) {
	doc, err := r.Reader.docStructure()
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

// ObjectBounds 获取文字、路径、图片或复合对象在页面坐标中的轴对齐范围，不修改对象
// 文字采用字形范围，路径包含描边与裁剪，图片采用裁剪后的几何范围，不解码像素或排除透明像素
// 底纹按填充或描边轮廓度量，不展开图案单元
// 不应用页面边界或父级变换；无可见范围时返回零值，不支持的对象类型返回错误
// 入参: object 图形对象, drawParam 图层绘制参数标识，无继承时为空
// 返回: Box 毫米坐标范围, error 错误信息
func (r *Renderer) ObjectBounds(object GraphicObject, drawParam string) (Box, error) {
	result, err := r.MeasureObject(object, MeasureOptions{Defaults: r.drawParamDefaults(drawParam, nil)})
	return result.Bounds, err
}

// AnnotationGeometry 获取注解外观的实际内容轮廓，不将Appearance容器边界视为绘制内容
// 链接保留透明对象定义的交互区域，图片按几何范围度量而不解码透明像素
// 入参: annotation 注解
// 返回: Box 页面范围, []ObjectContour 命中轮廓, error 错误信息
func (r *Renderer) AnnotationGeometry(annotation Annotation) (Box, []ObjectContour, error) {
	measurement := &annotationMeasurement{renderer: r}
	if err := r.walkAnnotation(annotation, measurement); err != nil {
		return Box{}, nil, err
	}
	box, contours := measurement.result.Bounds, measurement.result.Contours
	for _, source := range r.annotationActionSources(nil, []Annotation{annotation}) {
		for _, action := range source.Actions {
			if action.Event != "CLICK" {
				continue
			}
			region, path, err := r.actionLinkRegion(source, action)
			if err != nil {
				return Box{}, nil, err
			}
			if region.W <= 0 || region.H <= 0 {
				continue
			}
			if path == "" {
				path = fmt.Sprintf("M%g %gH%gV%gH%gZ", region.X, region.Y, region.X+region.W, region.Y+region.H, region.X)
			}
			box = unionTextBox(box, region)
			contours = append(contours, ObjectContour{Path: path})
		}
	}
	return box, contours, nil
}

// ObjectContours 获取路径、图片或复合对象的实际绘制区域，包含描边、虚线、变换及对象裁剪
// 图片不解码像素，底纹不展开图案单元；不包含页面边界或父级变换
// 入参: object 路径、图片或复合对象, drawParam 图层绘制参数标识
// 返回: []ObjectContour 可用于点选的轮廓, error 错误信息
func (r *Renderer) ObjectContours(object GraphicObject, drawParam string) ([]ObjectContour, error) {
	_, contours, err := r.ObjectGeometry(object, drawParam)
	return contours, err
}

// ObjectGeometry 一次度量路径、图片或复合对象的范围与轮廓，语义与ObjectBounds和ObjectContours一致
// 入参: object 图形对象, drawParam 图层绘制参数标识
// 返回: Box 毫米范围, []ObjectContour 绘制轮廓, error 错误信息
func (r *Renderer) ObjectGeometry(object GraphicObject, drawParam string) (Box, []ObjectContour, error) {
	if object.Type != "PathObject" && object.Type != "ImageObject" && object.Type != "CompositeObject" && object.Type != "CompositeGraphicUnit" {
		return Box{}, nil, fmt.Errorf("contours require a path, image or composite object")
	}
	result, err := r.MeasureObject(object, MeasureOptions{Defaults: r.drawParamDefaults(drawParam, nil), Contours: true})
	return result.Bounds, result.Contours, err
}

// MeasureObject 使用当前页面编译器度量对象，保持绘制与选区的字体和几何语义一致
// 入参: object 图形对象, options 度量上下文
// 返回: ObjectMeasurement 度量结果, error 错误信息
func (r *Renderer) MeasureObject(object GraphicObject, options MeasureOptions) (ObjectMeasurement, error) {
	if r.backends.Compiler == nil {
		return ObjectMeasurement{}, fmt.Errorf("%w: object measurement", ErrBackendUnavailable)
	}
	return r.backends.Compiler.MeasureObject(r, object, options)
}

// annotationMeasurement 按注解绘制顺序收集对象范围和轮廓
type annotationMeasurement struct {
	renderer *Renderer
	result   ObjectMeasurement
}

// DrawObject 使用对象实际继承状态度量注解内容，保留外观边界裁剪
// 入参: object 图形对象, state 绘制状态
// 返回: error 度量错误
func (m *annotationMeasurement) DrawObject(object *GraphicObject, state RenderState) error {
	if state.Hidden {
		return nil
	}
	result, err := m.renderer.MeasureObject(*object, MeasureOptions{Defaults: state.Defaults, Parent: state.Parent, BoundaryInCTM: state.BoundaryInCTM, Clip: state.Clip, Contours: true})
	if err != nil {
		return err
	}
	m.result.Bounds = unionTextBox(m.result.Bounds, result.Bounds)
	m.result.Contours = append(m.result.Contours, result.Contours...)
	return nil
}

// DrawStamp 满足页面访问接口，注解外观遍历不包含数字签章
// 入参: stamp 数字签章
// 返回: error 始终为空
func (m *annotationMeasurement) DrawStamp(stamp Stamp) error {
	return nil
}

// translateActionSources 在页面物理区域原点非零时将动作区域平移至输出视口坐标
// 入参: page 页面内容, sources 原始动作来源
// 返回: []actionSource 平移后的动作来源
func (r *Renderer) translateActionSources(page *PageContent, sources []actionSource) []actionSource {
	if page == nil || len(sources) == 0 {
		return sources
	}
	box, err := r.GetPageBox(page)
	if err != nil || (box.X == 0 && box.Y == 0) {
		return sources
	}
	shift := TranslationMatrix(-box.X, -box.Y)
	result := make([]actionSource, len(sources))
	copy(result, sources)
	for i := range result {
		result[i].Box.X -= box.X
		result[i].Box.Y -= box.Y
		result[i].Matrix = shift.Multiply(result[i].Matrix)
		if len(result[i].Geo) > 0 {
			if shifted, shiftErr := result[i].Geo.Transform(shift); shiftErr == nil {
				result[i].Geo = shifted
				if svg, svgErr := shifted.SVG(); svgErr == nil {
					result[i].Path = svg
				}
			}
		}
	}
	return result
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
					doc, err := r.Reader.docStructure()
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
