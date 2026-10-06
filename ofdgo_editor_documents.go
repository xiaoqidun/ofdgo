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
	"context"
	"encoding/xml"
	"fmt"
	"maps"
	"path"
	"reflect"
	"slices"
	"strings"
)

// DocumentCount 获取编辑包内的文档数量，新建编辑器包含一个当前文档
// 返回: int 文档数量
func (e *Editor) DocumentCount() int {
	if e.source == nil {
		return 1
	}
	return e.source.reader.DocumentCount()
}

// DocumentIndex 获取当前编辑文档索引，从0开始
// 返回: int 文档索引
func (e *Editor) DocumentIndex() int {
	if e.source == nil {
		return 0
	}
	return e.source.reader.DocumentIndex()
}

// DocumentInfo 获取指定文档信息的独立副本，不切换当前文档
// 入参: index 文档索引，从0开始
// 返回: DocInfo 文档信息, error 索引错误
func (e *Editor) DocumentInfo(index int) (DocInfo, error) {
	if index < 0 || index >= e.DocumentCount() {
		return DocInfo{}, fmt.Errorf("document index %d out of range", index)
	}
	if index == e.DocumentIndex() {
		return cloneEditorData(e.Info), nil
	}
	info := cloneEditorData(e.source.reader.OFD.DocBody[index].DocInfo)
	if info.CustomDatas != nil {
		for i := range info.CustomDatas.CustomData {
			info.CustomDatas.CustomData[i].sourceIndex = i + 1
		}
	}
	return info, nil
}

// SetDocumentInfo 修改指定文档信息，保留扩展内容，不切换当前文档，可通过Undo撤销
// 入参: index 文档索引，从0开始, info 文档信息
// 返回: error 修改错误
func (e *Editor) SetDocumentInfo(index int, info DocInfo) error {
	before, err := e.DocumentInfo(index)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(before, info) {
		return nil
	}
	if index == e.DocumentIndex() {
		e.SetInfo(info)
		return nil
	}
	return e.editDocuments(func(reader *Reader, data []byte, _ *editorXML) (int, error) {
		updated, err := editorUpdateInfoXML(data, NewEditor().Info.DocID, index, before, info)
		if err != nil {
			return 0, err
		}
		reader.files[reader.fileNamesFold["ofd.xml"]] = updated
		return reader.DocumentIndex(), nil
	})
}

// AddDocument 在包内插入含一页的空白文档并选中，不修改输入包
// 当前文档的修改保留，新文档使用独立目录和文档标识；可通过Undo撤销
// 入参: title 文档标题, width 页面宽度, height 页面高度, at 插入位置，从0开始，可等于文档数量
// 返回: int 新文档索引, error 创建错误
func (e *Editor) AddDocument(title string, width, height float64, at int) (int, error) {
	if at < 0 || at > e.DocumentCount() {
		return 0, fmt.Errorf("document index %d out of range", at)
	}
	created := NewEditor()
	created.Info.Title = title
	if _, err := created.AddPage(width, height); err != nil {
		return 0, err
	}
	err := e.editDocuments(func(reader *Reader, data []byte, root *editorXML) (int, error) {
		directory := ""
		for number := 0; ; number++ {
			candidate := fmt.Sprintf("Doc_%d", number)
			occupied := false
			for name := range reader.fileNamesFold {
				if name == strings.ToLower(candidate) || strings.HasPrefix(name, strings.ToLower(candidate)+"/") {
					occupied = true
					break
				}
			}
			if !occupied {
				directory = candidate
				break
			}
		}
		createdReader, err := created.Reader()
		if err != nil {
			return 0, err
		}
		for name, content := range createdReader.files {
			if name == "OFD.xml" {
				continue
			}
			reader.files[directory+strings.TrimPrefix(name, "Doc_0")] = content
		}
		body, err := encodeOFDXML(func(x *ofdXML) {
			x.root("DocBody", nil)
			x.start("DocInfo", nil)
			for _, field := range [][2]string{{"DocID", created.Info.DocID}, {"Title", title}, {"CreationDate", created.Info.CreationDate}, {"Creator", ofdCreator}} {
				if field[1] != "" {
					x.text(field[0], field[1])
				}
			}
			x.end("DocInfo")
			x.text("DocRoot", directory+"/Document.xml")
			x.end("DocBody")
		})
		if err != nil {
			return 0, err
		}
		position := root.close
		if existing := root.childAt("DocBody", at); existing != nil {
			position = existing.start
		}
		reader.files[reader.fileNamesFold["ofd.xml"]] = editorPatchXML(data, []editorXMLPatch{{position, position, bytes.TrimPrefix(body, []byte(xml.Header))}})
		return at, nil
	})
	if err != nil {
		return 0, err
	}
	return at, nil
}

