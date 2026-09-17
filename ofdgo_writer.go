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
	"archive/zip"
	"bytes"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"time"
)

const ofdNamespace = "http://www.ofdspec.org/2016"

// editorProgress 保存准备阶段的进度检查点，内存预览使用nil
type editorProgress func(stage string, completed, total int) error

// report 回报进度并原样传递调用方停止原因
// 入参: stage 阶段, completed 已处理项, total 总项数
// 返回: error 调用方停止原因
func (progress editorProgress) report(stage string, completed, total int) error {
	if progress != nil {
		return progress(stage, completed, total)
	}
	return nil
}

// WriteTo 逐个条目写出OFD，不关闭调用方输出流，出错时应丢弃本次输出
// 新增资源仅写入实际引用的部分，静态TrueType轮廓字体按实际文字生成子集
// 原有字体在全包引用可确定时保留字形编号裁剪；编辑资源、复杂字体及无法确定的引用保持原样
// 重开子集文档后输入未包含的文字，需要通过AddFont注册完整字体并替换原字体引用
// 入参: writer 输出流
// 返回: int64 已写入字节数, error 错误信息
func (e *Editor) WriteTo(writer io.Writer) (int64, error) {
	if err := e.validate(); err != nil {
		return 0, err
	}
	progress := editorProgress(e.OnWriteProgress)
	fonts, err := e.subsetFonts(progress)
	if err != nil {
		return 0, err
	}
	if e.source != nil {
		return e.writeSource(writer, fonts, progress)
	}
	if err := progress.report("write", 0, 0); err != nil {
		return 0, err
	}
	output := &ofdCountingWriter{writer: writer}
	archive := zip.NewWriter(output)
	err = e.writeParts(func(name string, data []byte, compressed bool) error {
		if subset, ok := fonts[name]; ok {
			data = subset
		}
		method := uint16(zip.Deflate)
		if compressed {
			method = zip.Store
		}
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if err != nil {
			return err
		}
		_, err = entry.Write(data)
		return err
	}, progress)
	if err == nil {
		err = archive.Close()
	}
	return output.count, err
}

// WritePagesTo 按指定顺序将页面另存为独立OFD，不修改当前文档或撤销记录
// 保留描述信息并生成新文档标识，迁移相关资源、目录、注释和签章外观，移除指向未选页面的跳转
// 快照只准备所选页面，关联引用由页面导入流程统一筛选
// 签名凭据不代表另存文件验签有效；进度与取消复用OnWriteProgress，另含snapshot、ids和commit阶段
// 入参: writer 输出流, indexes 从0开始的页面索引，不可重复或为空
// 返回: int64 已写入字节数, error 错误信息
func (e *Editor) WritePagesTo(writer io.Writer, indexes []int) (int64, error) {
	if len(indexes) == 0 {
		return 0, fmt.Errorf("no pages selected")
	}
	if err := e.validatePageIndexes(indexes); err != nil {
		return 0, err
	}
	progress := editorProgress(e.OnWriteProgress)
	if err := progress.report("snapshot", 0, 0); err != nil {
		return 0, err
	}
	snapshot := *e
	snapshot.pages = make([]PageContent, len(indexes))
	if e.source != nil {
		source := *e.source
		source.pages = make(map[string]*editorSourcePage, len(indexes))
		for _, index := range indexes {
			id := e.pages[index].ID
			if page := e.source.pages[id]; page != nil {
				source.pages[id] = page
			}
		}
		snapshot.source = &source
	}
	positions := make([]int, len(indexes))
	for i, index := range indexes {
		snapshot.pages[i] = e.pages[index]
		positions[i] = i
	}
	reader, err := snapshot.reader(progress)
	if err != nil {
		return 0, err
	}
	defer reader.Close()
	selected := NewEditor()
	id := selected.Info.DocID
	selected.Info = cloneEditorData(e.Info)
	selected.Info.DocID = id
	selected.OnWriteProgress = e.OnWriteProgress
	if _, err := selected.ImportPagesWithOptions(reader, positions, 0, PageImportOptions{Outlines: true, OnProgress: e.OnWriteProgress}); err != nil {
		return 0, err
	}
	return selected.WriteTo(writer)
}

// Reader 获取当前文档的独立内存快照，不进行ZIP压缩，后续修改不影响已有快照
// 返回的Reader可用于现有渲染、搜索和导出接口，新增资源仅包含实际引用的部分，二进制数据内部共享只读
// 返回: *Reader 阅读器, error 错误信息
func (e *Editor) Reader() (*Reader, error) {
	return e.reader(nil)
}

