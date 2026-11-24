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
	"crypto/sha256"
	"encoding/binary"
	"encoding/xml"
	"hash"
	"math"
	"slices"
	"strconv"
	"strings"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfFormBufferLimit 限制表单编码暂存容量，不限制资源大小
const pdfFormBufferLimit = 8 << 20

// pdfFormPointsToMM 定义PDF点到OFD毫米的换算系数
const pdfFormPointsToMM = 25.4 / 72.0

// pdfFormIdentity 唯一标识PDF表单XObject的间接引用或底层流
type pdfFormIdentity struct {
	ref    pdfgo.Reference
	stream *pdfgo.Stream
}

// pdfFormRecord 保存已注册局部矢量资源的原始对象及尺寸，供并行合并重映射
type pdfFormRecord struct {
	objects []GraphicObject
	width   float64
	height  float64
}

// pdfIsolatedGroupKey 区分独立透明组表单的局部光栅缓存键
type pdfIsolatedGroupKey struct {
	ref           pdfgo.Reference
	stream        *pdfgo.Stream
	bounds        pdfgo.Rectangle
	dpi           int
	alpha         int
	knockout      bool
	space         string
	contentDigest [32]byte
}

// pdfIsolatedGroupImage 保存独立透明组光栅化后的共享图像资源与局部边界
type pdfIsolatedGroupImage struct {
	id       string
	localBox Box
}

// pdfTemplateCandidateKey 区分多页固定位置高频表单模板的提升签名
type pdfTemplateCandidateKey struct {
	resourceID  string
	boundary    string
	ctm         string
	pageArea    PageArea
	zOrder      string
	alpha       int
	clipsDigest [32]byte
	hasClips    bool
}

// pdfFormResource 保存生成内容及资源编号，复用前核对完整内容
type pdfFormResource struct {
	content []byte
	id      string
}

// pdfFormCache 隔离编辑器的表单资源，复用编码缓冲而不共享缓存内容
type pdfFormCache struct {
	editor  *Editor
	entries renderCache[[32]byte, pdfFormResource]
	buffer  bytes.Buffer
}

// formIdentity 生成表单唯一标识，存在引用文档时保留流指针避免对象编号碰撞
// 入参: ref 表单间接引用, stream 表单流指针
// 返回: pdfFormIdentity 表单标识
func (p *pdfImporter) formIdentity(ref pdfgo.Reference, stream *pdfgo.Stream) pdfFormIdentity {
	if !p.hasReferenceDocuments() && ref != (pdfgo.Reference{}) {
		return pdfFormIdentity{ref: ref}
	}
	if stream != nil {
		return pdfFormIdentity{ref: ref, stream: stream}
	}
	return pdfFormIdentity{ref: ref}
}

// countLocalForms 统计当前页所有普通表单及透明组表单的引用次数
// 入参: nodes 页面原始图元切片
// 返回: map[pdfFormIdentity]int 页内表单出现频次
func (p *pdfImporter) countLocalForms(nodes []pdfCompositeNode) map[pdfFormIdentity]int {
	var counts map[pdfFormIdentity]int
	add := func(ref pdfgo.Reference, stream *pdfgo.Stream) {
		if ref == (pdfgo.Reference{}) && stream == nil {
			return
		}
		if counts == nil {
			counts = make(map[pdfFormIdentity]int)
		}
		counts[p.formIdentity(ref, stream)]++
	}
	var visit func([]pdfCompositeNode)
	visit = func(list []pdfCompositeNode) {
		for i := 0; i < len(list); i++ {
			node := list[i]
			for _, span := range node.forms {
				if span.count > 0 && span.mark != nil {
					add(span.mark.Ref, span.mark.Stream)
				}
			}
			if node.group != nil && !node.group.Page {
				add(node.group.Ref, node.group.Stream)
			}
			if len(node.children) != 0 {
				visit(node.children)
			}
		}
	}
	visit(nodes)
	return counts
}

// stripFormOuterClips 提取调用表单前的外层裁剪并剥离表单自身BBox冗余裁剪，不修改原节点
// 入参: nodes 表单内图元, outerCount 外层裁剪数量
// 返回: []pdfgo.Path 外层裁剪, []pdfCompositeNode 剥离后的浅拷贝图元, bool 是否可安全剥离
func stripFormOuterClips(nodes []pdfCompositeNode, outerCount int) ([]pdfgo.Path, []pdfCompositeNode, bool) {
	var outer []pdfgo.Path
	outerSet := false
	ok := true
	var cloneList func([]pdfCompositeNode) []pdfCompositeNode
	cloneList = func(list []pdfCompositeNode) []pdfCompositeNode {
		if len(list) == 0 || !ok {
			return nil
		}
		result := make([]pdfCompositeNode, len(list))
		for i, node := range list {
			copyNode := node
			stripStyle := func(style *pdfgo.Style) {
				if len(style.Clips) < outerCount {
					ok = false
					return
				}
				if outerCount > 0 {
					prefix := style.Clips[:outerCount]
					if !outerSet {
						outer, outerSet = prefix, true
					} else if len(outer) != len(prefix) || (len(prefix) > 0 && &outer[0] != &prefix[0] && !sameClipPrefix(outer, prefix)) {
						ok = false
						return
					}
				}
				strip := outerCount
				if outerCount > 0 && len(style.Clips) > outerCount && len(style.Clips[outerCount].Text) == 0 {
					for i := range outer {
						if sameClipPrefix(outer[i:i+1], style.Clips[outerCount:outerCount+1]) {
							strip = outerCount + 1
							break
						}
					}
				}
				if strip == len(style.Clips) {
					style.Clips = nil
				} else if strip > 0 {
					style.Clips = style.Clips[strip:]
				}
			}
			if node.path != nil {
				copyMark := *node.path
				stripStyle(&copyMark.Style)
				copyNode.path = &copyMark
			} else if node.text != nil {
				copyMark := *node.text
				stripStyle(&copyMark.Style)
				copyNode.text = &copyMark
			} else if node.image != nil {
				copyMark := *node.image
				stripStyle(&copyMark.Style)
				copyNode.image = &copyMark
			} else if node.glyph != nil {
				copyMark := *node.glyph
				stripStyle(&copyMark.Style)
				copyNode.glyph = &copyMark
			}
			if len(node.children) != 0 {
				copyNode.children = cloneList(node.children)
			}
			result[i] = copyNode
		}
		return result
	}
	stripped := cloneList(nodes)
	if !ok {
		return nil, nil, false
	}
	return outer, stripped, true
}

// sameClipPrefix 比较两段外层裁剪路径是否结构完全一致
// 入参: a 第一组路径, b 第二组路径
// 返回: bool 是否一致
func sameClipPrefix(a, b []pdfgo.Path) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].EvenOdd != b[i].EvenOdd || len(a[i].Segments) != len(b[i].Segments) || len(a[i].Text) != len(b[i].Text) {
			return false
		}
		if len(a[i].Segments) > 0 && &a[i].Segments[0] == &b[i].Segments[0] && (len(a[i].Text) == 0 || &a[i].Text[0] == &b[i].Text[0]) {
			continue
		}
		for j := range a[i].Segments {
			if a[i].Segments[j].Operator != b[i].Segments[j].Operator || !slices.Equal(a[i].Segments[j].Points, b[i].Segments[j].Points) {
				return false
			}
		}
		if !slices.Equal(a[i].Text, b[i].Text) {
			return false
		}
	}
	return true
}

