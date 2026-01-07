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
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strings"
)

// Reader OFD文件阅读器
type Reader struct {
	Path                      string
	Zip                       *zip.Reader
	Closer                    io.Closer
	OFD                       *OFD
	RootDir                   string
	ResMap                    map[string]string
	fontCache                 map[string]*Font
	drawParamCache            map[string]*DrawParam
	compositeGraphicUnitCache map[string]*CompositeGraphicUnit
	doc                       *Document
	Stamps                    map[string][]Stamp
}

// Close 关闭阅读器
// 返回: error 错误信息
func (r *Reader) Close() error {
	if r.Closer != nil {
		return r.Closer.Close()
	}
	return nil
}

// initRoot 读取根节点信息
// 返回: error 错误信息
func (r *Reader) initRoot() error {
	data, err := r.readFile("OFD.xml")
	if err != nil {
		return fmt.Errorf("failed to read ofd.xml: %w", err)
	}
	var ofd OFD
	if err := xml.Unmarshal(data, &ofd); err != nil {
		return fmt.Errorf("failed to unmarshal ofd.xml: %w", err)
	}
	r.OFD = &ofd
	r.ResMap = make(map[string]string)
	r.fontCache = make(map[string]*Font)
	r.drawParamCache = make(map[string]*DrawParam)
	r.compositeGraphicUnitCache = make(map[string]*CompositeGraphicUnit)
	return nil
}

// readFile 读取压缩包内的文件
// 入参: name 文件名
// 返回: []byte 文件内容, error 错误信息
func (r *Reader) readFile(name string) ([]byte, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimPrefix(name, "/")
	for _, f := range r.Zip.File {
		if f.Name == name {
			return readZipFile(f)
		}
	}
	return nil, fmt.Errorf("file not found: %s", name)
}

// openFile 打开压缩包内的文件流
// 入参: name 文件名
// 返回: io.ReadCloser 文件流, error 错误信息
func (r *Reader) openFile(name string) (io.ReadCloser, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimPrefix(name, "/")
	for _, f := range r.Zip.File {
		if f.Name == name {
			return f.Open()
		}
	}
	return nil, fmt.Errorf("file not found: %s", name)
}

// readZipFile 读取zip文件内容
// 入参: f zip文件对象
// 返回: []byte 文件内容, error 错误信息
func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// Doc 获取主文档结构
// 返回: *Document 文档结构, error 错误信息
func (r *Reader) Doc() (*Document, error) {
	if r.doc != nil {
		return r.doc, nil
	}
	if r.OFD == nil || len(r.OFD.DocBody) == 0 {
		return nil, fmt.Errorf("no docbody found")
	}
	docAttr := r.OFD.DocBody[0]
	docRootPath := docAttr.DocRoot
	r.RootDir = path.Dir(docRootPath)
	data, err := r.readFile(docRootPath)
	if err != nil {
		return nil, err
	}
	var doc Document
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal document.xml: %w", err)
	}
	r.OFD.DocBody[0].DocInfo.DocID = "loaded"
	if doc.CommonData.DocumentRes != "" {
		r.loadRes(doc.CommonData.DocumentRes)
	}
	if doc.CommonData.PublicRes != "" {
		r.loadRes(doc.CommonData.PublicRes)
	}
	r.doc = &doc
	_ = r.parseSignatures(&doc)
	return r.doc, nil
}

