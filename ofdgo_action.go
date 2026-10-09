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
	"encoding/xml"
	"fmt"
	"net/url"
	"strconv"
)

// Action 动作
type Action struct {
	Event  string  `xml:"Event,attr"`
	Region *Region `xml:"Region"`
	Goto   *Goto   `xml:"Goto"`
	URI    *URI    `xml:"URI"`
	GotoA  *GotoA  `xml:"GotoA"`
	Sound  *Sound  `xml:"Sound"`
	Movie  *Movie  `xml:"Movie"`
}

// Goto 文档内跳转动作
type Goto struct {
	Dest     *Dest         `xml:"Dest"`
	Bookmark *GotoBookmark `xml:"Bookmark"`
}

// Dest 文档内跳转目标，坐标单位为毫米
// Omit字段保留属性省略状态；Left和Top省略时取0，Zoom省略或为0时保持当前缩放
type Dest struct {
	Type     string  `xml:"Type,attr"`
	PageID   string  `xml:"PageID,attr"`
	Left     float64 `xml:"Left"`
	Right    float64 `xml:"Right"`
	Top      float64 `xml:"Top"`
	Bottom   float64 `xml:"Bottom"`
	Zoom     float64 `xml:"Zoom"`
	OmitLeft bool    `xml:"-"`
	OmitTop  bool    `xml:"-"`
	OmitZoom bool    `xml:"-"`
}

// GotoBookmark 书签跳转目标
type GotoBookmark struct {
	Name string `xml:"Name,attr"`
}

// URI URI动作
// Target保留Schema属性，不推断窗口语义
type URI struct {
	URI    string `xml:"URI,attr"`
	Base   string `xml:"Base,attr"`
	Target string `xml:"Target,attr,omitempty"`
}

// PageLink 页面点击动作，包含外链、跳转、附件和音视频播放
// Box使用页面毫米坐标，Path为可选的页面坐标SVG路径，存在时作为精确点击区域
// 同一来源的多个动作共享非零Group，组内按返回顺序执行
// NewWindow用于附件动作，为空时采用OFD默认的新窗口
type PageLink struct {
	URI        string
	Target     string
	Box        Box
	Dest       *Dest
	Attachment string
	NewWindow  *bool
	Path       string
	Group      int
	Sound      *Sound
	Movie      *Movie
}

// GotoA 附件动作
type GotoA struct {
	AttachID  string `xml:"AttachID,attr"`
	NewWindow *bool  `xml:"NewWindow,attr"`
}

// Sound 音频动作
type Sound struct {
	ResourceID  string `xml:"ResourceID,attr"`
	Volume      *int   `xml:"Volume,attr"`
	Repeat      bool   `xml:"Repeat,attr"`
	Synchronous bool   `xml:"Synchronous,attr"`
}

// Movie 视频动作
type Movie struct {
	ResourceID string `xml:"ResourceID,attr"`
	Operator   string `xml:"Operator,attr"`
}

// Region 动作区域
type Region struct {
	Area []RegionArea `xml:"Area"`
}

// RegionArea 动作区域分路径
type RegionArea struct {
	Start   string          `xml:"Start,attr"`
	Command []RegionCommand `xml:"-"`
}

// RegionCommand 动作区域绘制指令
type RegionCommand struct {
	Type           string
	Point1         string
	Point2         string
	Point3         string
	EllipseSize    string
	RotationAngle  string
	LargeArc       string
	SweepDirection string
	EndPoint       string
}

// actionSource 动作来源
type actionSource struct {
	Box     Box
	Actions []Action
	Matrix  Matrix
	Path    string
}

