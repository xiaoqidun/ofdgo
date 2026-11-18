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
	"encoding/xml"
	"fmt"
	"path"
)

// TemplateReference 普通页面的模板引用，保留原始模板标识及绑定层次
// PageIndex为当前文档的零基页面索引，PageID为页面标识；ZOrder为空表示沿用模板默认层次
type TemplateReference struct {
	PageIndex int
	PageID    string
	Template
}

// Templates 返回当前文档及所选版本的模板定义副本，省略的ZOrder返回Background
// 返回: []TemplatePage 独立模板清单, error 文档读取错误
func (e *Editor) Templates() ([]TemplatePage, error) {
	if e.source == nil {
		return []TemplatePage{}, nil
	}
	result := append([]TemplatePage{}, e.source.document.CommonData.TemplatePage...)
	for i := range result {
		result[i].ZOrder = editorTemplateOrder(result[i].ZOrder)
	}
	return result, nil
}

// PageTemplates 按文件顺序返回普通页面模板引用，ZOrder为空表示沿用模板默认层次
// 入参: page 零基页面索引
// 返回: []Template 独立引用清单, error 页面读取错误
func (e *Editor) PageTemplates(page int) ([]Template, error) {
	p, err := e.page(page)
	if err != nil {
		return nil, err
	}
	return append([]Template{}, p.Template...), nil
}

// TemplateReferences 返回当前文档及所选版本全部普通页面的模板引用副本，不包含模板页自身引用
// 清单按页面索引及页内引用文件顺序排列，无引用时返回空切片，读取失败时不返回部分结果
// 返回: []TemplateReference 引用定位及绑定层次清单, error 页面读取错误
func (e *Editor) TemplateReferences() ([]TemplateReference, error) {
	result := []TemplateReference{}
	for i := range e.pages {
		refs, err := e.PageTemplates(i)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			result = append(result, TemplateReference{PageIndex: i, PageID: e.pages[i].ID, Template: ref})
		}
	}
	return result, nil
}

// BindTemplate 在页面末尾绑定模板，空ZOrder沿用模板默认层次，重复绑定拒绝
// 入参: page 页面索引, id 模板标识, zOrder 页面覆盖层次
// 返回: error 标识、层次或重复绑定错误
func (e *Editor) BindTemplate(page int, id, zOrder string) error {
	if !validEditorTemplateOrder(zOrder) {
		return fmt.Errorf("invalid template ZOrder %q", zOrder)
	}
	return e.editLayerPage(page, func(r *Reader, data []byte, root *editorXML) ([]byte, error) {
		template, err := editorTemplateDefinition(r, id)
		if err != nil {
			return nil, err
		}
		for _, node := range editorStructureChildren(root, "Template") {
			if sameResourceID(node.attr("TemplateID"), id) {
				return nil, fmt.Errorf("template %q is already bound", id)
			}
		}
		attrs := ofdAttrs{{Name: xml.Name{Local: "TemplateID"}, Value: template.ID}}
		attrs.add("ZOrder", zOrder)
		fragment, err := root.containerXML("Template", attrs, nil)
		if err != nil {
			return nil, err
		}
		at := root.open
		for _, node := range editorStructureChildren(root, "Template") {
			at = node.end
		}
		if root.open == root.end {
			return editorAppendStructure(data, root, fragment), nil
		}
		return editorPatchXML(data, []editorXMLPatch{{at, at, fragment}}), nil
	})
}

// UnbindTemplate 解除当前页面的唯一指定模板引用，保留模板定义与其他页面绑定
// 入参: page 页面索引, id 模板标识
// 返回: error 引用或结构错误
func (e *Editor) UnbindTemplate(page int, id string) error {
	return e.editLayerPage(page, func(_ *Reader, data []byte, root *editorXML) ([]byte, error) {
		node, err := editorFindStructureID(root, "Template", "TemplateID", id)
		if err != nil {
			return nil, err
		}
		return editorPatchXML(data, []editorXMLPatch{{node.start, node.end, nil}}), nil
	})
}