// reader 生成内存快照，保存时复用准备进度，预览时不触发回调
// 入参: progress 准备进度回调
// 返回: *Reader 阅读器, error 错误信息
func (e *Editor) reader(progress editorProgress) (*Reader, error) {
	if err := e.validate(); err != nil {
		return nil, err
	}
	if e.source != nil {
		return e.sourceReader(progress)
	}
	r := &Reader{files: make(map[string][]byte)}
	if err := e.writeParts(func(name string, data []byte, _ bool) error {
		r.files[name] = data
		return nil
	}, progress); err != nil {
		return nil, err
	}
	if err := r.initRoot(); err != nil {
		return nil, err
	}
	if _, err := r.Doc(); err != nil {
		return nil, err
	}
	for i, page := range r.doc.Pages.Page {
		r.pageHeaderCache[r.ResPath(page.BaseLoc)] = PageContent{Area: PageArea{PhysicalBox: e.pages[i].Area.PhysicalBox}}
	}
	return r, nil
}

// validate 校验文档保存条件
// 返回: error 错误信息
func (e *Editor) validate() error {
	if len(e.pages) == 0 {
		return fmt.Errorf("document must contain at least one page")
	}
	if e.source == nil || e.Info.DocID != e.source.info.DocID {
		if data, err := hex.DecodeString(e.Info.DocID); err != nil || len(data) != 16 {
			return fmt.Errorf("DocID must contain 32 hexadecimal characters")
		}
	}
	for i, date := range []string{e.Info.CreationDate, e.Info.ModDate} {
		if e.source != nil && date == []string{e.source.info.CreationDate, e.source.info.ModDate}[i] {
			continue
		}
		if date != "" {
			if _, err := time.Parse("2006-01-02", date); err != nil {
				return fmt.Errorf("invalid document date: %w", err)
			}
		}
	}
	return nil
}