// pdfIdentityTransform 判断局部到页面矩阵在扣除边界偏移后是否为单位变换
// 入参: m 仿射矩阵, box 目标边界
// 返回: bool 是否可省略CTM属性
func pdfIdentityTransform(m pdfgo.Matrix, box Box) bool {
	return math.Abs(m[0]-1) <= 1e-9 && math.Abs(m[1]) <= 1e-9 && math.Abs(m[2]) <= 1e-9 && math.Abs(m[3]-1) <= 1e-9 && math.Abs(m[4]-box.X) <= 1e-9 && math.Abs(m[5]-box.Y) <= 1e-9
}

// templateCandidateKey 为固定位置复合对象构造模板提升匹配键
// 入参: ctx 取消上下文, unit 复合对象, area 页面区域, zOrder 目标模板层次
// 返回: pdfTemplateCandidateKey 模板签名, bool 是否允许提升, error 编码错误
func templateCandidateKey(ctx context.Context, unit CompositeObject, area PageArea, zOrder string) (pdfTemplateCandidateKey, bool, error) {
	if area.PhysicalBox == "" {
		return pdfTemplateCandidateKey{}, false, nil
	}
	alpha := -1
	if unit.Alpha != nil {
		alpha = *unit.Alpha
	}
	key := pdfTemplateCandidateKey{
		resourceID: unit.ResourceID,
		boundary:   unit.Boundary,
		ctm:        unit.CTM,
		pageArea:   area,
		zOrder:     zOrder,
		alpha:      alpha,
	}
	if unit.Clips != nil {
		data, err := encodeOFDXMLContext(ctx, func(x *ofdXML) {
			x.root("ClipsWrap", nil)
			x.clips(unit.Clips, unit.CTM)
			x.end("ClipsWrap")
		})
		if err != nil {
			return pdfTemplateCandidateKey{}, false, err
		}
		key.hasClips = true
		key.clipsDigest = sha256.Sum256(data)
	}
	return key, true, nil
}

// canonicalFormFloat 将毫米浮点坐标量化到1e-5精度，消除矩阵求逆产生的末位舍入噪声
// 入参: v 浮点数值
// 返回: string 规范化文本
func canonicalFormFloat(v float64) string {
	rounded := math.Round(v*1e5) / 1e5
	if rounded == 0 {
		rounded = 0
	}
	return strconv.FormatFloat(rounded, 'f', -1, 64)
}

// canonicalFormNumberValue 将浮点数值量化到1e-5精度
// 入参: v 原始数值
// 返回: float64 规范化数值
func canonicalFormNumberValue(v float64) float64 {
	rounded := math.Round(v*1e5) / 1e5
	if rounded == 0 {
		return 0
	}
	return rounded
}