// SetPageTemplateZOrder 设置页面模板的前景或背景层次，空值删除覆盖以沿用模板默认值
// 入参: page 页面索引, id 模板标识, zOrder 页面覆盖层次
// 返回: error 引用、层次或结构错误
func (e *Editor) SetPageTemplateZOrder(page int, id, zOrder string) error {
	if !validEditorTemplateOrder(zOrder) {
		return fmt.Errorf("invalid template ZOrder %q", zOrder)
	}
	return e.editLayerPage(page, func(_ *Reader, data []byte, root *editorXML) ([]byte, error) {
		node, err := editorFindStructureID(root, "Template", "TemplateID", id)
		if err != nil {
			return nil, err
		}
		if node.attr("ZOrder") == zOrder {
			return data, nil
		}
		return editorStructureAttribute(data, node, "ZOrder", zOrder)
	})
}

// MovePageTemplate 调整引用在页面全部模板引用中的零基文件顺序，同层次引用按此顺序绘制
// 入参: page 页面索引, id 模板标识, order 全部引用中的目标顺序
// 返回: error 引用、顺序或结构错误
func (e *Editor) MovePageTemplate(page int, id string, order int) error {
	return e.editLayerPage(page, func(_ *Reader, data []byte, root *editorXML) ([]byte, error) {
		node, err := editorFindStructureID(root, "Template", "TemplateID", id)
		if err != nil {
			return nil, err
		}
		return editorMoveStructure(data, editorStructureChildren(root, "Template"), node, order)
	})
}