// UnmarshalXML 解析跳转目标
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (dest *Dest) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var value struct {
		Left   *float64 `xml:"Left"`
		Right  *float64 `xml:"Right"`
		Top    *float64 `xml:"Top"`
		Bottom *float64 `xml:"Bottom"`
		Zoom   *float64 `xml:"Zoom"`
	}
	*dest = Dest{Type: attrValue(start, "Type"), PageID: attrValue(start, "PageID"), OmitLeft: true, OmitTop: true, OmitZoom: true}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" {
			continue
		}
		var target *float64
		switch attr.Name.Local {
		case "Left":
			dest.OmitLeft = false
			target = &dest.Left
		case "Top":
			dest.OmitTop = false
			target = &dest.Top
		case "Zoom":
			dest.OmitZoom = false
			target = &dest.Zoom
		case "Right":
			target = &dest.Right
		case "Bottom":
			target = &dest.Bottom
		}
		if target != nil {
			v, err := strconv.ParseFloat(attr.Value, 64)
			if err != nil || !finite(v) {
				return fmt.Errorf("invalid destination %s", attr.Name.Local)
			}
			*target = v
		}
	}
	if err := d.DecodeElement(&value, &start); err != nil {
		return err
	}
	if value.Left != nil {
		dest.Left = *value.Left
		dest.OmitLeft = false
	}
	if value.Right != nil {
		dest.Right = *value.Right
	}
	if value.Top != nil {
		dest.Top = *value.Top
		dest.OmitTop = false
	}
	if value.Bottom != nil {
		dest.Bottom = *value.Bottom
	}
	if value.Zoom != nil {
		dest.Zoom = *value.Zoom
		dest.OmitZoom = false
	}
	for _, number := range []float64{dest.Left, dest.Right, dest.Top, dest.Bottom, dest.Zoom} {
		if !finite(number) {
			return fmt.Errorf("invalid destination coordinate")
		}
	}
	return nil
}

// UnmarshalXML 解析视频动作并应用默认值
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (m *Movie) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type movie Movie
	value := movie{Operator: "Play"}
	if err := d.DecodeElement(&value, &start); err != nil {
		return err
	}
	*m = Movie(value)
	return nil
}

// UnmarshalXML 解析动作区域分路径并保留指令顺序
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (a *RegionArea) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*a = RegionArea{Start: attrValue(start, "Start")}
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch node := tok.(type) {
		case xml.StartElement:
			command := RegionCommand{
				Type:           node.Name.Local,
				Point1:         attrValue(node, "Point1"),
				Point2:         attrValue(node, "Point2"),
				Point3:         attrValue(node, "Point3"),
				EllipseSize:    attrValue(node, "EllipseSize"),
				RotationAngle:  attrValue(node, "RotationAngle"),
				LargeArc:       attrValue(node, "LargeArc"),
				SweepDirection: attrValue(node, "SweepDirection"),
				EndPoint:       attrValue(node, "EndPoint"),
			}
			a.Command = append(a.Command, command)
			if err := d.Skip(); err != nil {
				return err
			}
		case xml.EndElement:
			if node.Name.Local == start.Name.Local {
				return nil
			}
		}
	}
}

// gotoDest 获取文档内跳转目标
// 入参: action 跳转动作, bookmarks 书签
// 返回: *Dest 跳转目标
func gotoDest(action *Goto, bookmarks map[string]Dest) *Dest {
	if action.Dest != nil {
		dest := action.Dest.effective()
		return &dest
	}
	if action.Bookmark != nil {
		if dest, ok := bookmarks[action.Bookmark.Name]; ok {
			dest = dest.effective()
			return &dest
		}
	}
	return nil
}

// pageActionSources 获取页面动作来源
// 入参: page 页面内容, box 页面区域
// 返回: []actionSource 动作来源
func (r *Renderer) pageActionSources(page *PageContent, box Box) []actionSource {
	sources := make([]actionSource, 0)
	if len(page.Actions) > 0 {
		sources = append(sources, actionSource{
			Box:     Box{W: box.W, H: box.H},
			Actions: page.Actions,
			Matrix:  IdentityMatrix,
		})
	}
	for order := range 3 {
		if r.Reader.doc != nil {
			for _, ref := range page.Template {
				kind := ref.ZOrder
				if kind == "" {
					kind = "Background"
				}
				if layerOrder(kind) != order {
					continue
				}
				if template := r.loadTemplate(ref.TemplateID); template != nil {
					sources = append(sources, actionSource{Box: Box{W: box.W, H: box.H}, Actions: template.Actions, Matrix: IdentityMatrix})
					for templateOrder := range 3 {
						sources = r.appendLayerActionSources(sources, template.Content.Layer, templateOrder)
					}
				}
			}
		}
		sources = r.appendLayerActionSources(sources, page.Content.Layer, order)
	}
	return sources
}

// appendLayerActionSources 按绘制顺序添加图层动作来源
// 入参: sources 动作来源, layers 页面图层, order 图层顺序
// 返回: []actionSource 动作来源
func (r *Renderer) appendLayerActionSources(sources []actionSource, layers []Layer, order int) []actionSource {
	for _, layer := range layers {
		if layerOrder(layer.Type) != order {
			continue
		}
		for _, object := range layer.Objects {
			sources = r.appendGraphicActionSources(sources, object, IdentityMatrix, false, nil)
		}
	}
	return sources
}

