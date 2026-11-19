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
	"strings"

	basegeom "github.com/xiaoqidun/ofdgo/internal/base/geom"
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

// PageLink 动作目标，包含外链、跳转、附件和音视频播放
// Box使用页面毫米坐标，Path为可选的页面坐标SVG路径，存在时作为精确点击区域
// 目录和打开动作的Box和Path为空，不限定启动区域
// PlaybackBox为图元视频的播放区域，使用页面毫米坐标；非图元动作为空
// 同一来源的多个动作共享非零Group，组内按返回顺序执行
// NewWindow用于附件动作，为空时采用OFD默认的新窗口
type PageLink struct {
	URI         string
	Target      string
	Box         Box
	PlaybackBox *Box
	Dest        *Dest
	Attachment  string
	NewWindow   *bool
	Path        string
	Group       int
	Sound       *Sound
	Movie       *Movie
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

// UnmarshalXML 读取事件、区域和动作内容，忽略外部同名节点
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (action *Action) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*action = Action{Event: attrValue(start, "Event")}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		var target any
		switch node.Name.Local {
		case "Region":
			target = &action.Region
		case "Goto":
			target = &action.Goto
		case "URI":
			target = &action.URI
		case "GotoA":
			target = &action.GotoA
		case "Sound":
			target = &action.Sound
		case "Movie":
			target = &action.Movie
		default:
			return d.Skip()
		}
		return d.DecodeElement(target, &node)
	})
}

// UnmarshalXML 读取文档内跳转目标或书签，不采用外部扩展中的同名目标
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (action *Goto) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*action = Goto{}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "Dest":
			return d.DecodeElement(&action.Dest, &node)
		case "Bookmark":
			return d.DecodeElement(&action.Bookmark, &node)
		default:
			return d.Skip()
		}
	})
}

// UnmarshalXML 解析跳转目标，忽略外部坐标字段
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
	if err := decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		var target **float64
		switch node.Name.Local {
		case "Left":
			target = &value.Left
		case "Right":
			target = &value.Right
		case "Top":
			target = &value.Top
		case "Bottom":
			target = &value.Bottom
		case "Zoom":
			target = &value.Zoom
		default:
			return d.Skip()
		}
		return d.DecodeElement(target, &node)
	}); err != nil {
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

// MarshalXML 按标准属性写入目标，书签和动作采用相同格式
// 入参: encoder XML编码器, start 起始节点
// 返回: error 参数或编码错误
func (dest Dest) MarshalXML(encoder *xml.Encoder, start xml.StartElement) error {
	attrs, err := dest.attributes()
	if err != nil {
		return err
	}
	start.Attr = attrs
	if err := encoder.EncodeToken(start); err != nil {
		return err
	}
	return encoder.EncodeToken(start.End())
}

// UnmarshalXML 解析视频动作并应用默认值
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (m *Movie) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type movie Movie
	value := movie{Operator: "Play"}
	start = ofdXMLStart(start)
	if err := d.DecodeElement(&value, &start); err != nil {
		return err
	}
	*m = Movie(value)
	return nil
}

// UnmarshalXML 读取动作区域中的OFD分路径，跳过外部扩展区域
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (region *Region) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*region = Region{}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local != "Area" {
			return d.Skip()
		}
		return d.DecodeElement(&region.Area, &node)
	})
}

// UnmarshalXML 按原顺序读取区域指令，外部同名节点不参与路径绘制
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (a *RegionArea) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*a = RegionArea{Start: attrValue(start, "Start")}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
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
		return d.Skip()
	})
}

// actionSource 动作来源
type actionSource struct {
	Box     Box
	Actions []Action
	Matrix  Matrix
	Geo     GeometryPath
	Path    string
	Graphic bool
}