// RenameDocument 修改指定文档标题，保留其余元数据和内容，可通过Undo撤销
// 入参: index 文档索引，从0开始, title 标题，空值移除标题
// 返回: error 修改错误
func (e *Editor) RenameDocument(index int, title string) error {
	info, err := e.DocumentInfo(index)
	if err != nil {
		return err
	}
	info.Title = title
	return e.SetDocumentInfo(index, info)
}

// MoveDocument 调整包内文档顺序，当前文档随条目移动，不重写内容，可通过Undo撤销
// 入参: from 原文档索引, to 目标索引，均从0开始
// 返回: error 移动错误
func (e *Editor) MoveDocument(from, to int) error {
	if from < 0 || from >= e.DocumentCount() || to < 0 || to >= e.DocumentCount() {
		return fmt.Errorf("document index out of range")
	}
	if from == to {
		return nil
	}
	return e.editDocuments(func(reader *Reader, data []byte, root *editorXML) (int, error) {
		bodies := make([][]byte, reader.DocumentCount())
		for index := range bodies {
			body := root.childAt("DocBody", index)
			bodies[index] = data[body.start:body.end]
		}
		moveEditorItem(bodies, from, to)
		var patches []editorXMLPatch
		for index, content := range bodies {
			body := root.childAt("DocBody", index)
			patches = append(patches, editorXMLPatch{body.start, body.end, content})
		}
		reader.files[reader.fileNamesFold["ofd.xml"]] = editorPatchXML(data, patches)
		selected := reader.DocumentIndex()
		if selected == from {
			selected = to
		} else if from < selected && to >= selected {
			selected--
		} else if from > selected && to <= selected {
			selected++
		}
		return selected, nil
	})
}

// DeleteDocument 删除指定文档并清理独占文件，保留其他文档的共享资源，可通过Undo撤销
// 包内至少保留一个文档；删除当前文档时选中后一个，末尾则选中前一个
// 原签名保留，修改根索引或受保护文件可能使原签名失效
// 入参: index 文档索引，从0开始
// 返回: error 删除错误
func (e *Editor) DeleteDocument(index int) error {
	if index < 0 || index >= e.DocumentCount() {
		return fmt.Errorf("document index %d out of range", index)
	}
	if e.DocumentCount() == 1 {
		return fmt.Errorf("package must contain at least one document")
	}
	return e.editDocuments(func(reader *Reader, data []byte, root *editorXML) (int, error) {
		removed, err := reader.documentFiles(context.Background(), index)
		if err != nil {
			return 0, err
		}
		directory := path.Dir(cleanPackagePath(reader.OFD.DocBody[index].DocRoot))
		if directory != "." && !slices.ContainsFunc(reader.OFD.DocBody, func(body DocBody) bool {
			name := cleanPackagePath(body.DocRoot)
			return name != cleanPackagePath(reader.OFD.DocBody[index].DocRoot) && strings.HasPrefix(strings.ToLower(name), strings.ToLower(directory)+"/")
		}) {
			for name := range reader.fileNamesFold {
				if strings.HasPrefix(name, strings.ToLower(directory)+"/") {
					removed[name] = true
				}
			}
		}
		for other := range reader.OFD.DocBody {
			if other == index {
				continue
			}
			kept, err := reader.documentFiles(context.Background(), other)
			if err != nil {
				return 0, err
			}
			for name := range kept {
				delete(removed, name)
			}
		}
		delete(removed, "ofd.xml")
		for name := range reader.files {
			if removed[strings.ToLower(name)] {
				delete(reader.files, name)
			}
		}
		if reader.Zip != nil {
			reader.Zip = &zip.Reader{Comment: reader.Zip.Comment, File: slices.DeleteFunc(slices.Clone(reader.Zip.File), func(file *zip.File) bool {
				return removed[strings.ToLower(cleanPackagePath(file.Name))]
			})}
		}
		body := root.childAt("DocBody", index)
		reader.files[reader.fileNamesFold["ofd.xml"]] = editorPatchXML(data, []editorXMLPatch{{start: body.start, end: body.end}})
		selected := reader.DocumentIndex()
		if selected > index {
			selected--
		} else if selected == index {
			selected = min(index, reader.DocumentCount()-2)
		}
		return selected, nil
	})
}