// annotationActionSources 获取注释动作来源
// 入参: annotations 页面注释
// 返回: []actionSource 动作来源
func (r *Renderer) annotationActionSources(annotations []Annotation) []actionSource {
	sources := make([]actionSource, 0)
	for _, annotation := range annotations {
		if annotation.Visible != nil && !*annotation.Visible {
			continue
		}
		box, err := ParseBox(annotation.Appearance.Boundary)
		if err != nil {
			continue
		}
		for _, object := range annotation.Appearance.Objects {
			sources = r.appendGraphicActionSources(sources, object, TranslationMatrix(box.X, box.Y), false, nil)
		}
	}
	return sources
}

// appendGraphicActionSources 添加图形对象动作来源
// 入参: sources 动作来源, object 图形对象, parent 父级变换, boundaryInCTM 边界是否参与父级变换, seen 当前资源引用链
// 返回: []actionSource 动作来源
func (r *Renderer) appendGraphicActionSources(sources []actionSource, object GraphicObject, parent Matrix, boundaryInCTM bool, seen map[string]bool) []actionSource {
	var boundary, ctm, resource string
	var actions []Action
	var children []GraphicObject
	composite := false
	switch object.Type {
	case "TextObject":
		boundary = object.TextObject.Boundary
		ctm = object.TextObject.CTM
		actions = object.TextObject.Actions
	case "PathObject":
		boundary = object.PathObject.Boundary
		ctm = object.PathObject.CTM
		actions = object.PathObject.Actions
	case "ImageObject":
		boundary = object.ImageObject.Boundary
		ctm = object.ImageObject.CTM
		actions = object.ImageObject.Actions
	case "CompositeGraphicUnit", "CompositeObject":
		boundary = object.CompositeGraphicUnit.Boundary
		ctm = object.CompositeGraphicUnit.CTM
		resource = object.CompositeGraphicUnit.ResourceID
		actions = object.CompositeGraphicUnit.Actions
		children = object.CompositeGraphicUnit.Objects
		composite = true
	}
	box, _ := ParseBox(boundary)
	placement := TranslationMatrix(box.X, box.Y).Multiply(parent)
	if boundaryInCTM || composite {
		placement = parent.Multiply(TranslationMatrix(box.X, box.Y))
	}
	matrix := placement.Multiply(NewMatrix(ctm))
	if len(actions) > 0 {
		source := actionSource{Box: placement.TransformBox(Box{W: box.W, H: box.H}), Actions: actions, Matrix: matrix}
		if placement.a == 1 && placement.b == 0 && placement.c == 0 && placement.d == 1 {
			source.Box.W, source.Box.H = box.W, box.H
		}
		if box.W > 0 && box.H > 0 && !axisAlignedMatrix(placement) {
			path := geometryRectangle(Box{W: box.W, H: box.H})
			for i := range path {
				path[i].End.X, path[i].End.Y = placement.Transform(path[i].End.X, path[i].End.Y)
			}
			source.Path, _ = path.SVG()
		}
		sources = append(sources, source)
	}
	if resource != "" && !seen[resource] {
		if unit := r.CompositeGraphicUnits[resource]; unit != nil {
			if seen == nil {
				seen = make(map[string]bool)
			}
			seen[resource] = true
			sources = r.appendGraphicActionSources(sources, GraphicObject{Type: "CompositeGraphicUnit", CompositeGraphicUnit: *unit}, matrix, true, seen)
			delete(seen, resource)
		}
	}
	for _, child := range children {
		sources = r.appendGraphicActionSources(sources, child, matrix, boundaryInCTM, seen)
	}
	return sources
}

// actionLinkRegion 获取动作点击区域
// 入参: source 动作来源, action 动作
// 返回: Box 外接矩形, string 精确路径, error 几何错误
func (r *Renderer) actionLinkRegion(source actionSource, action Action) (Box, string, error) {
	if action.Region == nil {
		return source.Box, source.Path, nil
	}
	geometry, err := r.Geometry()
	if err != nil {
		return Box{}, "", err
	}
	path, err := geometry.Region(action.Region, source.Matrix)
	if err != nil {
		return Box{}, "", err
	}
	box, err := geometry.Bounds(path)
	if err != nil {
		return Box{}, "", err
	}
	outline, err := path.SVG()
	return box, outline, err
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