// writeParts 生成根节点、文档、资源索引、页面及二进制资源
// 入参: write 条目写入方法, progress 保存进度回调，预览时为nil
// 返回: error 错误信息
func (e *Editor) writeParts(write func(string, []byte, bool) error, progress editorProgress) error {
	fonts, images, spaces, _ := e.usedResources()
	writeXML := func(name string, encode func(*ofdXML)) error {
		data, err := encodeOFDXML(encode)
		if err != nil {
			return err
		}
		if name == "Doc_0/Document.xml" {
			data, err = e.withOutlines(data)
			if err != nil {
				return err
			}
		}
		return write(name, data, false)
	}
	if err := writeXML("OFD.xml", func(x *ofdXML) {
		x.root("OFD", ofdAttrs{{Name: xml.Name{Local: "Version"}, Value: "1.0"}, {Name: xml.Name{Local: "DocType"}, Value: "OFD"}})
		x.start("DocBody", nil)
		x.start("DocInfo", nil)
		for _, field := range [][2]string{
			{"DocID", e.Info.DocID}, {"Title", e.Info.Title}, {"Author", e.Info.Author},
			{"Subject", e.Info.Subject}, {"Abstract", e.Info.Abstract},
			{"CreationDate", e.Info.CreationDate}, {"ModDate", e.Info.ModDate},
			{"Creator", "xiaoqidun/ofdgo"},
		} {
			if field[1] != "" {
				x.text(field[0], field[1])
			}
		}
		if e.Info.CustomDatas != nil && len(e.Info.CustomDatas.CustomData) != 0 {
			x.start("CustomDatas", nil)
			for _, item := range e.Info.CustomDatas.CustomData {
				attrs := ofdAttrs{{Name: xml.Name{Local: "Name"}, Value: item.Name}}
				x.start("CustomData", attrs)
				x.token(xml.CharData(item.Value))
				x.end("CustomData")
			}
			x.end("CustomDatas")
		}
		x.end("DocInfo")
		x.text("DocRoot", "Doc_0/Document.xml")
		x.end("DocBody")
		x.end("OFD")
	}); err != nil {
		return err
	}
	if err := writeXML("Doc_0/Document.xml", func(x *ofdXML) {
		x.root("Document", nil)
		x.start("CommonData", nil)
		x.text("MaxUnitID", strconv.Itoa(e.maxID))
		x.start("PageArea", nil)
		x.text("PhysicalBox", e.pages[0].Area.PhysicalBox)
		x.end("PageArea")
		if len(fonts)+len(images)+len(spaces) != 0 {
			x.text("DocumentRes", "DocumentRes.xml")
		}
		x.end("CommonData")
		x.start("Pages", nil)
		for i, page := range e.pages {
			var attrs ofdAttrs
			attrs.add("ID", page.ID)
			attrs.add("BaseLoc", fmt.Sprintf("Pages/%d/Content.xml", i+1))
			x.start("Page", attrs)
			x.end("Page")
		}
		x.end("Pages")
		x.end("Document")
	}); err != nil {
		return err
	}
	if len(fonts)+len(images)+len(spaces) != 0 {
		if err := writeXML("Doc_0/DocumentRes.xml", func(x *ofdXML) { x.resources(fonts, images, spaces) }); err != nil {
			return err
		}
	}
	for i, page := range e.pages {
		if err := progress.report("pages", i, len(e.pages)); err != nil {
			return err
		}
		if err := writeXML(fmt.Sprintf("Doc_0/Pages/%d/Content.xml", i+1), func(x *ofdXML) {
			x.page(page)
		}); err != nil {
			return err
		}
	}
	if err := progress.report("pages", len(e.pages), len(e.pages)); err != nil {
		return err
	}
	for _, resources := range [][]editorResource{fonts, images} {
		for _, resource := range resources {
			if err := write(resource.name, resource.data, resource.image != nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// usedResources 筛选当前引用的新增资源及需要跨页复用的原资源文件，不修改资源池
// 返回: []editorResource 新增字体, []editorResource 新增图片, []ColorSpace 新增颜色空间, []string 原资源文件
func (e *Editor) usedResources() (fonts, images []editorResource, spaces []ColorSpace, sourceFiles []string) {
	used := make(map[string]bool)
	promoted := make(map[string]bool)
	files := make(map[string]bool)
	for _, page := range e.pages {
		var original map[string]GraphicObject
		if e.source != nil {
			original = make(map[string]GraphicObject)
			if source := e.source.pages[page.ID]; source != nil && source.original != nil {
				for _, layer := range source.original.Content.Layer {
					for _, object := range layer.Objects {
						original[editorObjectID(object)] = object
					}
				}
			}
		}
		for _, layer := range page.Content.Layer {
			for _, object := range layer.Objects {
				before, exists := original[editorObjectID(object)]
				if origin := e.objectOrigin(editorObjectID(object)); origin != nil && (!exists || before.Type != object.Type || !reflect.DeepEqual(before.CompositeGraphicUnit, object.CompositeGraphicUnit)) {
					for _, name := range origin.page.original.PageRes {
						files[e.source.reader.ResPath(resolveResourcePath(origin.page.ref.BaseLoc, "", name))] = true
					}
				}
				for _, value := range []*FillColor{object.TextObject.FillColor, (*FillColor)(object.TextObject.StrokeColor), object.PathObject.FillColor, (*FillColor)(object.PathObject.StrokeColor)} {
					if value != nil && value.ColorSpace != "" {
						used[value.ColorSpace] = true
						if e.source != nil && (!exists || !reflect.DeepEqual(before, object)) {
							promoted[value.ColorSpace] = true
						}
					}
				}
				switch object.Type {
				case "CompositeObject", "CompositeGraphicUnit":
					collectCompositeReferences(object.CompositeGraphicUnit, used)
				case "TextObject":
					used[object.TextObject.Font] = true
					if e.source != nil && (!exists || before.Type != object.Type || before.TextObject.Font != object.TextObject.Font) {
						promoted[object.TextObject.Font] = true
					}
				case "ImageObject":
					used[object.ImageObject.ResourceID] = true
					if e.source != nil && (!exists || before.Type != object.Type || before.ImageObject.ResourceID != object.ImageObject.ResourceID) {
						promoted[object.ImageObject.ResourceID] = true
					}
					if object.ImageObject.ImageMask != "" {
						used[object.ImageObject.ImageMask] = true
						if e.source != nil && (!exists || before.Type != object.Type || before.ImageObject.ImageMask != object.ImageObject.ImageMask) {
							promoted[object.ImageObject.ImageMask] = true
						}
					}
				}
			}
		}
	}
	for i := len(e.resources) - 1; i >= 0; i-- {
		resource := e.resources[i]
		if resource.composite != "" && used[resource.composite] {
			for _, id := range resource.references {
				used[id] = true
			}
		}
	}
	for _, resource := range e.resources {
		if resource.composite != "" && used[resource.composite] {
			files[resource.name] = true
		}
		if resource.font != nil && used[resource.font.ID] {
			fonts = append(fonts, resource)
			delete(promoted, resource.font.ID)
		}
		if resource.image != nil && used[resource.image.ID] {
			images = append(images, resource)
			delete(promoted, resource.image.ID)
		}
		if resource.space != nil && used[resource.space.ID] {
			spaces = append(spaces, *resource.space)
			delete(promoted, resource.space.ID)
		}
	}
	for id := range promoted {
		if name := e.source.reader.resourceFiles[id]; name != "" {
			files[name] = true
		}
	}
	return fonts, images, spaces, slices.Sorted(maps.Keys(files))
}

// page 写出页面尺寸、图层和对象
// 入参: page 页面内容
func (x *ofdXML) page(page PageContent) {
	x.root("Page", nil)
	x.start("Area", nil)
	x.text("PhysicalBox", page.Area.PhysicalBox)
	x.end("Area")
	x.start("Content", nil)
	for _, layer := range page.Content.Layer {
		x.layer(layer)
	}
	x.end("Content")
	x.end("Page")
}

// layer 写出新建图层及其对象
// 入参: layer 图层
func (x *ofdXML) layer(layer Layer) {
	var attrs ofdAttrs
	attrs.add("ID", layer.ID)
	attrs.add("Type", layer.Type)
	x.start("Layer", attrs)
	for _, object := range layer.Objects {
		x.object(object, false)
	}
	x.end("Layer")
}

// resources 写出文档资源索引
// 入参: fonts 字体资源, images 图片资源, spaces 颜色空间
func (x *ofdXML) resources(fonts, images []editorResource, spaces []ColorSpace) {
	x.root("Res", ofdAttrs{{Name: xml.Name{Local: "BaseLoc"}, Value: "Res"}})
	if len(spaces) != 0 {
		x.start("ColorSpaces", nil)
		for _, space := range spaces {
			x.start("ColorSpace", ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: space.ID}, {Name: xml.Name{Local: "Type"}, Value: space.Type}, {Name: xml.Name{Local: "BitsPerComponent"}, Value: strconv.Itoa(space.BitsPerComponent)}})
			x.end("ColorSpace")
		}
		x.end("ColorSpaces")
	}
	if len(fonts) != 0 {
		x.start("Fonts", nil)
		for _, resource := range fonts {
			font := resource.font
			var attrs ofdAttrs
			attrs.add("ID", font.ID)
			attrs.add("FontName", font.FontName)
			attrs.add("FamilyName", font.FamilyName)
			attrs.add("Charset", font.Charset)
			if font.Bold {
				attrs.add("Bold", "true")
			}
			if font.Italic {
				attrs.add("Italic", "true")
			}
			if font.FixedWidth {
				attrs.add("FixedWidth", "true")
			}
			x.start("Font", attrs)
			x.text("FontFile", font.FontFile)
			x.end("Font")
		}
		x.end("Fonts")
	}
	if len(images) != 0 {
		x.start("MultiMedias", nil)
		for _, resource := range images {
			image := resource.image
			var attrs ofdAttrs
			attrs.add("ID", image.ID)
			attrs.add("Type", image.Type)
			attrs.add("Format", image.Format)
			x.start("MultiMedia", attrs)
			x.text("MediaFile", image.MediaFile)
			x.end("MultiMedia")
		}
		x.end("MultiMedias")
	}
	x.end("Res")
}

// ofdCountingWriter 记录实际输出字节数
type ofdCountingWriter struct {
	writer io.Writer
	count  int64
}

// Write 写入并累计字节数
// 入参: data 数据
// 返回: int 字节数, error 错误信息
func (w *ofdCountingWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.count += int64(n)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return n, err
}

// ofdXML 使用标准ofd命名空间写出XML
type ofdXML struct {
	encoder *xml.Encoder
	err     error
}

// newOFDXML 创建XML编码器
// 入参: writer 输出流
// 返回: *ofdXML 编码器
func newOFDXML(writer io.Writer) *ofdXML {
	_, err := io.WriteString(writer, xml.Header)
	return &ofdXML{encoder: xml.NewEncoder(writer), err: err}
}

// token 写入XML标记并保留首个错误
// 入参: token XML标记
func (x *ofdXML) token(token xml.Token) {
	if x.err == nil {
		x.err = x.encoder.EncodeToken(token)
	}
}

// root 写入声明命名空间的根节点
// 入参: name 节点名, attrs 属性
func (x *ofdXML) root(name string, attrs ofdAttrs) {
	attrs.add("xmlns:ofd", ofdNamespace)
	x.start(name, attrs)
}

// start 写入节点起始标记
// 入参: name 节点名, attrs 属性
func (x *ofdXML) start(name string, attrs ofdAttrs) {
	x.token(xml.StartElement{Name: xml.Name{Local: "ofd:" + name}, Attr: attrs})
}

// end 写入节点结束标记
// 入参: name 节点名
func (x *ofdXML) end(name string) {
	x.token(xml.EndElement{Name: xml.Name{Local: "ofd:" + name}})
}

// text 写入文本节点
// 入参: name 节点名, value 文本
func (x *ofdXML) text(name, value string) {
	x.start(name, nil)
	x.token(xml.CharData(value))
	x.end(name)
}

// finish 完成XML输出
// 返回: error 错误信息
func (x *ofdXML) finish() error {
	if x.err != nil {
		return x.err
	}
	return x.encoder.Close()
}

// ofdAttrs OFD节点属性
type ofdAttrs []xml.Attr

// add 添加非空属性
// 入参: name 属性名, value 属性值
func (a *ofdAttrs) add(name, value string) {
	if value != "" {
		*a = append(*a, xml.Attr{Name: xml.Name{Local: name}, Value: value})
	}
}

// number 添加非零数值属性
// 入参: name 属性名, value 数值
func (a *ofdAttrs) number(name string, value float64) {
	if value != 0 {
		a.add(name, ofdNumber(value))
	}
}

// flag 添加显式布尔属性
// 入参: name 属性名, value 布尔值
func (a *ofdAttrs) flag(name string, value *bool) {
	if value != nil {
		a.add(name, strconv.FormatBool(*value))
	}
}

// alpha 添加显式透明度
// 入参: value 透明度
func (a *ofdAttrs) alpha(value *int) {
	if value != nil {
		a.add("Alpha", strconv.Itoa(*value))
	}
}

// encodeOFDXML 编码文档节点，用于保存和独立副本
// 入参: encode 节点写入方法
// 返回: []byte XML数据, error 错误信息
func encodeOFDXML(encode func(*ofdXML)) ([]byte, error) {
	var data bytes.Buffer
	x := newOFDXML(&data)
	encode(x)
	if err := x.finish(); err != nil {
		return nil, err
	}
	return data.Bytes(), nil
}

// object 按OFD规定顺序写出基本对象
// 入参: object 图形对象, root 是否为根节点
func (x *ofdXML) object(object GraphicObject, root bool) {
	var attrs ofdAttrs
	var fill, stroke *FillColor
	if root {
		attrs.add("xmlns:ofd", ofdNamespace)
	}
	switch object.Type {
	case "TextObject":
		obj := object.TextObject
		attrs.add("ID", obj.ID)
		attrs.add("DrawParam", obj.DrawParam)
		attrs.add("Boundary", obj.Boundary)
		attrs.add("CTM", obj.CTM)
		attrs.add("Font", obj.Font)
		attrs.number("Size", obj.Size)
		attrs.number("HScale", obj.HScale)
		attrs.number("Weight", float64(obj.Weight))
		attrs.number("ReadDirection", float64(obj.ReadDirection))
		attrs.number("CharDirection", float64(obj.CharDirection))
		attrs.number("LineWidth", obj.LineWidth)
		attrs.number("MiterLimit", obj.MiterLimit)
		attrs.add("Join", obj.Join)
		if obj.Italic {
			attrs.add("Italic", "true")
		}
		attrs.flag("Visible", obj.Visible)
		attrs.flag("Stroke", obj.Stroke)
		attrs.flag("Fill", obj.Fill)
		attrs.alpha(obj.Alpha)
		fill, stroke = obj.FillColor, (*FillColor)(obj.StrokeColor)
	case "PathObject", "Path":
		obj := object.PathObject
		attrs.add("ID", obj.ID)
		attrs.add("DrawParam", obj.DrawParam)
		attrs.add("Boundary", obj.Boundary)
		attrs.add("CTM", obj.CTM)
		attrs.number("LineWidth", obj.LineWidth)
		attrs.number("MiterLimit", obj.MiterLimit)
		attrs.add("Join", obj.Join)
		attrs.add("Cap", obj.Cap)
		attrs.add("Rule", obj.Rule)
		if obj.dashPatternSet {
			attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "DashPattern"}, Value: obj.DashPattern})
		} else {
			attrs.add("DashPattern", obj.DashPattern)
		}
		if obj.DashOffset != nil {
			attrs.add("DashOffset", ofdNumber(*obj.DashOffset))
		}
		attrs.flag("Visible", obj.Visible)
		attrs.flag("Stroke", obj.Stroke)
		attrs.flag("Fill", obj.Fill)
		attrs.alpha(obj.Alpha)
		fill, stroke = obj.FillColor, (*FillColor)(obj.StrokeColor)
	case "ImageObject":
		obj := object.ImageObject
		attrs.add("ID", obj.ID)
		attrs.add("Boundary", obj.Boundary)
		attrs.add("CTM", obj.CTM)
		attrs.add("ResourceID", obj.ResourceID)
		attrs.add("ImageMask", obj.ImageMask)
		attrs.flag("Visible", obj.Visible)
		attrs.alpha(obj.Alpha)
	case "CompositeObject", "CompositeGraphicUnit":
		obj := object.CompositeGraphicUnit
		attrs.add("ID", obj.ID)
		attrs.add("Boundary", obj.Boundary)
		attrs.add("CTM", obj.CTM)
		attrs.add("ResourceID", obj.ResourceID)
		attrs.add("DrawParam", obj.DrawParam)
		attrs.flag("Visible", obj.Visible)
		attrs.alpha(obj.Alpha)
	}
	x.start(object.Type, attrs)
	if object.Type == "ImageObject" {
		x.clips(object.ImageObject.Clips)
	} else if object.Type == "CompositeObject" || object.Type == "CompositeGraphicUnit" {
		x.clips(object.CompositeGraphicUnit.Clips)
	} else if object.Type == "PathObject" || object.Type == "Path" {
		x.clips(object.PathObject.Clips)
		x.color("StrokeColor", stroke)
		x.color("FillColor", fill)
		x.text("AbbreviatedData", object.PathObject.AbbreviatedData)
	} else if object.Type == "TextObject" {
		x.clips(object.TextObject.Clips)
		x.color("FillColor", fill)
		x.color("StrokeColor", stroke)
		for _, code := range object.TextObject.TextCode {
			var attrs ofdAttrs
			attrs.add("X", code.X)
			attrs.add("Y", code.Y)
			attrs.add("DeltaX", code.DeltaX)
			attrs.add("DeltaY", code.DeltaY)
			x.textCode(code.Value, attrs)
		}
	}
	x.end(object.Type)
}