// editDocuments 在独立包快照中修改文档入口，失败时不改变当前状态或历史
// 入参: edit 修改包快照并返回新的当前文档索引
// 返回: error 修改错误
func (e *Editor) editDocuments(edit func(*Reader, []byte, *editorXML) (int, error)) error {
	reader, err := e.Reader()
	if err != nil {
		return err
	}
	data, err := reader.readFile("OFD.xml")
	if err != nil {
		return err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return err
	}
	reader.files = maps.Clone(reader.files)
	index, err := edit(reader, data, root)
	if err != nil {
		return err
	}
	before := e.transactionSnapshot()
	reader.documentIndex, reader.doc = index, nil
	if err := reader.initRoot(); err != nil {
		return err
	}
	if _, err := reader.Doc(); err != nil {
		return err
	}
	if e.source != nil && e.documentRoot() == reader.OFD.DocBody[index].DocRoot && e.Info.DocID == reader.OFD.DocBody[index].DocInfo.DocID {
		source := *e.source
		source.reader = reader
		e.source = &source
	} else {
		next, err := reader.Editor()
		if err != nil {
			return err
		}
		next.history, next.historyIndex, next.historyLimit = e.history, e.historyIndex, e.historyLimit
		next.revision, next.serial = e.revision, e.serial
		next.OnWriteProgress, next.output = e.OnWriteProgress, e.output
		next.encryption = e.encryption
		next.backends, next.fontDirs, next.fontFS = e.backends, e.fontDirs, e.fontFS
		*e = *next
	}
	e.recordTransaction(before)
	return nil
}

// documentRoot 获取当前文档的包内入口，区分不同文档的资源和标识域
// 返回: string 文档入口，新建单文档编辑器为空
func (e *Editor) documentRoot() string {
	if e.source == nil {
		return ""
	}
	return e.source.reader.OFD.DocBody[e.DocumentIndex()].DocRoot
}

// sameDocument 判断快照是否属于同一文档，区别删除后复用目录的新文档
// 入参: other 编辑快照
// 返回: bool 是否为同一文档
func (e *Editor) sameDocument(other *Editor) bool {
	if e.source == nil || other.source == nil {
		if e.source == nil && other.source == nil {
			return true
		}
		fresh, packaged := e, other
		if fresh.source != nil {
			fresh, packaged = other, e
		}
		return len(packaged.source.reader.OFD.DocBody) == 1 && (packaged.source.fromNew || fresh.Info.DocID != "" && fresh.Info.DocID == packaged.source.info.DocID)
	}
	return e.documentRoot() == other.documentRoot() && e.source.info.DocID == other.source.info.DocID
}
