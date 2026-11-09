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
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strings"
)

// ErrIncompleteDocument 表示渐进阅读快照尚未包含请求的页面或完整文档
var ErrIncompleteDocument = errors.New("document content is incomplete")

// Reader OFD文件阅读器
// Reader及共享它的Renderer需串行调用
type Reader struct {
	Path                      string
	Zip                       *zip.Reader
	Closer                    io.Closer
	OFD                       *OFD
	RootDir                   string
	ResMap                    map[string]string
	resources                 *Resources
	pdfDestinations           *pdfDestinations
	resourcesRead             map[string]bool
	resourceFiles             map[string]string
	imageCatalog              []ImageInfo
	resourceInfoCache         []ResourceInfo
	mediaCache                map[string]MultiMedia
	fontCache                 map[string]*Font
	fontResourcesRead         bool
	fontFaces                 map[string][]*FontFace
	colorSpaceCache           map[string]*ColorSpace
	drawParamCache            map[string]*DrawParam
	compositeGraphicUnitCache map[string]*CompositeGraphicUnit
	pageHeaderCache           map[string]PageContent
	pageReady                 map[string]bool
	pageContents              *renderCache[pageContentKey, PageContent]
	doc                       *Document
	relatedPending            bool
	documentIndex             int
	documentRoot              string
	selectedVersion           *string
	versionInfo               *DocumentVersionInfo
	Stamps                    map[string][]Stamp
	Annots                    map[string][]Annotation
	annotationFiles           map[string][]string
	fileIndex                 map[string]*zip.File
	files                     map[string][]byte
	fileNames                 map[string]bool
	encryption                *encryptionState
}

// pageContentKey 按页面原文及解析模式区分缓存，不包含文档资源和区域继承
type pageContentKey struct {
	digest   [32]byte
	textOnly bool
}

// documentPageList 仅读取文档中的页面索引，统计页数时不构建其他结构
type documentPageList struct {
	Pages Pages
}

// UnmarshalXML 按文档命名空间读取页面列表，跳过其他文档结构和扩展
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (list *documentPageList) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "Document" {
		return fmt.Errorf("expected Document root, got %s", start.Name.Local)
	}
	*list = documentPageList{}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local == "Pages" {
			return d.DecodeElement(&list.Pages, &node)
		}
		return d.Skip()
	})
}

// Close 关闭阅读器持有的文件，重复调用不再关闭底层文件
// 返回: error 错误信息
func (r *Reader) Close() error {
	if r.Closer != nil {
		closer := r.Closer
		r.Closer = nil
		return closer.Close()
	}
	return nil
}

// Fork 创建保留当前文档及历史版本的独立只读视图，不复制包内文件数据
// 创建过程需与原实例串行，返回后可并行读取，公开结构及底层输入不得并发修改
// 返回值不持有输入所有权，全部视图使用结束前不得关闭或替换原输入
// 入参: ctx 取消上下文
// 返回: *Reader 独立阅读器, error 准备或取消错误
func (r *Reader) Fork(ctx context.Context) (*Reader, error) {
	if _, err := r.DocContext(ctx); err != nil {
		return nil, err
	}
	if r.pageContents == nil {
		r.pageContents = resourceCache[pageContentKey, PageContent](r.resources, 16<<20)
	}
	next := *r
	next.Closer = nil
	next.resourcesRead = maps.Clone(r.resourcesRead)
	next.resourceFiles = maps.Clone(r.resourceFiles)
	next.ResMap = maps.Clone(r.ResMap)
	next.mediaCache = maps.Clone(r.mediaCache)
	next.fontCache = maps.Clone(r.fontCache)
	next.fontFaces = maps.Clone(r.fontFaces)
	next.colorSpaceCache = maps.Clone(r.colorSpaceCache)
	next.drawParamCache = maps.Clone(r.drawParamCache)
	next.compositeGraphicUnitCache = maps.Clone(r.compositeGraphicUnitCache)
	next.pageHeaderCache = maps.Clone(r.pageHeaderCache)
	next.Annots = maps.Clone(r.Annots)
	next.Stamps = maps.Clone(r.Stamps)
	next.annotationFiles = maps.Clone(r.annotationFiles)
	return &next, ctx.Err()
}

// DocumentCount 获取包内文档数量
// 返回: int 文档数量
func (r *Reader) DocumentCount() int {
	if r.OFD == nil {
		return 0
	}
	return len(r.OFD.DocBody)
}

// DocumentIndex 获取当前文档索引，从0开始
// 返回: int 文档索引
func (r *Reader) DocumentIndex() int {
	return r.documentIndex
}

// DocumentPageCount 获取指定文档页数，不切换当前文档或加载资源、注释和签名
// 入参: index 文档索引，从0开始
// 返回: int 页数, error 错误信息
func (r *Reader) DocumentPageCount(index int) (int, error) {
	return r.DocumentPageCountContext(context.Background(), index)
}