// loadRes 加载资源文件
// 入参: resPath 资源路径
func (r *Reader) loadRes(resPath string) {
	if resPath == "" {
		return
	}
	fullPath := path.Join(r.RootDir, resPath)
	data, err := r.readFile(fullPath)
	if err != nil {
		return
	}
	var res Res
	if err := xml.Unmarshal(data, &res); err != nil {
		return
	}
	baseLoc := res.BaseLoc
	for _, mm := range res.MultiMedias.MultiMedia {
		if mm.MediaFile != "" {
			p := strings.TrimSpace(mm.MediaFile)
			if p == "" {
				continue
			}
			dir := path.Dir(resPath)
			if baseLoc != "" {
				if dir != baseLoc {
					dir = path.Join(dir, baseLoc)
				}
			}
			finalPath := path.Join(dir, p)
			r.ResMap[mm.ID] = finalPath
		}
	}
	for i := range res.Fonts.Font {
		f := &res.Fonts.Font[i]
		if f.FontFile != "" {
			dir := path.Dir(resPath)
			if baseLoc != "" {
				if dir != baseLoc {
					dir = path.Join(dir, baseLoc)
				}
			}
			f.FontFile = path.Join(dir, f.FontFile)
		}
		r.fontCache[f.ID] = f
	}
	for i := range res.DrawParams.DrawParam {
		dp := &res.DrawParams.DrawParam[i]
		r.drawParamCache[dp.ID] = dp
	}
	for i := range res.CompositeGraphicUnits.CompositeGraphicUnit {
		cgu := &res.CompositeGraphicUnits.CompositeGraphicUnit[i]
		r.compositeGraphicUnitCache[cgu.ID] = cgu
	}
}

// PageContent 获取页面内容
// 入参: page 页面对象
// 返回: *PageContent 页面内容, error 错误信息
func (r *Reader) PageContent(page Page) (*PageContent, error) {
	fullPath := path.Join(r.RootDir, page.BaseLoc)
	data, err := r.readFile(fullPath)
	if err != nil {
		return nil, err
	}
	var content PageContent
	if err := xml.Unmarshal(data, &content); err != nil {
		return nil, fmt.Errorf("failed to unmarshal page content: %w", err)
	}
	content.ID = page.ID
	return &content, nil
}

// ResPath 获取资源的完整路径
// 入参: resLink 资源链接
// 返回: string 完整路径
func (r *Reader) ResPath(resLink string) string {
	if resLink == "" {
		return ""
	}
	return path.Join(r.RootDir, resLink)
}

// ResData 获取资源文件数据
// 入参: resLink 资源链接
// 返回: []byte 资源数据, error 错误信息
func (r *Reader) ResData(resLink string) ([]byte, error) {
	fullPath := r.ResPath(resLink)
	return r.readFile(fullPath)
}

// DocRoots 获取所有文档根路径
// 返回: []string 路径列表
func (r *Reader) DocRoots() []string {
	var roots []string
	if r.OFD == nil {
		return roots
	}
	for _, body := range r.OFD.DocBody {
		roots = append(roots, body.DocRoot)
	}
	return roots
}

// Version 获取OFD版本号
// 返回: string 版本号
func (r *Reader) Version() string {
	if r.OFD == nil {
		return ""
	}
	return r.OFD.Version
}

// DocType 获取文档类型
// 返回: string 文档类型
func (r *Reader) DocType() string {
	if r.OFD == nil {
		return ""
	}
	return r.OFD.DocType
}

// DocInfo 获取文档元数据
// 返回: *DocInfo 元数据, error 错误信息
func (r *Reader) DocInfo() (*DocInfo, error) {
	if r.OFD == nil || len(r.OFD.DocBody) == 0 {
		return nil, fmt.Errorf("no docbody found")
	}
	return &r.OFD.DocBody[0].DocInfo, nil
}

// Permissions 获取文档权限信息
// 返回: *Permissions 权限信息, error 错误信息
func (r *Reader) Permissions() (*Permissions, error) {
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	return &doc.Permissions, nil
}

// Outlines 获取文档大纲
// 返回: []OutlineElem 大纲列表, error 错误信息
func (r *Reader) Outlines() ([]OutlineElem, error) {
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	return doc.Outlines.OutlineElem, nil
}

// Attachments 获取附件列表
// 返回: []Attachment 附件列表, error 错误信息
func (r *Reader) Attachments() ([]Attachment, error) {
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	return doc.Attachments.Attachment, nil
}

// CustomDatas 获取自定义数据
// 返回: []CustomData 自定义数据列表, error 错误信息
func (r *Reader) CustomDatas() ([]CustomData, error) {
	info, err := r.DocInfo()
	if err != nil {
		return nil, err
	}
	if info.CustomDatas == nil {
		return nil, nil
	}
	return info.CustomDatas.CustomData, nil
}

// Extensions 获取分列扩展项
// 返回: []Extension 扩展项列表, error 错误信息
func (r *Reader) Extensions() ([]Extension, error) {
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	return doc.Extensions.Extension, nil
}