// canonicalFormNumbers 量化空格分隔数值或路径指令串中的浮点数值，保留指令标记原样
// 入参: raw 原始属性串
// 返回: string 规范化属性串
func canonicalFormNumbers(raw string) string {
	if raw == "" {
		return ""
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	var out strings.Builder
	out.Grow(len(raw))
	for i, field := range fields {
		if i != 0 {
			out.WriteByte(' ')
		}
		if v, err := strconv.ParseFloat(field, 64); err == nil && finite(v) {
			out.WriteString(canonicalFormFloat(v))
		} else {
			out.WriteString(field)
		}
	}
	return out.String()
}

// canonicalFormClips 复制并量化裁剪集合中的几何数值，不修改原裁剪数据
// 入参: clips 原裁剪集合
// 返回: *Clips 规范化裁剪集合
func canonicalFormClips(clips *Clips) *Clips {
	if clips == nil {
		return nil
	}
	copyClips := *clips
	copyClips.Clip = make([]Clip, len(clips.Clip))
	for i, clip := range clips.Clip {
		copyClip := Clip{Area: make([]ClipArea, len(clip.Area))}
		for j, area := range clip.Area {
			copyArea := area
			copyArea.CTM = canonicalFormNumbers(area.CTM)
			if len(area.Path) != 0 {
				copyArea.Path = make([]PathObject, len(area.Path))
				for k, p := range area.Path {
					obj := canonicalFormObject(GraphicObject{Type: "Path", PathObject: p})
					copyArea.Path[k] = obj.PathObject
				}
			}
			if len(area.Text) != 0 {
				copyArea.Text = make([]TextObject, len(area.Text))
				for k, t := range area.Text {
					obj := canonicalFormObject(GraphicObject{Type: "Text", TextObject: t})
					copyArea.Text[k] = obj.TextObject
				}
			}
			copyClip.Area[j] = copyArea
		}
		copyClips.Clip[i] = copyClip
	}
	return &copyClips
}

// canonicalFormColor 复制并量化渐变及图案坐标数值，不修改原颜色对象
// 入参: color 原颜色对象
// 返回: *FillColor 规范化颜色对象
func canonicalFormColor(color *FillColor) *FillColor {
	if color == nil {
		return nil
	}
	copyColor := *color
	if color.AxialShd != nil {
		shd := *color.AxialShd
		shd.StartPoint = canonicalFormNumbers(shd.StartPoint)
		shd.EndPoint = canonicalFormNumbers(shd.EndPoint)
		shd.MapUnit = canonicalFormNumberValue(shd.MapUnit)
		copyColor.AxialShd = &shd
	}
	if color.RadialShd != nil {
		shd := *color.RadialShd
		shd.StartPoint = canonicalFormNumbers(shd.StartPoint)
		shd.EndPoint = canonicalFormNumbers(shd.EndPoint)
		shd.StartRadius = canonicalFormNumberValue(shd.StartRadius)
		shd.EndRadius = canonicalFormNumberValue(shd.EndRadius)
		shd.Angle = canonicalFormNumberValue(shd.Angle)
		shd.Eccentricity = canonicalFormNumberValue(shd.Eccentricity)
		shd.MapUnit = canonicalFormNumberValue(shd.MapUnit)
		copyColor.RadialShd = &shd
	}
	if color.GouraudShd != nil {
		shd := *color.GouraudShd
		shd.Point = slices.Clone(shd.Point)
		for i := range shd.Point {
			shd.Point[i].X = canonicalFormNumberValue(shd.Point[i].X)
			shd.Point[i].Y = canonicalFormNumberValue(shd.Point[i].Y)
		}
		copyColor.GouraudShd = &shd
	}
	if color.LaGouraudShd != nil {
		shd := *color.LaGouraudShd
		shd.Point = slices.Clone(shd.Point)
		for i := range shd.Point {
			shd.Point[i].X = canonicalFormNumberValue(shd.Point[i].X)
			shd.Point[i].Y = canonicalFormNumberValue(shd.Point[i].Y)
		}
		copyColor.LaGouraudShd = &shd
	}
	if color.Pattern != nil {
		pat := *color.Pattern
		pat.Width = canonicalFormNumberValue(pat.Width)
		pat.Height = canonicalFormNumberValue(pat.Height)
		pat.XStep = canonicalFormNumberValue(pat.XStep)
		pat.YStep = canonicalFormNumberValue(pat.YStep)
		pat.CTM = canonicalFormNumbers(pat.CTM)
		copyColor.Pattern = &pat
	}
	return &copyColor
}

// canonicalFormObject 生成用于去重哈希与比对的浮点规范化对象浅拷贝，不改动正文文字及原对象
// 入参: object 原始图形对象
// 返回: GraphicObject 规范化后的对象副本
func canonicalFormObject(object GraphicObject) GraphicObject {
	switch object.Type {
	case "PathObject", "Path":
		p := object.PathObject
		p.Boundary = canonicalFormNumbers(p.Boundary)
		p.CTM = canonicalFormNumbers(p.CTM)
		p.AbbreviatedData = canonicalFormNumbers(p.AbbreviatedData)
		p.LineWidth = canonicalFormNumberValue(p.LineWidth)
		p.MiterLimit = canonicalFormNumberValue(p.MiterLimit)
		p.DashPattern = canonicalFormNumbers(p.DashPattern)
		if p.DashOffset != nil {
			v := canonicalFormNumberValue(*p.DashOffset)
			p.DashOffset = &v
		}
		p.Clips = canonicalFormClips(p.Clips)
		p.FillColor = canonicalFormColor(p.FillColor)
		p.StrokeColor = (*StrokeColor)(canonicalFormColor((*FillColor)(p.StrokeColor)))
		object.PathObject = p
	case "TextObject", "Text":
		t := object.TextObject
		t.Boundary = canonicalFormNumbers(t.Boundary)
		t.CTM = canonicalFormNumbers(t.CTM)
		t.Size = canonicalFormNumberValue(t.Size)
		t.HScale = canonicalFormNumberValue(t.HScale)
		t.LineWidth = canonicalFormNumberValue(t.LineWidth)
		t.MiterLimit = canonicalFormNumberValue(t.MiterLimit)
		t.DashPattern = canonicalFormNumbers(t.DashPattern)
		if t.DashOffset != nil {
			v := canonicalFormNumberValue(*t.DashOffset)
			t.DashOffset = &v
		}
		if len(t.TextCode) != 0 {
			codes := make([]TextCode, len(t.TextCode))
			for i, code := range t.TextCode {
				code.X = canonicalFormNumbers(code.X)
				code.Y = canonicalFormNumbers(code.Y)
				code.DeltaX = canonicalFormNumbers(code.DeltaX)
				code.DeltaY = canonicalFormNumbers(code.DeltaY)
				codes[i] = code
			}
			t.TextCode = codes
		}
		t.Clips = canonicalFormClips(t.Clips)
		t.FillColor = canonicalFormColor(t.FillColor)
		t.StrokeColor = (*StrokeColor)(canonicalFormColor((*FillColor)(t.StrokeColor)))
		object.TextObject = t
	case "ImageObject":
		img := object.ImageObject
		img.Boundary = canonicalFormNumbers(img.Boundary)
		img.CTM = canonicalFormNumbers(img.CTM)
		img.Clips = canonicalFormClips(img.Clips)
		object.ImageObject = img
	case "CompositeObject":
		c := object.CompositeObject
		c.Boundary = canonicalFormNumbers(c.Boundary)
		c.CTM = canonicalFormNumbers(c.CTM)
		c.Clips = canonicalFormClips(c.Clips)
		object.CompositeObject = c
	}
	return object
}

// isolatedGroupContentDigest 计算独立透明组子图元局部几何与继承图形状态的确定性值摘要，遇不可摘要复杂对象返回false避免缓存碰撞
// 入参: nodes 剥离外层裁剪后的子图元切片, toLocal 页面到表单局部毫米坐标的变换矩阵, space 组混合颜色空间
// 返回: [32]byte 内容与状态特征摘要, bool 是否允许跨页缓存
func isolatedGroupContentDigest(nodes []pdfCompositeNode, toLocal pdfgo.Matrix, space *pdfgo.ColorSpace) ([32]byte, bool) {
	var zero [32]byte
	h := sha256.New()
	var buf [8]byte
	cacheable := true
	writeU64 := func(v uint64) {
		binary.LittleEndian.PutUint64(buf[:], v)
		h.Write(buf[:])
	}
	writeFloat := func(v float64) {
		q := math.Round(v*1e4) / 1e4
		if q == 0 {
			q = 0
		}
		writeU64(math.Float64bits(q))
	}
	writeBool := func(v bool) {
		if v {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
	}
	writeString := func(s string) {
		writeU64(uint64(len(s)))
		h.Write([]byte(s))
	}
	writeBytes := func(b []byte) {
		writeU64(uint64(len(b)))
		h.Write(b)
	}
	writeObject := func(obj pdfgo.Object) {
		if !cacheable {
			return
		}
		if !pdfgo.HashObject(h, obj) {
			cacheable = false
		}
	}
	writeMatrix := func(m pdfgo.Matrix) {
		for _, v := range m {
			writeFloat(v)
		}
	}
	writeColorSpace := func(cs *pdfgo.ColorSpace) {
		if !cacheable {
			return
		}
		writeBool(cs != nil)
		if cs == nil {
			return
		}
		digest, ok := cs.Digest()
		if !ok {
			cacheable = false
			return
		}
		h.Write(digest[:])
	}
	writeFont := func(font *pdfgo.Font) {
		if !cacheable {
			return
		}
		writeBool(font != nil)
		if font == nil {
			return
		}
		writeString(font.Name)
		writeString(string(font.Subtype))
		writeString(string(font.ProgramType))
		writeBool(font.Vertical)
		writeBytes(font.Program)
		writeObject(font.Dictionary)
	}
	writeGlyphs := func(glyphs []pdfgo.Glyph, positions []pdfgo.Point) {
		writeU64(uint64(len(glyphs)))
		for _, g := range glyphs {
			writeU64(uint64(g.Code))
			writeU64(uint64(g.CID))
			writeU64(uint64(g.ID))
			writeBool(g.HasID)
			writeString(g.Name)
			writeString(g.Text)
			writeFloat(g.Width)
			writeFloat(g.Vertical.Origin.X)
			writeFloat(g.Vertical.Origin.Y)
			writeFloat(g.Vertical.Advance)
			writeBool(g.WordSpace)
		}
		writeU64(uint64(len(positions)))
		for _, pos := range positions {
			writeFloat(pos.X)
			writeFloat(pos.Y)
		}
	}
	writeTextClip := func(tc *pdfgo.TextClip) {
		if !cacheable {
			return
		}
		writeBool(tc != nil)
		if tc == nil {
			return
		}
		writeFont(tc.Font)
		writeFloat(tc.Size)
		writeFloat(tc.HorizontalScale)
		writeMatrix(toLocal.Mul(tc.Matrix))
		writeGlyphs(tc.Glyphs, tc.Positions)
	}
	writeImage := func(img *pdfgo.Image) {
		if !cacheable {
			return
		}
		writeBool(img != nil)
		if img == nil {
			return
		}
		writeU64(uint64(img.Width))
		writeU64(uint64(img.Height))
		writeU64(uint64(img.BitsPerComponent))
		writeBool(img.ImageMask)
		writeBool(img.Interpolate)
		writeString(string(img.Intent))
		writeObject(img.ColorSpace)
		writeObject(img.Decode)
		writeObject(img.Mask)
		writeObject(img.SoftMask)
		writeObject(img.Stream)
	}
	writePath := func(path pdfgo.Path, mat pdfgo.Matrix) {
		if !cacheable {
			return
		}
		writeBool(path.EvenOdd)
		writeU64(uint64(len(path.Segments)))
		for _, seg := range path.Segments {
			writeString(seg.Operator)
			writeU64(uint64(len(seg.Points)))
			for _, pt := range seg.Points {
				p := mat.Apply(pt)
				writeFloat(p.X)
				writeFloat(p.Y)
			}
		}
		writeU64(uint64(len(path.Text)))
		for _, tc := range path.Text {
			writeTextClip(tc)
		}
	}
	writePaint := func(paint pdfgo.Paint) {
		if !cacheable {
			return
		}
		if paint.Process != nil || paint.Colorant != nil || paint.Axial != nil || paint.Radial != nil || paint.Function != nil || paint.Mesh != nil || paint.Tiling != nil || paint.Shading != nil {
			cacheable = false
			return
		}
		writeBool(paint.None)
		writeFloat(paint.Alpha)
		for _, v := range paint.RGB {
			writeFloat(v)
		}
		writeBool(paint.CMYK != nil)
		if paint.CMYK != nil {
			for _, v := range *paint.CMYK {
				writeFloat(v)
			}
		}
		writeColorSpace(paint.Space)
		writeString(string(paint.SourceSpace))
		writeU64(uint64(len(paint.Values)))
		for _, v := range paint.Values {
			writeFloat(v)
		}
	}
	writeStyle := func(st pdfgo.Style) {
		if !cacheable {
			return
		}
		if st.SoftMask != nil || st.Halftone != nil || st.Transfer != nil {
			cacheable = false
			return
		}
		writePaint(st.Fill)
		writePaint(st.Stroke)
		writeFloat(st.LineWidth)
		writeU64(uint64(st.Cap))
		writeU64(uint64(st.Join))
		writeFloat(st.MiterLimit)
		writeU64(uint64(len(st.Dash)))
		for _, d := range st.Dash {
			writeFloat(d)
		}
		writeFloat(st.DashPhase)
		writeBool(st.StrokeAdjust)
		writeBool(st.FillOverprint)
		writeBool(st.StrokeOverprint)
		writeU64(uint64(st.OverprintMode))
		writeString(string(st.RenderingIntent))
		writeString(string(st.BlendMode))
		writeBool(st.AlphaIsShape)
		writeBool(st.Smoothness != nil)
		if st.Smoothness != nil {
			writeFloat(*st.Smoothness)
		}
		writeBool(st.Antialias != nil)
		if st.Antialias != nil {
			writeBool(*st.Antialias)
		}
		writeU64(uint64(len(st.Clips)))
		for _, clip := range st.Clips {
			writePath(clip, toLocal)
		}
	}
	writeTextMark := func(tm *pdfgo.TextMark) {
		if !cacheable {
			return
		}
		writeFont(tm.Font)
		writeU64(uint64(tm.Mode))
		writeFloat(tm.Size)
		writeFloat(tm.HorizontalScale)
		writeMatrix(toLocal.Mul(tm.Matrix))
		writeBool(tm.StrokeMatrix != (pdfgo.Matrix{}))
		if tm.StrokeMatrix != (pdfgo.Matrix{}) {
			writeMatrix(toLocal.Mul(tm.StrokeMatrix))
		}
		writeStyle(tm.Style)
		writeGlyphs(tm.Glyphs, tm.Positions)
		writeTextClip(tm.Clip)
	}
	var hashNodes func(h hash.Hash, list []pdfCompositeNode)
	hashNodes = func(h hash.Hash, list []pdfCompositeNode) {
		writeU64(uint64(len(list)))
		for _, node := range list {
			if !cacheable {
				return
			}
			if node.path != nil {
				h.Write([]byte{'P'})
				writeBool(node.path.Fill)
				writeBool(node.path.Stroke)
				writeBool(node.path.StrokeMatrix != (pdfgo.Matrix{}))
				if node.path.StrokeMatrix != (pdfgo.Matrix{}) {
					writeMatrix(toLocal.Mul(node.path.StrokeMatrix))
				}
				writeStyle(node.path.Style)
				writePath(node.path.Path, toLocal)
			}
			if node.text != nil {
				h.Write([]byte{'T'})
				writeTextMark(node.text)
			}
			if node.image != nil {
				h.Write([]byte{'I'})
				writeImage(node.image.Image)
				writeMatrix(toLocal.Mul(node.image.Matrix))
				writeStyle(node.image.Style)
			}
			if node.glyph != nil {
				h.Write([]byte{'L'})
				writeTextMark(node.glyph)
			}
			if node.group != nil {
				if node.group.SoftMask != nil {
					cacheable = false
					return
				}
				h.Write([]byte{'G'})
				writeFloat(node.group.Alpha)
				writeBool(node.group.Isolated)
				writeBool(node.group.Knockout)
				writeString(string(node.group.BlendMode))
				writeColorSpace(node.group.ColorSpace)
			}
			if len(node.children) != 0 {
				h.Write([]byte{'C'})
				hashNodes(h, node.children)
			}
		}
	}
	writeColorSpace(space)
	hashNodes(h, nodes)
	if !cacheable {
		return zero, false
	}
	var sum [32]byte
	h.Sum(sum[:0])
	return sum, true
}

// encode 按原顺序编码对象分块并规范化浮点舍入噪声，不为缓存检查整理连续对象数组
// 入参: ctx 取消上下文, objects 表单对象, width 资源宽度, height 资源高度
// 返回: []byte 下次编码或重置前有效的内容, error 编码或取消错误
func (c *pdfFormCache) encode(ctx context.Context, objects *pdfObjectBuffer, width, height float64) ([]byte, error) {
	c.buffer.Reset()
	x := newOFDXML(&c.buffer)
	x.ctx = ctx
	x.root("Content", ofdAttrs{{Name: xml.Name{Local: "Width"}, Value: canonicalFormFloat(width)},
		{Name: xml.Name{Local: "Height"}, Value: canonicalFormFloat(height)}})
	for _, part := range objects.parts {
		for _, object := range part {
			x.object(canonicalFormObject(object), false)
			if x.err != nil {
				return nil, x.err
			}
		}
	}
	x.end("Content")
	if err := x.finish(); err != nil {
		return nil, err
	}
	return c.buffer.Bytes(), nil
}

// encodeSlice 编码连续对象切片并规范化浮点舍入噪声，用于跨工作协程合并后的表单去重校验
// 入参: ctx 取消上下文, objects 对象切片, width 资源宽度, height 资源高度
// 返回: []byte 下次编码或重置前有效的内容, error 编码或取消错误
func (c *pdfFormCache) encodeSlice(ctx context.Context, objects []GraphicObject, width, height float64) ([]byte, error) {
	c.buffer.Reset()
	x := newOFDXML(&c.buffer)
	x.ctx = ctx
	x.root("Content", ofdAttrs{{Name: xml.Name{Local: "Width"}, Value: canonicalFormFloat(width)},
		{Name: xml.Name{Local: "Height"}, Value: canonicalFormFloat(height)}})
	for _, object := range objects {
		x.object(canonicalFormObject(object), false)
		if x.err != nil {
			return nil, x.err
		}
	}
	x.end("Content")
	if err := x.finish(); err != nil {
		return nil, err
	}
	return c.buffer.Bytes(), nil
}

// reset 清空临时内容，释放超限缓冲，不修改已登记资源
func (c *pdfFormCache) reset() {
	if c.buffer.Cap() > pdfFormBufferLimit {
		c.buffer = bytes.Buffer{}
	} else {
		c.buffer.Reset()
	}
}

// formUseCount 汇总表单的跨页与当前页引用总频次
// 入参: ref 表单间接引用, stream 表单流指针
// 返回: int 估算总引用次数
func (p *pdfImporter) formUseCount(ref pdfgo.Reference, stream *pdfgo.Stream) int {
	total := 0
	if !p.hasReferenceDocuments() && ref != (pdfgo.Reference{}) {
		if p.formRefCounts != nil {
			total = p.formRefCounts[ref]
		}
		if total < 2 && p.seenFormPages != nil {
			if first, ok := p.seenFormPages[ref]; ok && first != p.page {
				total = 2
			}
		}
		if total < 2 && p.neighborFormRefs[ref] {
			total = 2
		}
	}
	if p.localFormCounts != nil {
		local := p.localFormCounts[p.formIdentity(ref, stream)]
		if local > total {
			total = local
		} else if local > 1 {
			total += local - 1
		}
	}
	return total
}

// recordPageForms 收集当前页遇到的表单间接引用，供单页提交后登记渐进跨页复用
// 入参: counts 当前页表单频次映射
func (p *pdfImporter) recordPageForms(counts map[pdfFormIdentity]int) {
	if len(counts) == 0 {
		return
	}
	for id := range counts {
		if id.ref == (pdfgo.Reference{}) || id.stream != nil {
			continue
		}
		if p.pageSeenForms == nil {
			p.pageSeenForms = make(map[pdfgo.Reference]bool, len(counts))
		}
		p.pageSeenForms[id.ref] = true
	}
}

// commitSeenForms 在单页转换提交成功后登记本页出现的表单引用
// 入参: pageIndex 已提交页面索引
func (p *pdfImporter) commitSeenForms(pageIndex int) {
	if len(p.pageSeenForms) == 0 {
		return
	}
	if p.seenFormPages == nil {
		p.seenFormPages = make(map[pdfgo.Reference]int, len(p.pageSeenForms))
	}
	for ref := range p.pageSeenForms {
		if _, exists := p.seenFormPages[ref]; !exists {
			p.seenFormPages[ref] = pageIndex
		}
	}
	p.pageSeenForms = nil
}

// shouldReuseForm 判断普通表单是否满足自动复用条件
// 入参: mark 表单元信息, count 展开图元数量
// 返回: bool 是否构建共享复合图形资源
func (p *pdfImporter) shouldReuseForm(mark *pdfgo.FormMark, count int) bool {
	if mark != nil && p.formUseCount(mark.Ref, mark.Stream) >= 2 {
		return true
	}
	return count >= 16
}

// shouldReuseGroupForm 判断透明组表单是否满足自动复用条件
// 入参: mark 透明组元信息, childCount 组内图元数量
// 返回: bool 是否尝试共享复合图形或光栅资源
func (p *pdfImporter) shouldReuseGroupForm(mark *pdfgo.GroupMark, childCount int) bool {
	if mark == nil || childCount == 0 {
		return false
	}
	if p.formUseCount(mark.Ref, mark.Stream) >= 2 {
		return true
	}
	space := mark.ColorSpace
	if space == nil {
		space = p.compositeSpace
	}
	return childCount >= 16 && !mark.Knockout && !mark.AlphaIsShape && codecpdf.NormalBlend(mark.BlendMode) && mark.SoftMask == nil && (space == nil || space.SRGBEquivalent()) && (mark.Alpha == 1 || mark.Isolated)
}

// probeNeighborFormRefs 在单页首屏转换时探测邻页XObject字典，提前识别跨页小表单
// 入参: local 当前页表单引用频次
// 返回: map[pdfgo.Reference]bool 邻页复用的表单引用集合
func (p *pdfImporter) probeNeighborFormRefs(local map[pdfFormIdentity]int) map[pdfgo.Reference]bool {
	if p.reader == nil || p.formRefCounts != nil || len(p.pageOrder) < 2 || len(local) == 0 || p.ctx.Err() != nil {
		return nil
	}
	var targets map[pdfgo.Reference]bool
	for id, count := range local {
		if id.ref == (pdfgo.Reference{}) || count >= 2 {
			continue
		}
		if p.seenFormPages != nil {
			if first, ok := p.seenFormPages[id.ref]; ok && first != p.page {
				continue
			}
		}
		if targets == nil {
			targets = make(map[pdfgo.Reference]bool)
		}
		targets[id.ref] = true
	}
	if len(targets) == 0 {
		return nil
	}
	var neighbors [2]int
	neighborCount := 0
	addNeighbor := func(idx int) {
		if neighborCount < len(neighbors) && idx >= 0 && idx < len(p.pageOrder) && idx != p.page {
			neighbors[neighborCount] = idx
			neighborCount++
		}
	}
	addNeighbor(p.page + 1)
	addNeighbor(p.page + 2)
	addNeighbor(p.page - 1)
	addNeighbor(p.page - 2)
	var found map[pdfgo.Reference]bool
	for _, idx := range neighbors[:neighborCount] {
		if p.ctx.Err() != nil {
			return nil
		}
		res := p.pageOrder[idx].Resources
		if res == nil || res["XObject"] == nil {
			continue
		}
		xobjVal, err := p.reader.Resolve(res["XObject"])
		if err != nil {
			continue
		}
		xobjs, ok := xobjVal.(pdfgo.Dictionary)
		if !ok {
			continue
		}
		for _, entry := range xobjs {
			ref, ok := entry.(pdfgo.Reference)
			if !ok || !targets[ref] {
				continue
			}
			if found == nil {
				found = make(map[pdfgo.Reference]bool)
			}
			found[ref] = true
			delete(targets, ref)
			if len(targets) == 0 {
				return found
			}
		}
	}
	return found
}

// scanFormRefCounts 轻量扫描各页资源字典，统计跨页共享表单的间接引用频次
// 入参: ctx 取消上下文
// 返回: map[pdfgo.Reference]int 表单引用出现页数及嵌套计数
func (p *pdfImporter) scanFormRefCounts(ctx context.Context) map[pdfgo.Reference]int {
	if p.reader == nil || len(p.pageOrder) == 0 || ctx.Err() != nil {
		return nil
	}
	counts := make(map[pdfgo.Reference]int)
	var scanDict func(dict pdfgo.Dictionary, pageSeen map[pdfgo.Reference]bool, depth int)
	scanDict = func(dict pdfgo.Dictionary, pageSeen map[pdfgo.Reference]bool, depth int) {
		if dict == nil || depth > 8 || ctx.Err() != nil {
			return
		}
		xobjVal, err := p.reader.Resolve(dict["XObject"])
		if err != nil {
			return
		}
		xobjs, ok := xobjVal.(pdfgo.Dictionary)
		if !ok {
			return
		}
		for _, entry := range xobjs {
			ref, ok := entry.(pdfgo.Reference)
			if !ok || pageSeen[ref] {
				continue
			}
			resolved, err := p.reader.Resolve(ref)
			if err != nil {
				continue
			}
			stream, ok := resolved.(*pdfgo.Stream)
			if !ok || stream == nil {
				continue
			}
			subtype, err := p.reader.Resolve(stream.Dictionary["Subtype"])
			if err != nil || subtype != pdfgo.Name("Form") {
				continue
			}
			pageSeen[ref] = true
			counts[ref]++
			if resVal, err := p.reader.Resolve(stream.Dictionary["Resources"]); err == nil {
				if resDict, ok := resVal.(pdfgo.Dictionary); ok {
					scanDict(resDict, pageSeen, depth+1)
				}
			}
		}
	}
	for _, page := range p.pageOrder {
		if ctx.Err() != nil {
			return nil
		}
		scanDict(page.Resources, make(map[pdfgo.Reference]bool), 0)
	}
	if ctx.Err() != nil {
		return nil
	}
	return counts
}

// compositeForm 优先按表单局部坐标构建共享矢量资源，异常矩阵时回退页面坐标复用
// 入参: mark 表单元信息, nodes 普通表单连续图元
// 返回: bool 是否已保存为复合对象, error 检查或转换错误
func (p *pdfImporter) compositeForm(mark *pdfgo.FormMark, nodes []pdfCompositeNode) (bool, error) {
	if mark != nil {
		packed, err := p.compositeFormLocal(*mark, nodes, nil)
		if err != nil || packed {
			return packed, err
		}
	}
	if len(nodes) < 16 {
		return false, nil
	}
	return p.compositeFormPage(nodes)
}

// compositeGroupForm 复用独立透明组表单，支持矢量复合图元共享与孤立透明组光栅复用
// 入参: node 透明组节点
// 返回: bool 是否已完成复用转换, error 转换错误
func (p *pdfImporter) compositeGroupForm(node pdfCompositeNode) (bool, error) {
	g := node.group
	if g == nil || g.Page || len(node.children) == 0 {
		return false, nil
	}
	space := g.ColorSpace
	if space == nil {
		space = p.compositeSpace
	}
	srgbSpace := space == nil || space.SRGBEquivalent()
	if !g.Knockout && !g.AlphaIsShape && codecpdf.NormalBlend(g.BlendMode) && g.SoftMask == nil && srgbSpace && (g.Alpha == 1 || g.Isolated) {
		var alphaPtr *int
		if g.Alpha < 1 {
			alpha := int(math.Round(g.Alpha * 255))
			alphaPtr = &alpha
		}
		mark := pdfgo.FormMark{Ref: g.Ref, Stream: g.Stream, Bounds: g.Bounds, Matrix: g.Matrix, OuterClips: g.OuterClips, Group: g}
		packed, err := p.compositeFormLocal(mark, node.children, alphaPtr)
		if err != nil || packed {
			return packed, err
		}
	}
	if g.Isolated && !g.AlphaIsShape && codecpdf.NormalBlend(g.BlendMode) && g.SoftMask == nil && (p.compositeSpace == nil || p.compositeSpace.SRGBEquivalent()) && p.warning != nil && p.formUseCount(g.Ref, g.Stream) >= 2 {
		return p.compositeIsolatedGroupRaster(node)
	}
	return false, nil
}

// compositeFormLocal 以表单局部毫米坐标构建复合图形资源，各实例通过Boundary与CTM独立定位
// 入参: mark 表单元信息, nodes 表单内图元, groupAlpha 可选透明组不透明度
// 返回: bool 是否已保存为复合对象, error 检查或转换错误
func (p *pdfImporter) compositeFormLocal(mark pdfgo.FormMark, nodes []pdfCompositeNode, groupAlpha *int) (bool, error) {
	bw, bh := mark.Bounds.XMax-mark.Bounds.XMin, mark.Bounds.YMax-mark.Bounds.YMin
	if !finite(bw) || !finite(bh) || bw <= 0 || bh <= 0 {
		return false, nil
	}
	mInv, ok := mark.Matrix.Inverse()
	if !ok {
		return false, nil
	}
	uWidth, uHeight := bw*pdfFormPointsToMM, bh*pdfFormPointsToMM
	if !finite(uWidth) || !finite(uHeight) || uWidth <= 0 || uHeight <= 0 {
		return false, nil
	}
	localMat := pdfgo.Matrix{pdfFormPointsToMM, 0, 0, -pdfFormPointsToMM, -mark.Bounds.XMin * pdfFormPointsToMM, mark.Bounds.YMax * pdfFormPointsToMM}
	localInv := pdfgo.Matrix{1 / pdfFormPointsToMM, 0, 0, -1 / pdfFormPointsToMM, mark.Bounds.XMin, mark.Bounds.YMax}
	formToLocal := localMat.Mul(mInv)
	localToPage := p.matrix.Mul(mark.Matrix).Mul(localInv)
	for _, value := range formToLocal {
		if !finite(value) {
			return false, nil
		}
	}
	for _, value := range localToPage {
		if !finite(value) {
			return false, nil
		}
	}
	instBox := pdfBounds([]pdfgo.Point{
		localToPage.Apply(pdfgo.Point{}),
		localToPage.Apply(pdfgo.Point{X: uWidth}),
		localToPage.Apply(pdfgo.Point{Y: uHeight}),
		localToPage.Apply(pdfgo.Point{X: uWidth, Y: uHeight}),
	})
	if !finite(instBox.X) || !finite(instBox.Y) || !finite(instBox.W) || !finite(instBox.H) || instBox.W <= 0 || instBox.H <= 0 {
		return false, nil
	}
	sequence, same, err := p.formSequence(nodes)
	if err != nil || !same {
		return false, err
	}
	if found, err := p.compositeHasTransfer(nodes, p.transferModel); err != nil || found {
		return false, err
	}
	for _, node := range nodes {
		if err := p.ctx.Err(); err != nil {
			return false, err
		}
		direct, err := p.directCompositeNode(node, p.compositeSpace)
		if err != nil || !direct {
			return false, err
		}
	}
	outerClips, stripped, stripOK := stripFormOuterClips(nodes, mark.OuterClips)
	if !stripOK {
		return false, nil
	}
	savedObjects := p.objects
	savedMatrix, savedW, savedH := p.matrix, p.pageWidth, p.pageHeight
	savedClipTexts := p.clipTexts
	savedScaleBoost := p.imageScaleBoost
	p.objects = nil
	p.matrix, p.pageWidth, p.pageHeight = formToLocal, uWidth, uHeight
	p.clipTexts = make(map[*pdfgo.TextClip]pdfClipTextEntry)
	if scale := localToPage.MaxScale(); finite(scale) && scale > 1 {
		p.imageScaleBoost = max(savedScaleBoost, scale)
	}
	restore := func() {
		p.objects = savedObjects
		p.matrix, p.pageWidth, p.pageHeight = savedMatrix, savedW, savedH
		p.clipTexts = savedClipTexts
		p.imageScaleBoost = savedScaleBoost
	}
	defer restore()
	visitor := p.visitor()
	for _, node := range stripped {
		if err := node.emit(visitor); err != nil {
			return false, err
		}
	}
	if err := p.ctx.Err(); err != nil {
		return false, err
	}
	if p.objects.len() == 0 {
		return true, nil
	}
	id, err := p.registerFormResource(p.objects, uWidth, uHeight)
	if err != nil {
		return false, err
	}
	restore()
	clips, err := p.clips(outerClips, instBox)
	if err != nil {
		return false, err
	}
	ctm := ""
	if !pdfIdentityTransform(localToPage, instBox) {
		ctm = pdfNumbers(localToPage[0], localToPage[1], localToPage[2], localToPage[3], localToPage[4]-instBox.X, localToPage[5]-instBox.Y)
	}
	unit := CompositeObject{
		Boundary:   pdfBoundary(instBox),
		CTM:        ctm,
		ResourceID: id,
		Alpha:      groupAlpha,
		Clips:      clips,
	}
	p.appendSequenceObject(GraphicObject{Type: "CompositeObject", CompositeObject: unit}, sequence)
	savedObjects = p.objects
	return true, nil
}

// compositeFormPage 在表单矩阵不可逆时沿用页面坐标打包大型矢量表单
// 入参: nodes 普通表单连续图元
// 返回: bool 是否已保存为复合对象, error 检查或转换错误
func (p *pdfImporter) compositeFormPage(nodes []pdfCompositeNode) (bool, error) {
	sequence, same, err := p.formSequence(nodes)
	if err != nil || !same {
		return false, err
	}
	if found, err := p.compositeHasTransfer(nodes, p.transferModel); err != nil || found {
		return false, err
	}
	for _, node := range nodes {
		if err := p.ctx.Err(); err != nil {
			return false, err
		}
		direct, err := p.directCompositeNode(node, p.compositeSpace)
		if err != nil || !direct {
			return false, err
		}
	}
	parent := p.objects
	p.objects = nil
	defer func() { p.objects = parent }()
	visitor := p.visitor()
	for _, node := range nodes {
		if err := node.emit(visitor); err != nil {
			return false, err
		}
	}
	if err := p.ctx.Err(); err != nil {
		return false, err
	}
	if p.objects.len() == 0 {
		return true, nil
	}
	id, err := p.registerFormResource(p.objects, p.pageWidth, p.pageHeight)
	if err != nil {
		return false, err
	}
	p.objects = parent
	p.appendSequenceObject(GraphicObject{Type: "CompositeObject", CompositeObject: CompositeObject{
		Boundary: pdfBoundary(Box{W: p.pageWidth, H: p.pageHeight}), ResourceID: id,
	}}, sequence)
	parent = p.objects
	return true, nil
}

// registerFormResource 按局部内容摘要去重注册复合图形资源，并保存原始对象供多核合并重映射
// 入参: objects 表单对象缓冲, width 资源宽度, height 资源高度
// 返回: string 资源标识, error 编码或注册错误
func (p *pdfImporter) registerFormResource(objects *pdfObjectBuffer, width, height float64) (string, error) {
	if p.formResources == nil || p.formResources.editor != p.editor {
		p.formResources = &pdfFormCache{editor: p.editor, entries: *resourceCache[[32]byte, pdfFormResource](p.resources, 32<<20)}
	}
	cache := p.formResources
	defer cache.reset()
	content, err := cache.encode(p.ctx, objects, width, height)
	if err != nil {
		return "", err
	}
	key := sha256.Sum256(content)
	resource, found := cache.entries.get(key)
	if !found || !bytes.Equal(resource.content, content) {
		slice, err := objects.data(p.ctx)
		if err != nil {
			return "", err
		}
		var saved []GraphicObject
		if p.recordForms {
			saved = cloneEditorData(slice)
		}
		id, err := p.objectValidation().addOwnedVector(p.ctx, slice, width, height)
		if err != nil {
			return "", err
		}
		if p.recordForms {
			if p.formRecords == nil {
				p.formRecords = make(map[string]pdfFormRecord)
			}
			p.formRecords[id] = pdfFormRecord{objects: saved, width: width, height: height}
		}
		resource = pdfFormResource{id: id}
		if len(content) <= cache.entries.limit-len(id)-128 {
			resource.content = bytes.Clone(content)
			cache.entries.put(key, resource, cap(resource.content)+len(id)+128)
		}
	}
	return resource.id, nil
}

// compositeIsolatedGroupRaster 将无法直接矢量化的独立透明组表单在局部坐标系光栅化为共享图像资源
// 入参: node 独立透明组节点
// 返回: bool 是否已完成共享光栅化, error 合成或编码错误
func (p *pdfImporter) compositeIsolatedGroupRaster(node pdfCompositeNode) (bool, error) {
	g := node.group
	bw, bh := g.Bounds.XMax-g.Bounds.XMin, g.Bounds.YMax-g.Bounds.YMin
	if !finite(bw) || !finite(bh) || bw <= 0 || bh <= 0 {
		return false, nil
	}
	mInv, ok := g.Matrix.Inverse()
	if !ok {
		return false, nil
	}
	uWidth, uHeight := bw*pdfFormPointsToMM, bh*pdfFormPointsToMM
	if !finite(uWidth) || !finite(uHeight) || uWidth <= 0 || uHeight <= 0 {
		return false, nil
	}
	localMat := pdfgo.Matrix{pdfFormPointsToMM, 0, 0, -pdfFormPointsToMM, -g.Bounds.XMin * pdfFormPointsToMM, g.Bounds.YMax * pdfFormPointsToMM}
	localInv := pdfgo.Matrix{1 / pdfFormPointsToMM, 0, 0, -1 / pdfFormPointsToMM, g.Bounds.XMin, g.Bounds.YMax}
	formToLocal := localMat.Mul(mInv)
	localToPage := p.matrix.Mul(g.Matrix).Mul(localInv)
	for _, value := range formToLocal {
		if !finite(value) {
			return false, nil
		}
	}
	for _, value := range localToPage {
		if !finite(value) {
			return false, nil
		}
	}
	sequence, same, err := p.formSequence(node.children)
	if err != nil || !same {
		return false, err
	}
	device := p.transferModel
	if device == "" {
		device = "DeviceRGB"
	}
	if found, err := p.compositeHasTransfer([]pdfCompositeNode{node}, device); err != nil || found {
		return false, err
	}
	outerClips, strippedChildren, stripOK := stripFormOuterClips(node.children, g.OuterClips)
	if !stripOK {
		return false, nil
	}
	scale := max(1.0, localToPage.MaxScale())
	if !finite(scale) || scale > 16 {
		return false, nil
	}
	effectiveDPI := math.Round(p.rasterDPI * scale)
	if effectiveDPI <= 0 {
		return false, nil
	}
	w, h, err := (&RasterPage{Width: uWidth, Height: uHeight, DPI: effectiveDPI}).PixelSize()
	if err != nil || w > 4096 || h > 4096 || int64(w)*int64(h) > 16<<20 {
		return false, nil
	}
	space := p.compositeSpace
	if g.ColorSpace != nil {
		space = g.ColorSpace
	}
	spaceName := ""
	if space != nil {
		spaceName = string(space.Model)
	}
	digest, cacheable := isolatedGroupContentDigest(strippedChildren, formToLocal, space)
	cacheKey := pdfIsolatedGroupKey{
		ref:           g.Ref,
		stream:        g.Stream,
		bounds:        g.Bounds,
		dpi:           int(effectiveDPI),
		alpha:         int(math.Round(g.Alpha * 255)),
		knockout:      g.Knockout,
		space:         spaceName,
		contentDigest: digest,
	}
	if !p.hasReferenceDocuments() && g.Ref != (pdfgo.Reference{}) {
		cacheKey.stream = nil
	}
	var cached pdfIsolatedGroupImage
	found := false
	if cacheable {
		cached, found = p.isolatedGroupImages[cacheKey]
	}
	if !found {
		localGroup := *g
		localNode := pdfCompositeNode{group: &localGroup, children: strippedChildren}
		savedObjects := p.objects
		savedMatrix, savedW, savedH, savedDPI := p.matrix, p.pageWidth, p.pageHeight, p.rasterDPI
		savedCache := p.compositeCache
		savedClipTexts := p.clipTexts
		savedImageObjects := p.report.ImageObjects
		p.objects = nil
		p.matrix, p.pageWidth, p.pageHeight, p.rasterDPI = formToLocal, uWidth, uHeight, effectiveDPI
		p.compositeCache = nil
		p.clipTexts = make(map[*pdfgo.TextClip]pdfClipTextEntry)
		err = p.compositeRegion(nil, localNode, space, false)
		generated := p.objects
		p.objects = savedObjects
		p.matrix, p.pageWidth, p.pageHeight, p.rasterDPI = savedMatrix, savedW, savedH, savedDPI
		p.compositeCache = savedCache
		p.clipTexts = savedClipTexts
		p.report.ImageObjects = savedImageObjects
		if err != nil {
			return false, err
		}
		if generated.len() != 1 {
			return generated.len() == 0, nil
		}
		objs, err := generated.data(p.ctx)
		if err != nil {
			return false, err
		}
		if objs[0].Type != "ImageObject" {
			return false, nil
		}
		localBox, err := ParseBox(objs[0].ImageObject.Boundary)
		if err != nil || localBox.W <= 0 || localBox.H <= 0 {
			return false, nil
		}
		cached = pdfIsolatedGroupImage{id: objs[0].ImageObject.ResourceID, localBox: localBox}
		if cacheable {
			if p.isolatedGroupImages == nil {
				p.isolatedGroupImages = make(map[pdfIsolatedGroupKey]pdfIsolatedGroupImage)
			}
			p.isolatedGroupImages[cacheKey] = cached
		}
	}
	boxMatrix := localToPage.Mul(pdfgo.Matrix{cached.localBox.W, 0, 0, cached.localBox.H, cached.localBox.X, cached.localBox.Y})
	instBox := pdfBounds([]pdfgo.Point{
		boxMatrix.Apply(pdfgo.Point{}),
		boxMatrix.Apply(pdfgo.Point{X: 1}),
		boxMatrix.Apply(pdfgo.Point{Y: 1}),
		boxMatrix.Apply(pdfgo.Point{X: 1, Y: 1}),
	})
	if !finite(instBox.X) || !finite(instBox.Y) || !finite(instBox.W) || !finite(instBox.H) || instBox.W <= 0 || instBox.H <= 0 {
		return false, nil
	}
	clips, err := p.clips(outerClips, instBox)
	if err != nil {
		return false, err
	}
	alpha := 255
	object := ImageObject{
		Boundary:   pdfBoundary(instBox),
		ResourceID: cached.id,
		CTM:        pdfNumbers(boxMatrix[0], boxMatrix[1], boxMatrix[2], boxMatrix[3], boxMatrix[4]-instBox.X, boxMatrix[5]-instBox.Y),
		Alpha:      &alpha,
		Clips:      clips,
	}
	p.appendSequenceObject(GraphicObject{Type: "ImageObject", ImageObject: object}, sequence)
	p.report.ImageObjects++
	return true, nil
}

// formSequence 检查表单图元的结构区段一致性，放开无逻辑结构绑定的普通标记内容限制
// 入参: nodes 表单图元
// 返回: *pdfgo.ContentSequence 来源区段, bool 是否可合并, error 取消错误
func (p *pdfImporter) formSequence(nodes []pdfCompositeNode) (*pdfgo.ContentSequence, bool, error) {
	var sequence *pdfgo.ContentSequence
	seen := false
	hasStructure := p.structureBindings != nil && len(p.structureBindings.marked) != 0
	var visit func([]pdfCompositeNode) bool
	visit = func(nodes []pdfCompositeNode) bool {
		for _, node := range nodes {
			if p.ctx.Err() != nil {
				return false
			}
			var current *pdfgo.ContentSequence
			switch {
			case node.path != nil:
				current = node.path.Sequence
			case node.text != nil:
				origin := p.textOrigin(*node.text)
				if len(origin.related) != 0 {
					return false
				}
				current = origin.sequence
			case node.image != nil:
				current = node.image.Sequence
			case node.glyph != nil:
				current = node.glyph.Sequence
			default:
				if !visit(node.children) {
					return false
				}
				continue
			}
			effective := p.effectiveStructureSequence(current, hasStructure)
			if effective != nil || seen && sequence != nil {
				if seen && sequence != effective {
					return false
				}
				sequence, seen = effective, true
			} else if !seen {
				seen = true
			}
			if !visit(node.children) {
				return false
			}
		}
		return true
	}
	same := visit(nodes)
	return sequence, same, p.ctx.Err()
}

// effectiveStructureSequence 返回对逻辑结构树生效的标记区段，无结构绑定时返回nil
// 入参: sequence 当前区段, hasStructure 文档是否存在结构树绑定
// 返回: *pdfgo.ContentSequence 生效结构区段
func (p *pdfImporter) effectiveStructureSequence(sequence *pdfgo.ContentSequence, hasStructure bool) *pdfgo.ContentSequence {
	if sequence == nil {
		return nil
	}
	if sequence.Object != (pdfgo.Reference{}) {
		return sequence
	}
	if !hasStructure {
		return nil
	}
	for current := sequence; current != nil; current = current.Parent {
		if current.Object != (pdfgo.Reference{}) {
			return sequence
		}
		if key, ok, err := pdfStructureContentKey(current); err == nil && ok && p.structureBindings.marked[key] {
			return sequence
		}
	}
	return nil
}

// promoteTemplatePages 将多页固定位置处于底层或顶层且无穿插遮挡的高频表单自动提升为OFD模板页
// 入参: ctx 取消上下文
// 返回: error 模板页编码或注册错误
func (p *pdfImporter) promoteTemplatePages(ctx context.Context) error {
	if len(p.editor.pages) < 2 {
		return nil
	}
	boundIDs := p.structureBoundObjectIDs()
	promoteEdge := func(zOrder string, head bool) error {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			groups := make(map[pdfTemplateCandidateKey][]int)
			var order []pdfTemplateCandidateKey
			for i := range p.editor.pages {
				page := &p.editor.pages[i]
				if len(page.Content.Layer) == 0 {
					continue
				}
				objs := page.Content.Layer[0].Objects
				if len(objs) == 0 {
					continue
				}
				pos := len(objs) - 1
				if head {
					pos = 0
				}
				obj := objs[pos]
				if obj.Type != "CompositeObject" {
					continue
				}
				unit := obj.CompositeObject
				if unit.ResourceID == "" || len(unit.Actions) != 0 || boundIDs[unit.ID] {
					continue
				}
				key, ok, err := templateCandidateKey(ctx, unit, page.Area, zOrder)
				if err != nil {
					return err
				}
				if !ok {
					continue
				}
				if len(groups[key]) == 0 {
					order = append(order, key)
				}
				groups[key] = append(groups[key], i)
			}
			promoted := false
			for _, key := range order {
				pages := groups[key]
				if len(pages) < 2 {
					continue
				}
				samplePage := &p.editor.pages[pages[0]]
				sampleObjs := samplePage.Content.Layer[0].Objects
				pos := len(sampleObjs) - 1
				if head {
					pos = 0
				}
				sampleObj := cloneEditorData(sampleObjs[pos])
				sampleObj.CompositeObject.ID = ""
				tplID, err := p.editor.addOwnedTemplatePage(ctx, samplePage.Area, []GraphicObject{sampleObj}, zOrder, "")
				if err != nil {
					return err
				}
				for _, pageIndex := range pages {
					page := &p.editor.pages[pageIndex]
					objs := page.Content.Layer[0].Objects
					if head {
						page.Content.Layer[0].Objects = objs[1:]
						page.Template = append(page.Template, Template{TemplateID: tplID, ZOrder: zOrder})
					} else {
						page.Content.Layer[0].Objects = objs[:len(objs)-1]
						insertAt := len(page.Template)
						for idx, tpl := range page.Template {
							if tpl.ZOrder == zOrder {
								insertAt = idx
								break
							}
						}
						page.Template = slices.Insert(page.Template, insertAt, Template{TemplateID: tplID, ZOrder: zOrder})
					}
				}
				promoted = true
			}
			if !promoted {
				break
			}
		}
		return nil
	}
	if err := promoteEdge("Background", true); err != nil {
		return err
	}
	return promoteEdge("Foreground", false)
}

// structureBoundObjectIDs 收集已绑定逻辑结构树的对象标识，避免提升模板破坏结构引用
// 返回: map[string]bool 已绑定对象标识集合
func (p *pdfImporter) structureBoundObjectIDs() map[string]bool {
	if p.structureBindings == nil || len(p.structureBindings.sequences) == 0 && len(p.structureBindings.objects) == 0 {
		return nil
	}
	bound := make(map[string]bool)
	for _, refs := range p.structureBindings.sequences {
		for _, ref := range refs {
			for _, id := range ref.ObjectIDs {
				bound[id] = true
			}
		}
	}
	for _, refs := range p.structureBindings.objects {
		for _, ref := range refs {
			for _, id := range ref.ObjectIDs {
				bound[id] = true
			}
		}
	}
	return bound
}
