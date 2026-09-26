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
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
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
// 制作软件统一为xiaoqidun/ofdgo，未修改的文档保留原制作软件及版本
// 重开子集文档后输入未包含的文字，需要通过AddFont注册完整字体并替换原字体引用
// 入参: writer 输出流
// 返回: int64 已写入字节数, error 错误信息
func (e *Editor) WriteTo(writer io.Writer) (int64, error) {
	if e.encryption != nil {
		return e.writeEncrypted(writer)
	}
	return e.writePlaintext(writer)
}

// writePlaintext 写出未加密包，供显式明文输出与加密封装共用
// 入参: writer 输出流
// 返回: int64 写入字节数, error 错误信息
func (e *Editor) writePlaintext(writer io.Writer) (int64, error) {
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
		source.annotationPages = nil
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
	selected.encryption = e.encryption
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
	r := &Reader{files: make(map[string][]byte), encryption: e.encryption}
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
	if len(e.removedPages) != 0 {
		output := write
		write = func(name string, data []byte, binary bool) error {
			if !binary && strings.EqualFold(path.Ext(name), ".xml") {
				var err error
				data, err = pruneCreatedPageReferences(data, e.removedPages)
				if err != nil {
					return err
				}
			}
			return output(name, data, binary)
		}
	}
	fonts, images, spaces, definitions := e.usedResources()
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
			{"Creator", ofdCreator},
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
		for _, name := range definitions {
			x.text("DocumentRes", "/"+name)
		}
		x.end("CommonData")
		x.start("Pages", nil)
		for _, page := range e.pages {
			var attrs ofdAttrs
			attrs.add("ID", page.ID)
			attrs.add("BaseLoc", packagePagePath("", page.ID))
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
		name := packagePagePath(e.packageDirectory(), page.ID)
		if len(e.origins) == 0 {
			if err := writeXML(name, func(x *ofdXML) { x.page(page) }); err != nil {
				return err
			}
		} else {
			data, err := e.sourceNewPageXML(page)
			if err != nil {
				return err
			}
			if err := write(name, data, false); err != nil {
				return err
			}
		}
	}
	if err := progress.report("pages", len(e.pages), len(e.pages)); err != nil {
		return err
	}
	for _, resources := range [][]editorResource{fonts, images} {
		for _, resource := range resources {
			if resource.name == "" {
				continue
			}
			if err := write(resource.name, resource.data, resource.image != nil); err != nil {
				return err
			}
		}
	}
	for _, resource := range e.resources {
		if resource.definition() != "" && slices.Contains(definitions, resource.name) {
			if err := write(resource.name, resource.data, false); err != nil {
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
	if e.source != nil {
		for _, name := range e.annotationFiles() {
			data, changed := e.source.reader.files[name]
			if !changed {
				continue
			}
			refs := editorResourceRefs{ids: used, files: make(map[string]bool)}
			if _, safe := refs.scan(bytes.NewReader(data), name); !safe {
				for _, resource := range e.resources {
					if resource.font != nil {
						used[resource.font.ID] = true
					}
					if resource.image != nil {
						used[resource.image.ID] = true
					}
					if resource.space != nil {
						used[resource.space.ID] = true
					}
					used[resource.definition()] = true
				}
			}
		}
		maps.Copy(promoted, used)
	}
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
				collectObjectReferences(object, used)
				if e.source != nil && (!exists || !reflect.DeepEqual(before, object)) {
					collectObjectReferences(object, promoted)
				}
				if origin := e.objectOrigin(editorObjectID(object)); origin != nil && origin.page != nil && (!exists || before.Type != object.Type || !reflect.DeepEqual(before.CompositeGraphicUnit, object.CompositeGraphicUnit)) {
					for _, name := range origin.page.original.PageRes {
						files[e.source.reader.ResPath(resolveResourcePath(origin.page.ref.BaseLoc, "", name))] = true
					}
				}
			}
		}
	}
	for i := len(e.resources) - 1; i >= 0; i-- {
		resource := e.resources[i]
		if resource.definition() != "" && used[resource.definition()] {
			for _, id := range resource.references {
				used[id] = true
				if e.source != nil {
					promoted[id] = true
				}
			}
		}
	}
	for _, resource := range e.resources {
		if resource.definition() != "" && used[resource.definition()] {
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
	var actions []Action
	if root {
		attrs.add("xmlns:ofd", ofdNamespace)
	}
	switch object.Type {
	case "TextObject", "Text":
		obj := object.TextObject
		attrs.add("ID", obj.ID)
		attrs.add("DrawParam", obj.DrawParam)
		attrs.add("Boundary", obj.Boundary)
		attrs.add("CTM", obj.CTM)
		attrs.add("Font", obj.Font)
		attrs.number("Size", obj.Size)
		attrs.number("HScale", obj.HScale)
		attrs.number("VScale", obj.VScale)
		attrs.add("Decoration", obj.Decoration)
		attrs.number("Weight", float64(obj.Weight))
		attrs.number("ReadDirection", float64(obj.ReadDirection))
		attrs.number("CharDirection", float64(obj.CharDirection))
		attrs.number("LineWidth", obj.LineWidth)
		if obj.LineWidthSet && obj.LineWidth == 0 {
			attrs.add("LineWidth", "0")
		}
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
		actions = obj.Actions
	case "PathObject", "Path":
		obj := object.PathObject
		attrs.add("ID", obj.ID)
		attrs.add("DrawParam", obj.DrawParam)
		attrs.add("Boundary", obj.Boundary)
		attrs.add("CTM", obj.CTM)
		attrs.number("LineWidth", obj.LineWidth)
		if obj.LineWidthSet && obj.LineWidth == 0 {
			attrs.add("LineWidth", "0")
		}
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
		actions = obj.Actions
	case "ImageObject":
		obj := object.ImageObject
		attrs.add("ID", obj.ID)
		attrs.add("Boundary", obj.Boundary)
		attrs.add("CTM", obj.CTM)
		attrs.add("ResourceID", obj.ResourceID)
		attrs.add("ImageMask", obj.ImageMask)
		attrs.flag("Visible", obj.Visible)
		attrs.alpha(obj.Alpha)
		actions = obj.Actions
	case "CompositeObject", "CompositeGraphicUnit":
		obj := object.CompositeGraphicUnit
		attrs.add("ID", obj.ID)
		attrs.add("Boundary", obj.Boundary)
		attrs.add("CTM", obj.CTM)
		attrs.add("ResourceID", obj.ResourceID)
		attrs.add("DrawParam", obj.DrawParam)
		attrs.flag("Visible", obj.Visible)
		attrs.alpha(obj.Alpha)
		actions = obj.Actions
	}
	x.start(object.Type, attrs)
	x.actions(actions)
	if object.Type == "ImageObject" {
		x.clips(object.ImageObject.Clips)
		x.border(object.ImageObject.Border)
	} else if object.Type == "CompositeObject" || object.Type == "CompositeGraphicUnit" {
		x.clips(object.CompositeGraphicUnit.Clips)
	} else if object.Type == "PathObject" || object.Type == "Path" {
		x.clips(object.PathObject.Clips)
		x.color("StrokeColor", stroke)
		x.color("FillColor", fill)
		x.text("AbbreviatedData", object.PathObject.AbbreviatedData)
	} else if object.Type == "TextObject" || object.Type == "Text" {
		x.clips(object.TextObject.Clips)
		x.color("FillColor", fill)
		x.color("StrokeColor", stroke)
		offset, index := 0, 0
		for _, code := range object.TextObject.TextCode {
			end := offset + len(textCodeRunes(code.Value))
			for index < len(object.TextObject.CGTransform) && object.TextObject.CGTransform[index].CodePosition < end {
				transform := object.TextObject.CGTransform[index]
				attrs := ofdAttrs{
					{Name: xml.Name{Local: "CodePosition"}, Value: strconv.Itoa(transform.CodePosition - offset)},
					{Name: xml.Name{Local: "CodeCount"}, Value: strconv.Itoa(transform.CodeCount)},
					{Name: xml.Name{Local: "GlyphCount"}, Value: strconv.Itoa(transform.GlyphCount)},
				}
				x.start("CGTransform", attrs)
				x.text("Glyphs", transform.Glyphs)
				x.end("CGTransform")
				index++
			}
			var attrs ofdAttrs
			attrs.add("X", code.X)
			attrs.add("Y", code.Y)
			attrs.add("DeltaX", code.DeltaX)
			attrs.add("DeltaY", code.DeltaY)
			x.textCode(code.Value, attrs)
			offset = end
		}
	}
	x.end(object.Type)
}

// clips 写出对象裁剪，保留区域内的路径与文字
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
			for _, text := range area.Text {
				x.object(GraphicObject{Type: "Text", TextObject: text}, false)
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

// color 写出纯色、渐变和图案，保留颜色空间与透明度
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
	if color.AxialShd != nil {
		shading := color.AxialShd
		x.shading("AxialShd", shadingAttrs(shading.MapType, shading.MapUnit, shading.Extend, shading.StartPoint, shading.EndPoint), shading.Segment)
	}
	if color.RadialShd != nil {
		shading := color.RadialShd
		attrs := shadingAttrs(shading.MapType, shading.MapUnit, shading.Extend, shading.StartPoint, shading.EndPoint)
		attrs.add("StartRadius", ofdNumber(shading.StartRadius))
		attrs.add("EndRadius", ofdNumber(shading.EndRadius))
		attrs.number("Eccentricity", shading.Eccentricity)
		attrs.number("Angle", shading.Angle)
		x.shading("RadialShd", attrs, shading.Segment)
	}
	if color.Pattern != nil {
		x.pattern(color.Pattern)
	}
	x.end(name)
}

// shadingAttrs 编码轴向和径向渐变的共同属性
// 入参: mapType 映射方式, mapUnit 映射周期, extend 延伸方式, start、end 渐变端点
// 返回: ofdAttrs 属性集合
func shadingAttrs(mapType string, mapUnit float64, extend, start, end string) ofdAttrs {
	var attrs ofdAttrs
	attrs.add("MapType", mapType)
	attrs.number("MapUnit", mapUnit)
	attrs.add("Extend", extend)
	attrs.add("StartPoint", start)
	attrs.add("EndPoint", end)
	return attrs
}

// shading 写出渐变分段，保留未显式设置的位置
// 入参: name 渐变类型, attrs 属性, segments 色标
func (x *ofdXML) shading(name string, attrs ofdAttrs, segments []ShdSegment) {
	x.start(name, attrs)
	for _, segment := range segments {
		var attrs ofdAttrs
		if !segment.positionMissing {
			attrs.add("Position", ofdNumber(segment.Position))
		}
		x.start("Segment", attrs)
		x.element("Color", segment.Color)
		x.end("Segment")
	}
	x.end(name)
}

// element 写出具有标准XML标签的子树，显式声明OFD命名空间
// 入参: name 元素名称, value 元素内容
func (x *ofdXML) element(name string, value any) {
	if x.err == nil {
		x.err = x.encoder.EncodeElement(value, xml.StartElement{Name: xml.Name{Space: ofdNamespace, Local: name}})
	}
}

// border 写出图片边框及其绘制颜色
// 入参: border 边框，nil不输出节点
func (x *ofdXML) border(border *ImageBorder) {
	if border == nil {
		return
	}
	var attrs ofdAttrs
	if border.LineWidth != nil {
		attrs.add("LineWidth", ofdNumber(*border.LineWidth))
	}
	attrs.number("HorizonalCornerRadius", border.HorizonalCornerRadius)
	attrs.number("VerticalCornerRadius", border.VerticalCornerRadius)
	attrs.number("DashOffset", border.DashOffset)
	attrs.add("DashPattern", border.DashPattern)
	x.start("Border", attrs)
	x.color("BorderColor", (*FillColor)(border.BorderColor))
	x.end("Border")
}

// pattern 写出图案单元，保留对象绘制顺序
// 入参: pattern 图案
func (x *ofdXML) pattern(pattern *Pattern) {
	var attrs ofdAttrs
	attrs.number("Width", pattern.Width)
	attrs.number("Height", pattern.Height)
	attrs.number("XStep", pattern.XStep)
	attrs.number("YStep", pattern.YStep)
	attrs.add("ReflectMethod", pattern.ReflectMethod)
	attrs.add("RelativeTo", pattern.RelativeTo)
	attrs.add("CTM", pattern.CTM)
	x.start("Pattern", attrs)
	x.start("CellContent", nil)
	for _, object := range pattern.CellContent.Objects {
		x.object(object, false)
	}
	x.end("CellContent")
	x.end("Pattern")
}

// actions 写出对象动作及精确点击区域，不检查链接可达性
// 入参: actions 动作列表
func (x *ofdXML) actions(actions []Action) {
	if len(actions) == 0 {
		return
	}
	x.start("Actions", nil)
	for _, action := range actions {
		var attrs ofdAttrs
		attrs.add("Event", action.Event)
		x.start("Action", attrs)
		if action.Region != nil {
			x.start("Region", nil)
			for _, area := range action.Region.Area {
				x.start("Area", ofdAttrs{{Name: xml.Name{Local: "Start"}, Value: area.Start}})
				for _, command := range area.Command {
					var attrs ofdAttrs
					for _, pair := range [][2]string{{"Point1", command.Point1}, {"Point2", command.Point2}, {"Point3", command.Point3}, {"EllipseSize", command.EllipseSize}, {"RotationAngle", command.RotationAngle}, {"LargeArc", command.LargeArc}, {"SweepDirection", command.SweepDirection}, {"EndPoint", command.EndPoint}} {
						attrs.add(pair[0], pair[1])
					}
					x.start(command.Type, attrs)
					x.end(command.Type)
				}
				x.end("Area")
			}
			x.end("Region")
		}
		if action.Goto != nil {
			x.start("Goto", nil)
			if dest := action.Goto.Dest; dest != nil {
				attrs := ofdAttrs{{Name: xml.Name{Local: "Type"}, Value: dest.Type}, {Name: xml.Name{Local: "PageID"}, Value: dest.PageID}}
				switch dest.Type {
				case "XYZ":
					if !dest.OmitLeft {
						attrs.add("Left", ofdNumber(dest.Left))
					}
					if !dest.OmitTop {
						attrs.add("Top", ofdNumber(dest.Top))
					}
					if !dest.OmitZoom {
						attrs.add("Zoom", ofdNumber(dest.Zoom))
					}
				case "FitH":
					if !dest.OmitTop {
						attrs.add("Top", ofdNumber(dest.Top))
					}
				case "FitV":
					if !dest.OmitLeft {
						attrs.add("Left", ofdNumber(dest.Left))
					}
				case "FitR":
					attrs.add("Left", ofdNumber(dest.Left))
					attrs.add("Right", ofdNumber(dest.Right))
					attrs.add("Top", ofdNumber(dest.Top))
					attrs.add("Bottom", ofdNumber(dest.Bottom))
				}
				x.start("Dest", attrs)
				x.end("Dest")
			}
			if action.Goto.Bookmark != nil {
				x.element("Bookmark", action.Goto.Bookmark)
			}
			x.end("Goto")
		}
		if action.URI != nil {
			x.element("URI", action.URI)
		}
		if action.GotoA != nil {
			x.element("GotoA", action.GotoA)
		}
		if action.Sound != nil {
			x.element("Sound", action.Sound)
		}
		if action.Movie != nil {
			x.element("Movie", action.Movie)
		}
		x.end("Action")
	}
	x.end("Actions")
}