// pageOpenActionSources 获取页面及模板的打开动作，保留模板层次和引用顺序
// 入参: page 页面内容
// 返回: []actionSource 动作来源
func (r *Renderer) pageOpenActionSources(page *PageContent) []actionSource {
	var sources []actionSource
	if len(page.Actions) > 0 {
		sources = append(sources, actionSource{Actions: page.Actions})
	}
	if r.Reader.doc == nil {
		return sources
	}
	for order := range 3 {
		for _, ref := range page.Template {
			if r.templateOrder(ref) != order {
				continue
			}
			if template := r.loadTemplate(ref.TemplateID); template != nil && len(template.Actions) > 0 {
				sources = append(sources, actionSource{Actions: template.Actions})
			}
		}
	}
	return sources
}

// pageActionSources 获取页面及模板内的图元动作来源
// 入参: page 页面内容
// 返回: []actionSource 动作来源
func (r *Renderer) pageActionSources(page *PageContent) []actionSource {
	defer r.bindPageResources(page)()
	sources := make([]actionSource, 0)
	for order := range 3 {
		if r.Reader != nil && r.Reader.doc != nil {
			for _, ref := range page.Template {
				if r.templateOrder(ref) != order {
					continue
				}
				if template := r.loadTemplate(ref.TemplateID); template != nil {
					restoreTpl := r.bindParsedResources(template.pageResources)
					for templateOrder := range 3 {
						sources = r.appendLayerActionSources(sources, template.Content.Layer, templateOrder)
					}
					restoreTpl()
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
		for object := range (PatternContent{Objects: layer.Objects, TextObject: layer.TextObject, PathObject: layer.PathObject, ImageObject: layer.ImageObject, CompositeGraphicUnit: layer.CompositeGraphicUnit}).objects() {
			sources = r.appendGraphicActionSources(sources, object, IdentityMatrix, false, nil)
		}
	}
	return sources
}

// annotationActionSources 获取注释动作来源
// 入参: page 所属页面内容, annotations 页面注释
// 返回: []actionSource 动作来源
func (r *Renderer) annotationActionSources(page *PageContent, annotations []Annotation) []actionSource {
	if page != nil {
		defer r.bindPageResources(page)()
	}
	sources := make([]actionSource, 0)
	for i := range annotations {
		annotation := &annotations[i]
		if annotation.Visible != nil && !*annotation.Visible {
			continue
		}
		var box Box
		if strings.TrimSpace(annotation.Appearance.Boundary) != "" {
			var err error
			box, err = ParseBox(annotation.Appearance.Boundary)
			if err != nil {
				continue
			}
		}
		for object := range annotation.Appearance.objects() {
			sources = r.appendGraphicActionSources(sources, *object, TranslationMatrix(box.X, box.Y), false, nil)
		}
	}
	return sources
}

// appendGraphicActionSources 添加图形对象动作来源
// 入参: sources 动作来源, object 图形对象, parent 父级变换, boundaryInCTM 边界是否参与父级变换, seen 当前资源引用链
// 返回: []actionSource 动作来源
func (r *Renderer) appendGraphicActionSources(sources []actionSource, object GraphicObject, parent Matrix, boundaryInCTM bool, seen map[*CompositeGraphicUnit]bool) []actionSource {
	var boundary, ctm, resource string
	var actions []Action
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
		composite = true
	}
	box, _ := ParseBox(boundary)
	placement := TranslationMatrix(box.X, box.Y).Multiply(parent)
	if boundaryInCTM || composite {
		placement = parent.Multiply(TranslationMatrix(box.X, box.Y))
	}
	matrix := placement.Multiply(NewMatrix(ctm))
	if len(actions) > 0 {
		source := actionSource{Box: placement.TransformBox(Box{W: box.W, H: box.H}), Actions: actions, Matrix: matrix, Graphic: true}
		if placement.a == 1 && placement.b == 0 && placement.c == 0 && placement.d == 1 {
			source.Box.W, source.Box.H = box.W, box.H
		}
		if box.W > 0 && box.H > 0 && !basegeom.AxisAligned(basegeom.MatrixFromValues(placement.Values())) {
			path := GeometryPath(basegeom.Rectangle(Box{W: box.W, H: box.H}))
			for i := range path {
				path[i].End.X, path[i].End.Y = placement.Transform(path[i].End.X, path[i].End.Y)
			}
			source.Geo = path
			source.Path, _ = path.SVG()
		}
		sources = append(sources, source)
	}
	if resource != "" {
		unit := r.compositeDefinition(resource)
		if unit != nil && !seen[unit] {
			if seen == nil {
				seen = make(map[*CompositeGraphicUnit]bool)
			}
			seen[unit] = true
			sources = r.appendGraphicActionSources(sources, GraphicObject{Type: "CompositeGraphicUnit", CompositeGraphicUnit: *unit}, matrix, true, seen)
			delete(seen, unit)
		}
	}
	if composite {
		for child := range object.CompositeGraphicUnit.objects() {
			sources = r.appendGraphicActionSources(sources, child, matrix, true, seen)
		}
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

// effective 应用省略属性的默认值，不改变原始声明
// 返回: Dest 生效的目标参数
func (dest Dest) effective() Dest {
	if dest.OmitLeft {
		dest.Left = 0
	}
	if dest.OmitTop {
		dest.Top = 0
	}
	if dest.OmitZoom {
		dest.Zoom = 0
	}
	return dest
}

// attributes 校验并生成目标属性，缩放为0表示沿用当前比例
// 返回: ofdAttrs 标准属性, error 模式或参数错误
func (dest Dest) attributes() (ofdAttrs, error) {
	dest = dest.effective()
	attrs := ofdAttrs{{Name: xml.Name{Local: "Type"}, Value: dest.Type}, {Name: xml.Name{Local: "PageID"}, Value: dest.PageID}}
	if dest.PageID == "" {
		return nil, fmt.Errorf("missing destination page")
	}
	add := func(name string, value float64, omit bool) error {
		if !finite(value) {
			return fmt.Errorf("invalid destination %s", name)
		}
		if !omit {
			attrs.add(name, ofdNumber(value))
		}
		return nil
	}
	switch dest.Type {
	case "XYZ":
		if dest.Zoom != 0 && (dest.Zoom < 0.1 || dest.Zoom > 64) {
			return nil, fmt.Errorf("destination zoom must be zero or between 0.1 and 64")
		}
		if err := add("Zoom", dest.Zoom, dest.OmitZoom); err != nil {
			return nil, err
		}
		fallthrough
	case "FitR":
		if err := add("Left", dest.Left, dest.OmitLeft); err != nil {
			return nil, err
		}
		if err := add("Top", dest.Top, dest.OmitTop); err != nil {
			return nil, err
		}
		if dest.Type == "FitR" {
			if dest.Right <= dest.Left || dest.Bottom <= dest.Top {
				return nil, fmt.Errorf("empty destination rectangle")
			}
			if err := add("Right", dest.Right, false); err != nil {
				return nil, err
			}
			if err := add("Bottom", dest.Bottom, false); err != nil {
				return nil, err
			}
		}
	case "FitH":
		if err := add("Top", dest.Top, dest.OmitTop); err != nil {
			return nil, err
		}
	case "FitV":
		if err := add("Left", dest.Left, dest.OmitLeft); err != nil {
			return nil, err
		}
	case "Fit":
	default:
		return nil, fmt.Errorf("invalid destination type %q", dest.Type)
	}
	return attrs, nil
}

// decodeOFDActions 读取标准动作列表，不将外部同名节点作为交互动作
// 入参: d XML解码器, start 列表起始节点, actions 动作接收列表
// 返回: error 解析错误
func decodeOFDActions(d *xml.Decoder, start xml.StartElement, actions *[]Action) error {
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local != "Action" {
			return d.Skip()
		}
		var action Action
		if err := d.DecodeElement(&action, &node); err != nil {
			return err
		}
		*actions = append(*actions, action)
		return nil
	})
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