// DocumentPageCountContext 获取指定文档页数，读取和解析时响应取消，不加载关联资源
// 入参: ctx 取消上下文, index 文档索引，从0开始
// 返回: int 页数, error 选择、读取或取消错误
func (r *Reader) DocumentPageCountContext(ctx context.Context, index int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if index < 0 || index >= r.DocumentCount() {
		return 0, fmt.Errorf("document index %d out of range", index)
	}
	if index == r.documentIndex && r.doc != nil {
		return len(r.doc.Pages.Page), nil
	}
	view := *r
	root, err := view.documentEntryContext(ctx, index)
	if err != nil {
		return 0, err
	}
	data, err := r.readFileViewContext(ctx, root)
	if err != nil {
		return 0, err
	}
	var doc documentPageList
	decoder := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
	if err := decoder.Decode(&doc); err != nil {
		return 0, fmt.Errorf("failed to unmarshal document.xml: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if index == r.documentIndex {
		r.documentRoot, r.versionInfo = view.documentRoot, view.versionInfo
	}
	return len(doc.Pages.Page), nil
}

// Document 创建指定文档的独立阅读器，不改变当前文档或混用资源缓存
// 返回的阅读器不关闭输入文件，使用期间不得关闭持有输入文件的原阅读器
// 入参: index 文档索引，从0开始
// 返回: *Reader 文档阅读器, error 错误信息
func (r *Reader) Document(index int) (*Reader, error) {
	return r.DocumentContext(context.Background(), index)
}

// DocumentContext 创建指定文档的独立阅读器，读取结构和关联内容时响应取消
// 返回值不关闭输入文件，使用期间不得关闭持有输入文件的原阅读器
// 入参: ctx 取消上下文, index 文档索引，从0开始
// 返回: *Reader 文档阅读器, error 读取或取消错误
func (r *Reader) DocumentContext(ctx context.Context, index int) (*Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if index < 0 || index >= r.DocumentCount() {
		return nil, fmt.Errorf("document index %d out of range", index)
	}
	next := &Reader{Path: r.Path, Zip: r.Zip, files: r.files, encryption: r.encryption, documentIndex: index, resources: r.resources, pageReady: r.pageReady, pdfDestinations: r.pdfDestinations.clone()}
	if err := next.initRootContext(ctx); err != nil {
		return nil, err
	}
	if _, err := next.DocContext(ctx); err != nil {
		return nil, err
	}
	return next, nil
}

// Doc 获取当前文档结构
// 返回: *Document 文档结构, error 错误信息
func (r *Reader) Doc() (*Document, error) {
	return r.DocContext(context.Background())
}

// DocContext 获取当前文档结构及关联内容，取消或失败时保留原阅读状态
// 入参: ctx 取消上下文
// 返回: *Document 文档结构, error 读取或取消错误
func (r *Reader) DocContext(ctx context.Context) (*Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.doc != nil && !r.relatedPending {
		return r.doc, nil
	}
	view := *r
	if r.doc == nil {
		view.resourcesRead = maps.Clone(r.resourcesRead)
		view.resourceFiles = maps.Clone(r.resourceFiles)
		view.ResMap = maps.Clone(r.ResMap)
		view.mediaCache = maps.Clone(r.mediaCache)
		view.fontCache = maps.Clone(r.fontCache)
		view.colorSpaceCache = maps.Clone(r.colorSpaceCache)
		view.drawParamCache = maps.Clone(r.drawParamCache)
		view.compositeGraphicUnitCache = maps.Clone(r.compositeGraphicUnitCache)
	}
	doc, err := view.docStructureContext(ctx)
	if err != nil {
		return nil, err
	}
	if view.relatedPending {
		view.Annots = maps.Clone(r.Annots)
		for page, entries := range view.Annots {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			view.Annots[page] = slices.Clone(entries)
		}
		view.Stamps = maps.Clone(r.Stamps)
		for page, entries := range view.Stamps {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			view.Stamps[page] = slices.Clone(entries)
		}
		_ = view.parseAnnotations(ctx, doc)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		_ = view.parseSignatures(ctx, doc)
		view.relatedPending = false
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.doc == nil {
		r.commitDocumentStructure(&view)
	} else {
		r.relatedPending = view.relatedPending
	}
	r.Annots, r.Stamps, r.annotationFiles = view.Annots, view.Stamps, view.annotationFiles
	return doc, nil
}

// docStructure 读取文档及资源索引，关联注解和签名由Doc按需解析
// 返回: *Document 文档结构, error 错误信息
func (r *Reader) docStructure() (*Document, error) {
	return r.docStructureContext(context.Background())
}

// docStructureContext 读取文档及资源索引，取消或失败时保留原阅读状态
// 入参: ctx 取消上下文
// 返回: *Document 文档结构, error 读取、解析或取消错误
func (r *Reader) docStructureContext(ctx context.Context) (*Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.doc != nil {
		return r.doc, nil
	}
	if r.OFD == nil || r.documentIndex < 0 || r.documentIndex >= len(r.OFD.DocBody) {
		return nil, fmt.Errorf("no docbody found")
	}
	docAttr := r.OFD.DocBody[r.documentIndex]
	view := *r
	docRootPath, err := view.DocumentRootContext(ctx)
	if err != nil {
		return nil, err
	}
	view.RootDir = path.Dir(docRootPath)
	data, err := view.readFileViewContext(ctx, docRootPath)
	if err != nil {
		return nil, err
	}
	var doc Document
	decoder := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal document.xml: %w", err)
	}
	doc.Signatures = view.versionSignatures(docAttr.Signatures)
	view.resourcesRead = maps.Clone(r.resourcesRead)
	view.resourceFiles = maps.Clone(r.resourceFiles)
	view.ResMap = maps.Clone(r.ResMap)
	view.mediaCache = maps.Clone(r.mediaCache)
	view.fontCache = maps.Clone(r.fontCache)
	view.colorSpaceCache = maps.Clone(r.colorSpaceCache)
	view.drawParamCache = maps.Clone(r.drawParamCache)
	view.compositeGraphicUnitCache = maps.Clone(r.compositeGraphicUnitCache)
	for _, res := range doc.CommonData.DocumentRes {
		if err := view.loadResContext(ctx, res); err != nil {
			return nil, err
		}
	}
	for _, res := range doc.CommonData.PublicRes {
		if err := view.loadResContext(ctx, res); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	view.doc, view.relatedPending = &doc, true
	r.commitDocumentStructure(&view)
	return r.doc, nil
}

// commitDocumentStructure 提交已完成的文档读取，保留渲染器共享的资源索引
// 入参: view 已验证的独立读取状态
func (r *Reader) commitDocumentStructure(view *Reader) {
	maps.Copy(r.resourcesRead, view.resourcesRead)
	maps.Copy(r.resourceFiles, view.resourceFiles)
	maps.Copy(r.ResMap, view.ResMap)
	maps.Copy(r.mediaCache, view.mediaCache)
	maps.Copy(r.fontCache, view.fontCache)
	maps.Copy(r.colorSpaceCache, view.colorSpaceCache)
	maps.Copy(r.drawParamCache, view.drawParamCache)
	maps.Copy(r.compositeGraphicUnitCache, view.compositeGraphicUnitCache)
	r.documentRoot, r.versionInfo, r.RootDir = view.documentRoot, view.versionInfo, view.RootDir
	r.doc, r.relatedPending = view.doc, view.relatedPending
}

// PageContent 获取页面内容
// 每次返回独立的页面内容，修改后可传入Renderer重绘，不会写回原文件
// 入参: page 页面对象
// 返回: *PageContent 页面内容, error 错误信息
func (r *Reader) PageContent(page Page) (*PageContent, error) {
	return r.PageContentContext(context.Background(), page)
}

// PageContentContext 读取独立页面内容，取消时不返回部分页面
// 入参: ctx 取消上下文, page 页面对象
// 返回: *PageContent 页面内容, error 读取、解析或取消错误
func (r *Reader) PageContentContext(ctx context.Context, page Page) (*PageContent, error) {
	return r.readPageContent(ctx, page, false, false)
}

// PageCount 获取当前文档总页数，不加载注解或签名
// 返回: int 页数, error 错误信息
func (r *Reader) PageCount() (int, error) {
	doc, err := r.docStructure()
	if err != nil {
		return 0, err
	}
	return len(doc.Pages.Page), nil
}

// PageContentByIndex 按当前文档页面顺序读取页面内容
// 入参: index 页面索引，从0开始
// 返回: *PageContent 页面内容, error 错误信息
func (r *Reader) PageContentByIndex(index int) (*PageContent, error) {
	return r.PageContentByIndexContext(context.Background(), index)
}

// PageContentByIndexContext 按当前文档页面顺序读取独立页面内容
// 入参: ctx 取消上下文, index 页面索引，从0开始
// 返回: *PageContent 页面内容, error 索引、读取或取消错误
func (r *Reader) PageContentByIndexContext(ctx context.Context, index int) (*PageContent, error) {
	doc, err := r.DocContext(ctx)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(doc.Pages.Page) {
		return nil, fmt.Errorf("page index %d out of range", index)
	}
	return r.PageContentContext(ctx, doc.Pages.Page[index])
}

// PageArea 读取页面区域，不解析页面图元
// 入参: page 页面对象
// 返回: PageArea 页面区域, error 错误信息
func (r *Reader) PageArea(page Page) (PageArea, error) {
	return r.PageAreaContext(context.Background(), page)
}

// PageAreaContext 读取页面区域并解析模板继承，不解析页面图元
// 入参: ctx 取消上下文, page 页面对象
// 返回: PageArea 页面区域, error 读取、解析或取消错误
func (r *Reader) PageAreaContext(ctx context.Context, page Page) (PageArea, error) {
	content, err := r.readPageHeaderContext(ctx, page)
	if err != nil {
		return PageArea{}, err
	}
	return r.resolvePageAreaContext(ctx, content.Area, content.Template)
}

// ResPath 按当前文档目录解析ST_Loc，包内绝对路径以根目录为基准
// 入参: resLink 资源链接
// 返回: string 完整路径
func (r *Reader) ResPath(resLink string) string {
	if resLink == "" {
		return ""
	}
	resLink = strings.ReplaceAll(resLink, "\\", "/")
	resLink = strings.TrimSpace(resLink)
	if resLink == "" {
		return ""
	}
	if strings.HasPrefix(resLink, "/") {
		return cleanPackagePath(resLink)
	}
	resLink = path.Clean(resLink)
	rootDir := strings.TrimPrefix(strings.ReplaceAll(r.RootDir, "\\", "/"), "/")
	return cleanPackagePath(path.Join(rootDir, resLink))
}

// ResData 获取资源文件数据
// 入参: resLink 资源链接
// 返回: []byte 资源数据, error 错误信息
func (r *Reader) ResData(resLink string) ([]byte, error) {
	fullPath := r.ResPath(resLink)
	return r.readFile(fullPath)
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
	if r.OFD == nil || r.documentIndex < 0 || r.documentIndex >= len(r.OFD.DocBody) {
		return nil, fmt.Errorf("no docbody found")
	}
	return &r.OFD.DocBody[r.documentIndex].DocInfo, nil
}

// Permissions 获取文档权限信息，不加载注解或签名
// 返回: *Permissions 权限信息, error 错误信息
func (r *Reader) Permissions() (*Permissions, error) {
	doc, err := r.docStructure()
	if err != nil {
		return nil, err
	}
	return &doc.Permissions, nil
}

// Outlines 获取文档大纲，不加载注解或签名
// 返回: []OutlineElem 大纲列表, error 错误信息
func (r *Reader) Outlines() ([]OutlineElem, error) {
	doc, err := r.docStructure()
	if err != nil {
		return nil, err
	}
	return doc.Outlines.OutlineElem, nil
}

// Actions 获取文档动作，不加载注解或签名
// 返回: []Action 动作列表, error 错误信息
func (r *Reader) Actions() ([]Action, error) {
	doc, err := r.docStructure()
	if err != nil {
		return nil, err
	}
	return doc.Actions, nil
}

// Bookmarks 获取文档书签，不加载注解或签名
// 返回: []Bookmark 书签列表, error 错误信息
func (r *Reader) Bookmarks() ([]Bookmark, error) {
	doc, err := r.docStructure()
	if err != nil {
		return nil, err
	}
	return doc.Bookmarks.Bookmark, nil
}

// Attachments 获取独立的附件列表，修改返回值不影响阅读器缓存
// 返回: []Attachment 附件列表, error 错误信息
func (r *Reader) Attachments() ([]Attachment, error) {
	return r.AttachmentsContext(context.Background())
}

// AttachmentsContext 获取独立的附件列表，不加载无关注解或签名
// 取消或失败时不缓存未完成的附件清单，修改返回值不影响缓存
// 入参: ctx 取消上下文
// 返回: []Attachment 附件列表, error 读取、解析或取消错误
func (r *Reader) AttachmentsContext(ctx context.Context) ([]Attachment, error) {
	return r.attachmentsContext(ctx, false)
}

// attachmentsContext 按调用方所有权读取附件清单，库内只读访问不复制完整清单
// 入参: ctx 取消上下文, readonly 是否允许借用只读清单
// 返回: []Attachment 附件列表, error 读取、解析或取消错误
func (r *Reader) attachmentsContext(ctx context.Context, readonly bool) ([]Attachment, error) {
	doc, err := r.docStructureContext(ctx)
	if err != nil {
		return nil, err
	}
	items := doc.Attachments.Attachment
	pending := strings.TrimSpace(doc.Attachments.Path) != "" && items == nil
	if pending {
		var attachments Attachments
		partPath, err := r.readDocumentPartContext(ctx, doc.Attachments.Path, &attachments)
		if err != nil {
			return nil, err
		}
		for i := range attachments.Attachment {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			attachment := &attachments.Attachment[i]
			if fileLoc := resolveResourcePath(partPath, "", attachment.FileLoc); fileLoc != "" {
				attachment.FileLoc = "/" + fileLoc
			}
		}
		if attachments.Attachment == nil {
			attachments.Attachment = []Attachment{}
		}
		items = attachments.Attachment
	}
	attachments := items
	if !readonly {
		if items != nil {
			attachments = make([]Attachment, len(items))
		}
		for i := range items {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			attachments[i] = items[i]
			if attachments[i].Size != nil {
				size := *attachments[i].Size
				attachments[i].Size = &size
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if pending {
		doc.Attachments.Attachment = items
	}
	return attachments, nil
}

// OpenAttachment 打开附件文件流，调用方负责关闭，完整读取后校验ZIP数据
// 入参: id 附件标识
// 返回: io.ReadCloser 附件文件流, error 错误信息
func (r *Reader) OpenAttachment(id string) (io.ReadCloser, error) {
	return r.OpenAttachmentContext(context.Background(), id)
}

// OpenAttachmentContext 打开可取消读取的附件文件流，调用方负责关闭
// 完整读取后校验ZIP数据，不加载无关注解或签名
// 入参: ctx 取消上下文, id 附件标识
// 返回: io.ReadCloser 附件文件流, error 读取或取消错误
func (r *Reader) OpenAttachmentContext(ctx context.Context, id string) (io.ReadCloser, error) {
	attachments, err := r.attachmentsContext(ctx, true)
	if err != nil {
		return nil, err
	}
	for _, attachment := range attachments {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if attachment.ID == id {
			stream, err := r.openFile(r.ResPath(attachment.FileLoc))
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				stream.Close()
				return nil, err
			}
			return imageInput{ReadCloser: stream, ctx: ctx}, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("attachment not found: %s", id)
}

// AttachmentData 获取附件文件数据
// 入参: id 附件标识
// 返回: []byte 附件数据, error 错误信息
func (r *Reader) AttachmentData(id string) ([]byte, error) {
	return r.AttachmentDataContext(context.Background(), id)
}

// AttachmentDataContext 获取独立的附件数据，读取失败或取消时不返回部分内容
// 入参: ctx 取消上下文, id 附件标识
// 返回: []byte 附件数据, error 读取或取消错误
func (r *Reader) AttachmentDataContext(ctx context.Context, id string) ([]byte, error) {
	stream, err := r.OpenAttachmentContext(ctx, id)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	data, err := io.ReadAll(stream)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

// CustomTags 获取自定义标引
// 返回的文件路径可直接传入ResData读取
// 返回: []CustomTag 自定义标引列表, error 错误信息
func (r *Reader) CustomTags() ([]CustomTag, error) {
	return r.CustomTagsContext(context.Background())
}

// CustomTagsContext 读取当前文档及版本的标引清单，取消或失败时不缓存未完成的清单
// 不加载标引内容、页面、注解或签名；返回的文件路径可直接传入ResData读取
// 入参: ctx 取消上下文
// 返回: []CustomTag 自定义标引列表, error 读取、解析或取消错误
func (r *Reader) CustomTagsContext(ctx context.Context) ([]CustomTag, error) {
	doc, err := r.docStructureContext(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(doc.CustomTags.Path) != "" && doc.CustomTags.CustomTag == nil {
		var customTags CustomTags
		partPath, err := r.readDocumentPartContext(ctx, doc.CustomTags.Path, &customTags)
		if err != nil {
			return nil, err
		}
		for i := range customTags.CustomTag {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			tag := &customTags.CustomTag[i]
			tag.SchemaLoc = r.resourceLocation(partPath, "", tag.SchemaLoc)
			tag.FileLoc = r.resourceLocation(partPath, "", tag.FileLoc)
		}
		if customTags.CustomTag == nil {
			customTags.CustomTag = []CustomTag{}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		doc.CustomTags.CustomTag = customTags.CustomTag
	}
	return doc.CustomTags.CustomTag, nil
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

// Extensions 获取扩展项，不加载注解或签名
// 返回的文件路径可直接传入ResData读取
// 返回: []Extension 扩展项列表, error 错误信息
func (r *Reader) Extensions() ([]Extension, error) {
	return r.ExtensionsContext(context.Background())
}

// ExtensionsContext 读取当前文档及版本的扩展项，取消或失败时不缓存未完成清单
// 入参: ctx 取消上下文
// 返回: []Extension 扩展项列表, error 读取、解析或取消错误
func (r *Reader) ExtensionsContext(ctx context.Context) ([]Extension, error) {
	doc, err := r.docStructureContext(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(doc.Extensions.Path) != "" && doc.Extensions.Extension == nil {
		var extensions Extensions
		partPath, err := r.readDocumentPartContext(ctx, doc.Extensions.Path, &extensions)
		if err != nil {
			return nil, err
		}
		for i := range extensions.Extension {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			extension := &extensions.Extension[i]
			for j := range extension.ExtendData {
				extension.ExtendData[j] = r.resourceLocation(partPath, "", extension.ExtendData[j])
			}
		}
		if extensions.Extension == nil {
			extensions.Extension = []Extension{}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		doc.Extensions.Extension = extensions.Extension
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return doc.Extensions.Extension, nil
}

// initRoot 读取根节点信息
// 返回: error 错误信息
func (r *Reader) initRoot() error {
	return r.initRootContext(context.Background())
}

// initRootContext 重建包索引与主入口，取消或失败时保留原阅读状态
// 入参: ctx 取消上下文
// 返回: error 索引、读取、解析或取消错误
func (r *Reader) initRootContext(ctx context.Context) error {
	if r.resources == nil {
		r.resources, _ = NewResources(ResourceOptions{})
	}
	view := *r
	if err := view.indexPackageContext(ctx); err != nil {
		return err
	}
	data, err := view.readFileViewContext(ctx, "OFD.xml")
	if err != nil {
		return fmt.Errorf("failed to read ofd.xml: %w", err)
	}
	var ofd OFD
	decoder := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
	if err := decoder.Decode(&ofd); err != nil {
		return fmt.Errorf("failed to unmarshal ofd.xml: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.fileIndex, r.fileNames = view.fileIndex, view.fileNames
	if r.pageContents == nil {
		r.pageContents = resourceCache[pageContentKey, PageContent](r.resources, 16<<20)
	}
	r.OFD = &ofd
	r.doc, r.versionInfo = nil, nil
	r.documentRoot, r.RootDir = "", ""
	r.relatedPending, r.fontResourcesRead = false, false
	r.imageCatalog = nil
	r.resourceInfoCache = nil
	r.Stamps, r.Annots, r.annotationFiles = nil, nil, nil
	r.ResMap = make(map[string]string)
	r.resourcesRead = make(map[string]bool)
	r.mediaCache = make(map[string]MultiMedia)
	r.resourceFiles = make(map[string]string)
	r.fontCache = make(map[string]*Font)
	r.fontFaces = make(map[string][]*FontFace)
	r.colorSpaceCache = make(map[string]*ColorSpace)
	r.drawParamCache = make(map[string]*DrawParam)
	r.compositeGraphicUnitCache = make(map[string]*CompositeGraphicUnit)
	r.pageHeaderCache = make(map[string]PageContent)
	return nil
}

// indexPackage 按大小写敏感路径建立包索引，拒绝规范化后重复的ZIP条目
// 返回: error 错误信息
func (r *Reader) indexPackage() error {
	return r.indexPackageContext(context.Background())
}

// indexPackageContext 建立大小写敏感的包索引，取消或路径校验失败时保留原索引
// 入参: ctx 取消上下文
// 返回: error 路径或取消错误
func (r *Reader) indexPackageContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	count := 0
	if r.Zip != nil {
		count = len(r.Zip.File)
	}
	index := make(map[string]*zip.File, count)
	names := make(map[string]bool, count+len(r.files))
	if r.Zip != nil {
		for _, f := range r.Zip.File {
			if err := ctx.Err(); err != nil {
				return err
			}
			if f.FileInfo().IsDir() {
				continue
			}
			name := cleanPackagePath(f.Name)
			if !validPackagePath(name) {
				return fmt.Errorf("invalid package path: %q", f.Name)
			}
			if previous, ok := index[name]; ok {
				return fmt.Errorf("ambiguous package paths: %q and %q", previous.Name, f.Name)
			}
			index[name] = f
			names[name] = true
		}
	}
	for name := range r.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name != cleanPackagePath(name) || !validPackagePath(name) {
			return fmt.Errorf("invalid package path: %q", name)
		}
		names[name] = true
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.fileIndex, r.fileNames = index, names
	return nil
}

// validPackagePath 判断规范化路径是否位于包内
// 入参: name 规范化路径
// 返回: bool 是否有效
func validPackagePath(name string) bool {
	return name != "." && name != ".." && !strings.HasPrefix(name, "../") && !strings.HasPrefix(name, "/") && !strings.ContainsAny(name, ":\x00")
}

// readFile 读取文档内的文件
// 入参: name 文件名
// 返回: []byte 文件内容, error 错误信息
func (r *Reader) readFile(name string) ([]byte, error) {
	data, borrowed, err := r.readFileData(name)
	if borrowed {
		data = bytes.Clone(data)
	}
	return data, err
}

// readFileView 借用已加载文件或读取压缩文件，返回数据不得修改
// 入参: name 文件名
// 返回: []byte 只读文件内容, error 错误信息
func (r *Reader) readFileView(name string) ([]byte, error) {
	data, _, err := r.readFileData(name)
	return data, err
}

// readFileViewContext 读取包内文件并响应取消，已加载数据直接借用
// 入参: ctx 取消上下文, name 文件名
// 返回: []byte 只读文件内容, error 读取或取消错误
func (r *Reader) readFileViewContext(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if data, ok := r.files[cleanPackagePath(name)]; ok {
		return data, nil
	}
	input, err := r.openFile(name)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	data, err := io.ReadAll(imageInput{ReadCloser: input, ctx: ctx})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

// readFileData 统一解析包内路径并区分缓存借用与独立读取
// 入参: name 文件名
// 返回: []byte 文件内容, bool 是否借用缓存, error 错误信息
func (r *Reader) readFileData(name string) ([]byte, bool, error) {
	name = cleanPackagePath(name)
	if data, ok := r.files[name]; ok {
		return data, true, nil
	}
	if f, ok := r.packageFile(name); ok {
		data, err := readZipFile(f)
		return data, false, err
	}
	return nil, false, fmt.Errorf("file not found: %s", name)
}

// openFile 打开文档内的文件流
// 入参: name 文件名
// 返回: io.ReadCloser 文件流, error 错误信息
func (r *Reader) openFile(name string) (io.ReadCloser, error) {
	name = cleanPackagePath(name)
	if data, ok := r.files[name]; ok {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	if f, ok := r.packageFile(name); ok {
		return f.Open()
	}
	return nil, fmt.Errorf("file not found: %s", name)
}

// cleanPackagePath 清理包内文件路径
// 入参: name 文件路径
// 返回: string 清理后的文件路径
func cleanPackagePath(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimPrefix(name, "/")
	return path.Clean(name)
}

// packageFile 获取包内文件
// 入参: name 文件路径
// 返回: *zip.File 压缩包文件, bool 是否存在
func (r *Reader) packageFile(name string) (*zip.File, bool) {
	f, ok := r.fileIndex[cleanPackagePath(name)]
	return f, ok
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

// loadRes 加载资源文件
// 入参: resPath 资源路径
func (r *Reader) loadRes(resPath string) {
	_ = r.loadResContext(context.Background(), resPath)
}

// loadResContext 加载可读资源的索引，读取、解析和遍历时响应取消
// 入参: ctx 取消上下文, resPath 资源路径
// 返回: error 取消错误
func (r *Reader) loadResContext(ctx context.Context, resPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if resPath == "" {
		return nil
	}
	fullPath := r.ResPath(resPath)
	if r.resourcesRead[fullPath] {
		return nil
	}
	data, err := r.readFileViewContext(ctx, fullPath)
	if err != nil {
		return ctx.Err()
	}
	var res Res
	decoder := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
	if err := decoder.Decode(&res); err != nil {
		return ctx.Err()
	}
	baseLoc := res.BaseLoc
	for i := range res.ColorSpaces.ColorSpace {
		if err := ctx.Err(); err != nil {
			return err
		}
		cs := &res.ColorSpaces.ColorSpace[i]
		if cs.Profile != "" {
			cs.Profile = r.resourceLocation(fullPath, baseLoc, cs.Profile)
		}
		r.colorSpaceCache[cs.ID] = cs
		r.resourceFiles[cs.ID] = fullPath
	}
	for _, mm := range res.MultiMedias.MultiMedia {
		if err := ctx.Err(); err != nil {
			return err
		}
		if mm.MediaFile != "" {
			if finalPath := r.resourceLocation(fullPath, baseLoc, mm.MediaFile); finalPath != "" {
				mm.MediaFile = finalPath
				if r.mediaCache == nil {
					r.mediaCache = make(map[string]MultiMedia)
				}
				r.mediaCache[mm.ID] = mm
				r.ResMap[mm.ID] = finalPath
				r.resourceFiles[mm.ID] = fullPath
			}
		}
	}
	for i := range res.Fonts.Font {
		if err := ctx.Err(); err != nil {
			return err
		}
		f := &res.Fonts.Font[i]
		if f.FontFile != "" {
			f.FontFile = r.resourceLocation(fullPath, baseLoc, f.FontFile)
		}
		r.fontCache[f.ID] = f
		r.resourceFiles[f.ID] = fullPath
	}
	for i := range res.DrawParams.DrawParam {
		if err := ctx.Err(); err != nil {
			return err
		}
		dp := &res.DrawParams.DrawParam[i]
		r.drawParamCache[dp.ID] = dp
		r.resourceFiles[dp.ID] = fullPath
	}
	for i := range res.CompositeGraphicUnits.CompositeGraphicUnit {
		if err := ctx.Err(); err != nil {
			return err
		}
		cgu := &res.CompositeGraphicUnits.CompositeGraphicUnit[i]
		r.compositeGraphicUnitCache[cgu.ID] = cgu
		r.resourceFiles[cgu.ID] = fullPath
	}
	r.resourcesRead[fullPath] = true
	return ctx.Err()
}

// resourceLocation 按资源XML所在位置解析文件，文档目录外的共享文件保留绝对路径
// 入参: name 资源XML包内路径, base 资源基准目录, file 文件声明
// 返回: string 可供ResPath解析的文件位置
func (r *Reader) resourceLocation(name, base, file string) string {
	location := resolveResourcePath(name, base, file)
	if location == "" {
		return ""
	}
	root := cleanPackagePath(r.RootDir)
	if root != "." && strings.HasPrefix(location, root+"/") {
		return strings.TrimPrefix(location, root+"/")
	}
	return "/" + location
}

// resolveResourcePath 解析资源文件路径
// 入参: resPath 资源文件路径, baseLoc 资源基准路径, filePath 文件路径
// 返回: string 资源文件路径
func resolveResourcePath(resPath, baseLoc, filePath string) string {
	p := strings.TrimSpace(filePath)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(p, "/") {
		return strings.TrimPrefix(path.Clean(p), "/")
	}
	dir := path.Dir(strings.ReplaceAll(strings.TrimSpace(resPath), "\\", "/"))
	baseLoc = strings.ReplaceAll(strings.TrimSpace(baseLoc), "\\", "/")
	if strings.HasPrefix(baseLoc, "/") {
		dir = cleanPackagePath(baseLoc)
	} else if baseLoc != "" {
		dir = path.Join(dir, baseLoc)
	}
	return path.Join(dir, p)
}

// readPageContent 读取页面，可跳过文字提取不使用的独立路径和图片
// 入参: ctx 取消上下文, page 页面对象, textOnly 是否仅用于文字提取, template 是否按模板页读取
// 返回: *PageContent 页面内容, error 错误信息
func (r *Reader) readPageContent(ctx context.Context, page Page, textOnly, template bool) (*PageContent, error) {
	if !template && r.pageReady != nil && !r.pageReady[page.ID] {
		return nil, ErrIncompleteDocument
	}
	fullPath := r.ResPath(page.BaseLoc)
	data, err := r.readFileViewContext(ctx, fullPath)
	if err != nil {
		return nil, err
	}
	if r.pageContents == nil {
		r.pageContents = resourceCache[pageContentKey, PageContent](r.resources, 16<<20)
	}
	var content PageContent
	if len(data) > (r.pageContents.limit/4-4096)/16 {
		content, err = decodePageContent(ctx, data, textOnly)
	} else {
		var parsed PageContent
		parsed, err = r.pageContents.load(ctx, pageContentKey{sha256.Sum256(data), textOnly}, func() (PageContent, int, error) {
			content, err := decodePageContent(ctx, data, textOnly)
			return content, len(data)*16 + 4096, err
		})
		if err == nil {
			content = cloneEditorData(parsed)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal page content: %w", err)
	}
	content.ID = page.ID
	if template {
		content.Template = nil
	}
	for _, res := range content.PageRes {
		if err := r.loadResContext(ctx, "/"+resolveResourcePath(fullPath, "", res)); err != nil {
			return nil, err
		}
	}
	content.Area, err = r.resolvePageAreaContext(ctx, content.Area, content.Template)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &content, nil
}

// decodePageContent 解析未应用页面资源和区域继承的原始内容
// 入参: ctx 取消上下文, data 页面XML, textOnly 是否仅读取文字
// 返回: PageContent 页面内容, error 解析错误
func decodePageContent(ctx context.Context, data []byte, textOnly bool) (PageContent, error) {
	var content PageContent
	decoder := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
	var err error
	if textOnly {
		err = xml.NewTokenDecoder(&textPageTokens{Decoder: decoder}).Decode(&content)
	} else {
		err = decoder.Decode(&content)
	}
	return content, err
}

// loadPageResources 加载页面资源，不解析图元
// 入参: page 页面引用
func (r *Reader) loadPageResources(page Page) {
	header, err := r.readPageHeader(page)
	if err != nil {
		return
	}
	for _, res := range header.PageRes {
		r.loadRes("/" + resolveResourcePath(r.ResPath(page.BaseLoc), "", res))
	}
}

// readPageHeader 读取页面区域、模板引用和资源声明
// 入参: page 页面对象
// 返回: PageContent 页面头部, error 错误信息
func (r *Reader) readPageHeader(page Page) (PageContent, error) {
	return r.readPageHeaderContext(context.Background(), page)
}

// readPageHeaderContext 读取并缓存页面头部，取消时不缓存部分结果
// 入参: ctx 取消上下文, page 页面对象
// 返回: PageContent 页面头部, error 读取、解析或取消错误
func (r *Reader) readPageHeaderContext(ctx context.Context, page Page) (PageContent, error) {
	if err := ctx.Err(); err != nil {
		return PageContent{}, err
	}
	name := r.ResPath(page.BaseLoc)
	if content, ok := r.pageHeaderCache[name]; ok {
		return content, nil
	}
	f, err := r.openFile(name)
	if err != nil {
		return PageContent{}, err
	}
	defer f.Close()
	var content PageContent
	d := xml.NewDecoder(imageInput{ReadCloser: f, ctx: ctx})
	var root xml.Name
scan:
	for {
		if err := ctx.Err(); err != nil {
			return PageContent{}, err
		}
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return PageContent{}, err
		}
		if start, ok := token.(xml.StartElement); ok {
			if root.Local == "" {
				if start.Name.Local != "Page" {
					return PageContent{}, fmt.Errorf("expected Page root, got %s", start.Name.Local)
				}
				root = start.Name
				continue
			}
			if !ofdXMLChild(root, start.Name) {
				if err := d.Skip(); err != nil {
					return PageContent{}, err
				}
				continue
			}
			start = ofdXMLStart(start)
			switch start.Name.Local {
			case "Template":
				var template Template
				if err := d.DecodeElement(&template, &start); err != nil {
					return PageContent{}, err
				}
				content.Template = append(content.Template, template)
			case "Area":
				if err := d.DecodeElement(&content.Area, &start); err != nil {
					return PageContent{}, err
				}
			case "PageRes":
				var res string
				if err := d.DecodeElement(&res, &start); err != nil {
					return PageContent{}, err
				}
				content.PageRes = append(content.PageRes, res)
			case "Content":
				break scan
			default:
				if err := d.Skip(); err != nil {
					return PageContent{}, err
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return PageContent{}, err
	}
	r.pageHeaderCache[name] = content
	return content, nil
}

// resolvePageArea 解析页面区域的继承关系
// 入参: area 页面区域, templates 模板引用
// 返回: PageArea 页面区域, error 错误信息
func (r *Reader) resolvePageArea(area PageArea, templates []Template) (PageArea, error) {
	return r.resolvePageAreaContext(context.Background(), area, templates)
}

// resolvePageAreaContext 按模板顺序解析页面区域的继承，读取过程中响应取消
// 入参: ctx 取消上下文, area 页面区域, templates 模板引用
// 返回: PageArea 页面区域, error 读取或取消错误
func (r *Reader) resolvePageAreaContext(ctx context.Context, area PageArea, templates []Template) (PageArea, error) {
	if err := ctx.Err(); err != nil {
		return PageArea{}, err
	}
	if area != (PageArea{}) || r.doc == nil {
		return area, nil
	}
	for _, ref := range templates {
		for _, template := range r.doc.CommonData.TemplatePage {
			if err := ctx.Err(); err != nil {
				return PageArea{}, err
			}
			if !sameResourceID(template.ID, ref.TemplateID) {
				continue
			}
			content, err := r.readPageHeaderContext(ctx, Page{BaseLoc: template.BaseLoc})
			if err != nil {
				return PageArea{}, err
			}
			if content.Area != (PageArea{}) {
				return content.Area, nil
			}
			break
		}
	}
	return r.doc.CommonData.PageArea, nil
}

// readDocumentPart 读取文档关联文件
// 入参: loc 文件位置, value 文档结构
// 返回: string 文件路径, error 错误信息
func (r *Reader) readDocumentPart(loc string, value any) (string, error) {
	return r.readDocumentPartContext(context.Background(), loc, value)
}

// readDocumentPartContext 读取并解析文档关联文件，传递取消信号到文件读取和XML解码
// 入参: ctx 取消上下文, loc 文件位置, value 文档结构
// 返回: string 文件路径, error 读取、解析或取消错误
func (r *Reader) readDocumentPartContext(ctx context.Context, loc string, value any) (string, error) {
	fullPath := r.ResPath(strings.TrimSpace(loc))
	data, err := r.readFileViewContext(ctx, fullPath)
	if err != nil {
		return "", err
	}
	decoder := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
	if err := decoder.Decode(value); err != nil {
		return "", fmt.Errorf("failed to unmarshal %s: %w", path.Base(fullPath), err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return fullPath, nil
}