// clips 写出对象路径裁剪，保留裁剪集合、区域及路径的变换
// 入参: clips 裁剪集合，nil不输出节点
func (x *ofdXML) clips(clips *Clips) {
	if clips == nil {
		return
	}
	var attrs ofdAttrs
	attrs.flag("TransFlag", clips.TransFlag)
	x.start("Clips", attrs)
	for _, clip := range clips.Clip {
		x.start("Clip", nil)
		for _, area := range clip.Area {
			var attrs ofdAttrs
			attrs.add("CTM", area.CTM)
			attrs.add("DrawParam", area.DrawParam)
			x.start("Area", attrs)
			for _, path := range area.Path {
				x.object(GraphicObject{Type: "Path", PathObject: path}, false)
			}
			x.end("Area")
		}
		x.end("Clip")
	}
	x.end("Clips")
}

// textCode 使用XML字符引用转义空格，避免依赖阅读器保留原始空白
// 入参: value OFD文字内容, attrs 文字定位属性
func (x *ofdXML) textCode(value string, attrs ofdAttrs) {
	if x.err != nil {
		return
	}
	var escaped bytes.Buffer
	if x.err = xml.EscapeText(&escaped, []byte(value)); x.err != nil {
		return
	}
	content := struct {
		Text []byte `xml:",innerxml"`
	}{Text: bytes.ReplaceAll(escaped.Bytes(), []byte(" "), []byte("&#x20;"))}
	x.err = x.encoder.EncodeElement(content, xml.StartElement{Name: xml.Name{Local: "ofd:TextCode"}, Attr: attrs})
}

// color 写出基本颜色
// 入参: name 节点名, color 颜色
func (x *ofdXML) color(name string, color *FillColor) {
	if color == nil {
		return
	}
	var attrs ofdAttrs
	attrs.add("Value", color.Value)
	attrs.add("ColorSpace", color.ColorSpace)
	if color.Index != nil {
		attrs.add("Index", strconv.Itoa(*color.Index))
	}
	attrs.alpha(color.Alpha)
	x.start(name, attrs)
	x.end(name)
}