// CreateTemplateFromPage 复制指定普通页面自身内容为模板，不改变源页或自动绑定，空层次采用Background
// 复用页面复制的标识重映射并保留原XML及资源；模板引用不递归复制，注解和签章不属于模板内容
// 模板编辑可使用普通页面副本及现有编辑接口，再通过ReplaceTemplateFromPage保持模板身份提交
// 入参: page 源页面索引, name 模板名称, zOrder 默认前景背景层次
// 返回: string 新模板标识, error 页面、标识重映射或提交错误
func (e *Editor) CreateTemplateFromPage(page int, name, zOrder string) (string, error) {
	if !validEditorTemplateOrder(zOrder) {
		return "", fmt.Errorf("invalid template ZOrder %q", zOrder)
	}
	var id string
	err := e.Transaction(func(_ *Editor) error {
		if _, err := e.page(page); err != nil {
			return err
		}
		if err := e.prepareSourceIDs(); err != nil {
			return err
		}
		r, err := e.reader(nil)
		if err != nil {
			return err
		}
		defer r.Close()
		doc, err := r.Doc()
		if err != nil {
			return err
		}
		source := r.ResPath(doc.Pages.Page[page].BaseLoc)
		data, err := r.readFile(source)
		if err != nil {
			return err
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return err
		}
		var patches []editorXMLPatch
		for _, node := range editorStructureChildren(root, "Template") {
			patches = append(patches, editorXMLPatch{node.start, node.end, nil})
		}
		if root.child("Area") == nil {
			content, err := r.PageContentByIndex(page)
			if err != nil {
				return err
			}
			var fields []byte
			for _, field := range [][2]string{{"PhysicalBox", content.Area.PhysicalBox}, {"ApplicationBox", content.Area.ApplicationBox}, {"ContentBox", content.Area.ContentBox}, {"BleedBox", content.Area.BleedBox}} {
				if field[1] != "" {
					fields = append(fields, root.textXML(field[0], field[1])...)
				}
			}
			area, err := root.containerXML("Area", nil, fields)
			if err != nil {
				return err
			}
			at := root.close
			for _, node := range root.children {
				if node.matchesOFD("Content") || node.matchesOFD("Actions") {
					at = node.start
					break
				}
			}
			if root.open == root.end {
				data = editorAppendStructure(data, root, area)
			} else {
				patches = append(patches, editorXMLPatch{at, at, area})
			}
		}
		data = editorPatchXML(data, patches)
		root, err = parseEditorXML(data)
		if err != nil {
			return err
		}
		if !editorXMLCopyable(root) {
			return fmt.Errorf("template source contains unsupported identifiers")
		}
		ids := make(map[string]string)
		if err := e.collectCompositeIDs(root, ids); err != nil {
			return err
		}
		data, err = editorXMLRemapIDs(data, root, ids)
		if err != nil {
			return err
		}
		id = e.nextID()
		location := packageAvailableName(r, nil, path.Join(path.Dir(source), "Template_"+id+".xml"))
		document, err := r.readFile(r.documentRoot)
		if err != nil {
			return err
		}
		entry, err := parseEditorXML(document)
		if err != nil {
			return err
		}
		common := entry.child("CommonData")
		if common == nil {
			return fmt.Errorf("document has no CommonData")
		}
		attrs := ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: id}, {Name: xml.Name{Local: "BaseLoc"}, Value: "/" + location}, {Name: xml.Name{Local: "ZOrder"}, Value: editorTemplateOrder(zOrder)}}
		attrs.add("Name", name)
		fragment, err := common.containerXML("TemplatePage", attrs, nil)
		if err != nil {
			return err
		}
		at := common.close
		if node := common.child("DefaultCS"); node != nil {
			at = node.start
		}
		document = editorPatchXML(document, []editorXMLPatch{{at, at, fragment}})
		return e.commitLayerTemplateParts(r, map[string][]byte{location: data, r.documentRoot: document})
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// RenameTemplate 修改当前文档模板名称，空字符串清空名称，不修改共享模板内容
// 入参: id 模板标识, name 模板名称
// 返回: error 标识或结构错误
func (e *Editor) RenameTemplate(id, name string) error {
	return e.editTemplateDefinition(id, func(_ *Reader, data []byte, node *editorXML) ([]byte, error) {
		if node.attr("Name") == name {
			return data, nil
		}
		return editorStructureAttribute(data, node, "Name", name)
	})
}

// SetTemplateZOrder 设置模板默认前景背景层次，不修改页面显式覆盖，空值采用Background
// 入参: id 模板标识, zOrder 默认前景背景层次
// 返回: error 标识、层次或结构错误
func (e *Editor) SetTemplateZOrder(id, zOrder string) error {
	if !validEditorTemplateOrder(zOrder) {
		return fmt.Errorf("invalid template ZOrder %q", zOrder)
	}
	zOrder = editorTemplateOrder(zOrder)
	return e.editTemplateDefinition(id, func(_ *Reader, data []byte, node *editorXML) ([]byte, error) {
		if editorTemplateOrder(node.attr("ZOrder")) == zOrder {
			return data, nil
		}
		return editorStructureAttribute(data, node, "ZOrder", zOrder)
	})
}

// ReplaceTemplateFromPage 复制普通页自身内容替换模板内容，保持模板标识、名称、层次及全部绑定
// 源页及其他文档和历史版本保持不变，整个替换仅产生一条撤销记录
// 入参: id 目标模板标识, page 普通源页面索引
// 返回: error 标识、复制或提交错误
func (e *Editor) ReplaceTemplateFromPage(id string, page int) error {
	return e.Transaction(func(_ *Editor) error {
		list, err := e.Templates()
		if err != nil {
			return err
		}
		found := false
		for _, item := range list {
			if sameResourceID(item.ID, id) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("template %q not found", id)
		}
		created, err := e.CreateTemplateFromPage(page, "", "")
		if err != nil {
			return err
		}
		r, err := e.reader(nil)
		if err != nil {
			return err
		}
		defer r.Close()
		data, err := r.readFile(r.documentRoot)
		if err != nil {
			return err
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return err
		}
		common := root.child("CommonData")
		old, err := editorFindStructureID(common, "TemplatePage", "ID", id)
		if err != nil {
			return err
		}
		next, err := editorFindStructureID(common, "TemplatePage", "ID", created)
		if err != nil {
			return err
		}
		patch, err := editorXMLAttributePatch(data, old, "BaseLoc", next.attr("BaseLoc"))
		if err != nil {
			return err
		}
		data = editorPatchXML(data, []editorXMLPatch{patch, {next.start, next.end, nil}})
		return e.commitLayerTemplateParts(r, map[string][]byte{r.documentRoot: data})
	})
}

// DeleteTemplate 删除当前文档未被普通页面引用的模板定义，保留可能被其他文档、版本或扩展引用的文件
// 入参: id 模板标识
// 返回: error 标识、被引用或结构错误
func (e *Editor) DeleteTemplate(id string) error {
	return e.editTemplateDefinition(id, func(r *Reader, data []byte, node *editorXML) ([]byte, error) {
		doc, err := r.Doc()
		if err != nil {
			return nil, err
		}
		for i := range doc.Pages.Page {
			page, err := r.PageContentByIndex(i)
			if err != nil {
				return nil, err
			}
			for _, ref := range page.Template {
				if sameResourceID(ref.TemplateID, id) {
					return nil, fmt.Errorf("template %q is referenced by page %d", id, i)
				}
			}
		}
		return editorPatchXML(data, []editorXMLPatch{{node.start, node.end, nil}}), nil
	})
}

// Templates 直接返回当前文档及版本的模板清单，不创建编辑器，省略的ZOrder返回Background
// 返回: []TemplatePage 独立模板定义副本, error 文档读取错误
func (r *Reader) Templates() ([]TemplatePage, error) {
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	result := append([]TemplatePage{}, doc.CommonData.TemplatePage...)
	for i := range result {
		result[i].ZOrder = editorTemplateOrder(result[i].ZOrder)
	}
	return result, nil
}

// TemplateReferences 只读枚举当前文档及所选版本全部普通页面的模板引用，不创建编辑器或遍历其他文档、版本
// 返回独立副本，按页面索引及页内引用文件顺序排列；无引用时返回空切片，读取失败时不返回部分结果
// 返回: []TemplateReference 引用定位及绑定层次清单, error 文档或页面读取错误
func (r *Reader) TemplateReferences() ([]TemplateReference, error) {
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	result := []TemplateReference{}
	for i, item := range doc.Pages.Page {
		page, err := r.PageContentByIndex(i)
		if err != nil {
			return nil, err
		}
		for _, ref := range page.Template {
			result = append(result, TemplateReference{PageIndex: i, PageID: item.ID, Template: ref})
		}
	}
	return result, nil
}

// editorTemplateOrder 将省略的模板默认层次规范化为Background
// 入参: value 原始层次
// 返回: string 有效默认层次
func editorTemplateOrder(value string) string {
	if value == "" {
		return "Background"
	}
	return value
}

// validEditorTemplateOrder 校验模板前景背景枚举及未指定状态
// 入参: value 层次值
// 返回: bool 是否有效
func validEditorTemplateOrder(value string) bool {
	return value == "" || value == "Background" || value == "Foreground"
}

// editorTemplateDefinition 查找当前阅读器中的唯一模板定义，拒绝缺失及歧义标识
// 入参: reader 当前阅读器, id 模板标识
// 返回: TemplatePage 定义副本, error 文档或标识错误
func editorTemplateDefinition(reader *Reader, id string) (TemplatePage, error) {
	doc, err := reader.Doc()
	if err != nil {
		return TemplatePage{}, err
	}
	var result TemplatePage
	for _, item := range doc.CommonData.TemplatePage {
		if sameResourceID(item.ID, id) {
			if result.ID != "" {
				return TemplatePage{}, fmt.Errorf("ambiguous template ID %q", id)
			}
			result = item
		}
	}
	if result.ID == "" {
		return result, fmt.Errorf("template %q not found", id)
	}
	return result, nil
}

// editTemplateDefinition 在事务快照中局部修改模板声明，保留命名空间、扩展及其他版本
// 入参: id 模板标识, edit 文档原文修改回调
// 返回: error 读取、修改或提交错误
func (e *Editor) editTemplateDefinition(id string, edit func(*Reader, []byte, *editorXML) ([]byte, error)) error {
	return e.Transaction(func(_ *Editor) error {
		r, err := e.reader(nil)
		if err != nil {
			return err
		}
		defer r.Close()
		data, err := r.readFile(r.documentRoot)
		if err != nil {
			return err
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return err
		}
		node, err := editorFindStructureID(root.child("CommonData"), "TemplatePage", "ID", id)
		if err != nil {
			return err
		}
		next, err := edit(r, data, node)
		if err != nil || bytes.Equal(data, next) {
			return err
		}
		return e.commitLayerTemplateParts(r, map[string][]byte{r.documentRoot: next})
	})
}
