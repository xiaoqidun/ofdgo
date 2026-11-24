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
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// ErrIncompleteDocument 表示渐进阅读快照尚未包含请求的页面或完整文档
var ErrIncompleteDocument = errors.New("document content is incomplete")

// ErrEntryTooLarge 表示包内单条目解压后体积超出大小限制
var ErrEntryTooLarge = errors.New("package entry exceeds size limit")

// nextReaderArchiveID 为只读ZIP阅读器分配唯一缓存标识
var nextReaderArchiveID atomic.Uint64

// Reader OFD文件阅读器
// Reader及共享它的Renderer需串行调用
type Reader struct {
	path                      string
	zip                       *zip.Reader
	closer                    io.Closer
	ofd                       *OFD
	rootDir                   string
	resMap                    map[string]string
	sourceFile                os.FileInfo
	archiveID                 uint64
	resources                 *Resources
	pdfDestinations           *pdfDestinations
	resFileCache              map[string]*parsedRes
	resourcesRead             map[string]bool
	resourceFilesByKind       map[string]map[string]string
	imageCatalog              []ImageInfo
	resourceInfoCache         []ResourceInfo
	mediaCache                map[string]MultiMedia
	fontCache                 map[string]*Font
	fontResourcesRead         bool
	fontFaces                 map[string][]*FontFace
	colorSpaceCache           map[string]*ColorSpace
	colorProfiles             map[colorProfileKey]*colorProfile
	drawParamCache            map[string]*DrawParam
	compositeGraphicUnitCache map[string]*CompositeGraphicUnit
	docColorSpaces            *resourceIndex[*ColorSpace]
	docMedias                 *resourceIndex[MultiMedia]
	docResPaths               *resourceIndex[string]
	docFonts                  *resourceIndex[*Font]
	docDrawParams             *resourceIndex[*DrawParam]
	docComposites             *resourceIndex[*CompositeGraphicUnit]
	pageHeaderCache           map[string]PageContent
	pageHeaders               *pageHeaderStore
	stampSealCache            *stampSealCache
	pageReady                 map[string]bool
	memPages                  map[string]*PageContent
	pageContents              *renderCache[pageContentKey, PageContent]
	doc                       *Document
	relatedPending            bool
	annotIndexLoaded          bool
	sigIndexLoaded            bool
	documentIndex             int
	documentRoot              string
	selectedVersion           *string
	versionInfo               *DocumentVersionInfo
	stamps                    map[string][]Stamp
	pendingStamps             map[string][]Stamp
	annots                    map[string][]Annotation
	annotationFiles           map[string][]string
	pageAnnotFiles            map[string][]string
	pageAnnotsLoaded          map[string]bool
	pageStampsLoaded          map[string]bool
	fileIndex                 map[string]*zip.File
	files                     map[string][]byte
	fileNames                 map[string]bool
	encryption                *encryptionState
	encrypted                 *encryptedPackageSource
	encHold                   *encryptionHold
	maxIDCache                *archiveMaxIDCache
	docPageIDs                map[string]string
	docPageIDsDoc             *Document
	docPageIDsLen             int
	pageAnnotCanon            map[string]string
	annotCanon                map[string]string
	annotCanonLen             int
	pendingStampKeys          map[string][]string
	stampCanon                map[string]string
	stampCanonLen             int
}

// pageContentKey 按页面原文或只读归档路径及解析模式区分缓存，不包含文档资源和区域继承
type pageContentKey struct {
	archiveID uint64
	path      string
	digest    [32]byte
	textOnly  bool
}

// documentPageList 仅读取文档中的页面索引，统计页数时不构建其他结构
type documentPageList struct {
	Pages Pages
}

// resLoadState 在批量加载文档资源文件期间维护归一化标识到原始键的常数时间索引
type resLoadState struct {
	colorSpaces   map[string]string
	medias        map[string]string
	resPaths      map[string]string
	fonts         map[string]string
	drawParams    map[string]string
	composites    map[string]string
	filesByKind   map[string]map[string]string
	idxColorSpace map[string][]string
	idxMedias     map[string][]string
	idxResPaths   map[string][]string
	idxFonts      map[string][]string
	idxDrawParams map[string][]string
	idxComposites map[string][]string
}

// limitedEntryReader 限制包内单条目解压字节数，超限时返回明确错误而非静默截断
type limitedEntryReader struct {
	io.ReadCloser
	name      string
	remaining int64
	limit     int64
}

// pageHeaderEntry 保存页面头部缓存及其对应的内存源切片标识
type pageHeaderEntry struct {
	content PageContent
	source  []byte
}

// pageHeaderStore 在只读阅读器及派生视图间并发共享页面头部缓存
type pageHeaderStore struct {
	mu    sync.RWMutex
	items map[string]pageHeaderEntry
}

// pageHeaderScanner 复用单工作协程的解压器与缓冲区以快速解析页面头部
type pageHeaderScanner struct {
	flate io.ReadCloser
	buf   []byte
}

// archiveMaxIDCache 在不可变归档及其派生阅读器间并发共享已扫描的最大标识
type archiveMaxIDCache struct {
	mu        sync.RWMutex
	archiveID uint64
	encrypted *encryptedPackageSource
	ready     bool
	maxID     uint64
}

// WithDocumentIndex 设置打开OFD时默认选中的文档索引，从0开始
// 入参: index 文档索引
// 返回: ReaderOption 阅读选项
func WithDocumentIndex(index int) ReaderOption {
	return func(options *readerOptions) {
		options.documentIndex = index
		options.hasDocumentIndex = true
	}
}

// WithVersion 设置打开OFD时选中的历史版本标识，空字符串表示主文档入口
// 入参: id 版本标识
// 返回: ReaderOption 阅读选项
func WithVersion(id string) ReaderOption {
	return func(options *readerOptions) {
		value := id
		options.versionID = &value
	}
}

// mergeMapResourceOverride 按十进制等价标识批量覆盖字典中的旧资源条目
// 入参: dst 目标字典, src 新资源表, normIndex 归一化键到原始键索引
func mergeMapResourceOverride[T any](dst, src map[string]T, normIndex *map[string]string) {
	if len(src) == 0 {
		return
	}
	idx := *normIndex
	if idx == nil {
		idx = make(map[string]string, len(dst)+len(src))
		for existing := range dst {
			if key := editorResourceID(existing); key != "" {
				idx[key] = existing
			}
		}
		*normIndex = idx
	}
	for id, value := range src {
		if key := editorResourceID(id); key != "" {
			if prev, ok := idx[key]; ok && prev != id {
				delete(dst, prev)
			}
			idx[key] = id
		}
		dst[id] = value
	}
}

// mergeResourceIndexOverride 按高优先级资源表覆盖目标索引中的同名十进制条目
// 入参: dst 文档级目标索引, src 当前资源文件局部索引, normIndex 归一化键到原始键列表索引
func mergeResourceIndexOverride[T comparable](dst, src *resourceIndex[T], normIndex *map[string][]string) {
	if len(src.exact) == 0 {
		return
	}
	idx := *normIndex
	if idx == nil {
		idx = make(map[string][]string, len(dst.exact)+len(src.exact))
		for existing := range dst.exact {
			if key := editorResourceID(existing); key != "" {
				idx[key] = append(idx[key], existing)
			}
		}
		*normIndex = idx
	}
	if len(dst.exact) != 0 {
		for id := range src.exact {
			if key := editorResourceID(id); key != "" {
				if olds := idx[key]; len(olds) != 0 {
					for _, existing := range olds {
						delete(dst.exact, existing)
					}
					delete(dst.canonical, key)
					delete(dst.ambiguous, key)
					delete(idx, key)
				}
			}
		}
	}
	for id, value := range src.exact {
		dst.put(id, value)
		if key := editorResourceID(id); key != "" {
			idx[key] = append(idx[key], id)
		}
	}
}

// mergeResourceFilesByKind 按资源类别批量记录声明文件路径并覆写等价旧标识
// 入参: r 阅读器, kind 资源类别, src 资源精确表, fullPath 资源XML包内路径, state 批量加载状态
func mergeResourceFilesByKind[T any](r *Reader, kind string, src map[string]T, fullPath string, state *resLoadState) {
	if len(src) == 0 {
		return
	}
	if r.resourceFilesByKind == nil {
		r.resourceFilesByKind = make(map[string]map[string]string)
	}
	bucket := r.resourceFilesByKind[kind]
	if bucket == nil {
		bucket = make(map[string]string, len(src))
		r.resourceFilesByKind[kind] = bucket
	}
	if state.filesByKind == nil {
		state.filesByKind = make(map[string]map[string]string)
	}
	idx := state.filesByKind[kind]
	if idx == nil {
		idx = make(map[string]string, len(bucket)+len(src))
		for existing := range bucket {
			if key := editorResourceID(existing); key != "" {
				idx[key] = existing
			}
		}
		state.filesByKind[kind] = idx
	}
	for id := range src {
		if key := editorResourceID(id); key != "" {
			if prev, ok := idx[key]; ok && prev != id {
				delete(bucket, prev)
			}
			idx[key] = id
		}
		bucket[id] = fullPath
	}
}

// validPackagePath 判断规范化路径是否位于包内
// 入参: name 规范化路径
// 返回: bool 是否有效
func validPackagePath(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.HasPrefix(name, "../") && !strings.HasPrefix(name, "/") && !strings.ContainsAny(name, ":\x00")
}

// cleanPackagePath 清理包内文件路径
// 入参: name 文件路径
// 返回: string 清理后的文件路径
func cleanPackagePath(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimLeft(name, "/")
	return path.Clean(name)
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
		return cleanPackagePath(p)
	}
	res := strings.TrimLeft(strings.ReplaceAll(strings.TrimSpace(resPath), "\\", "/"), "/")
	dir := path.Dir(res)
	baseLoc = strings.ReplaceAll(strings.TrimSpace(baseLoc), "\\", "/")
	if strings.HasPrefix(baseLoc, "/") {
		dir = cleanPackagePath(baseLoc)
	} else if baseLoc != "" {
		dir = path.Join(dir, baseLoc)
	}
	return strings.TrimLeft(path.Join(dir, p), "/")
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

// parsePageHeaderReaderContext 从字节流解析页面头部直到Content节点
// 入参: ctx 取消上下文, input 页面字节流
// 返回: PageContent 页面头部, error 解析或取消错误
func parsePageHeaderReaderContext(ctx context.Context, input io.Reader) (PageContent, error) {
	var content PageContent
	d := xml.NewDecoder(input)
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
	return content, nil
}

// readHeaderPrefix 读取最多len(buf)字节的页面前导数据
// 入参: reader 输入流, buf 复用缓冲
// 返回: int 已读字节数, error 读取错误
func readHeaderPrefix(reader io.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := reader.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// scanFastPageHeader 快速判断前导字节是否已包含完整页面头部以及是否为空头部
// 入参: head 页面前导XML字节
// 返回: bool 是否已涵盖完整头部, bool 是否不含Area/Template/PageRes
func scanFastPageHeader(head []byte) (bool, bool) {
	if len(head) == 0 || bytes.Contains(head, []byte("<!")) {
		return false, false
	}
	cut := -1
	for _, marker := range [][]byte{[]byte("<ofd:Content"), []byte("<Content"), []byte("</ofd:Page"), []byte("</Page")} {
		if idx := bytes.Index(head, marker); idx >= 0 && (cut < 0 || idx < cut) {
			next := idx + len(marker)
			if next < len(head) && (head[next] == '>' || head[next] == ' ' || head[next] == '\t' || head[next] == '\r' || head[next] == '\n' || head[next] == '/') {
				cut = idx
			}
		}
	}
	if cut < 0 {
		return false, false
	}
	prefix := head[:cut]
	if !bytes.Contains(prefix, []byte("<ofd:Page")) && !bytes.Contains(prefix, []byte("<Page")) {
		return false, false
	}
	if !bytes.Contains(prefix, []byte("Area")) && !bytes.Contains(prefix, []byte("Template")) && !bytes.Contains(prefix, []byte("PageRes")) {
		return true, true
	}
	return true, false
}

// sameByteSlice 判断两个字节切片是否引用同一段内存和长度，用于识别内存文件覆盖
// 入参: a 原切片, b 当前切片
// 返回: bool 是否同源同长
func sameByteSlice(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return (a == nil) == (b == nil)
	}
	return &a[0] == &b[0]
}

// resPathFast 对常见规范相对路径快速拼接，特殊路径回退至完整ResPath
// 入参: r 阅读器, rootDir 已清理文档根目录, baseLoc 页面相对路径
// 返回: string 规范化包内路径
func resPathFast(r *Reader, rootDir, baseLoc string) string {
	if rootDir != "" && baseLoc != "" && baseLoc[0] != '/' && baseLoc[0] != '.' && baseLoc[0] > ' ' && baseLoc[len(baseLoc)-1] > ' ' && !strings.Contains(baseLoc, "\\") && !strings.Contains(baseLoc, "..") && !strings.Contains(baseLoc, "//") {
		if rootDir == "." {
			return baseLoc
		}
		return rootDir + "/" + baseLoc
	}
	return r.ResPath(baseLoc)
}

// cloneDocVersions 深拷贝文档版本列表
// 入参: versions 原版本列表指针
// 返回: *DocVersions 独立副本
func cloneDocVersions(versions *DocVersions) *DocVersions {
	if versions == nil {
		return nil
	}
	return &DocVersions{Version: slices.Clone(versions.Version)}
}

// cloneDocInfo 深拷贝文档元数据，隔离关键词与自定义数据切片
// 入参: info 原文档元数据
// 返回: *DocInfo 独立副本
func cloneDocInfo(info *DocInfo) *DocInfo {
	if info == nil {
		return nil
	}
	copied := *info
	copied.Keywords = slices.Clone(info.Keywords)
	if info.CustomDatas != nil {
		copied.CustomDatas = &CustomDatas{CustomData: slices.Clone(info.CustomDatas.CustomData)}
	}
	return &copied
}

// cloneAction 深拷贝单个文档动作及其所有指针和切片字段
// 入参: action 原动作
// 返回: Action 独立副本
func cloneAction(action Action) Action {
	copied := Action{Event: action.Event}
	if action.Region != nil {
		areas := make([]RegionArea, len(action.Region.Area))
		for i, area := range action.Region.Area {
			areas[i] = RegionArea{Start: area.Start, Command: slices.Clone(area.Command)}
		}
		copied.Region = &Region{Area: areas}
	}
	if action.Goto != nil {
		gotoCopy := &Goto{}
		if action.Goto.Dest != nil {
			dest := action.Goto.Dest.effective()
			gotoCopy.Dest = &dest
		}
		if action.Goto.Bookmark != nil {
			bookmark := *action.Goto.Bookmark
			gotoCopy.Bookmark = &bookmark
		}
		copied.Goto = gotoCopy
	}
	if action.URI != nil {
		uri := *action.URI
		copied.URI = &uri
	}
	if action.GotoA != nil {
		gotoA := GotoA{AttachID: action.GotoA.AttachID}
		if action.GotoA.NewWindow != nil {
			v := *action.GotoA.NewWindow
			gotoA.NewWindow = &v
		}
		copied.GotoA = &gotoA
	}
	if action.Sound != nil {
		sound := *action.Sound
		if action.Sound.Volume != nil {
			v := *action.Sound.Volume
			sound.Volume = &v
		}
		copied.Sound = &sound
	}
	if action.Movie != nil {
		movie := *action.Movie
		copied.Movie = &movie
	}
	return copied
}

// cloneActions 深拷贝动作切片
// 入参: actions 原动作列表
// 返回: []Action 独立副本
func cloneActions(actions []Action) []Action {
	if actions == nil {
		return nil
	}
	result := make([]Action, len(actions))
	for i, action := range actions {
		result[i] = cloneAction(action)
	}
	return result
}

// cloneOutlines 递归深拷贝文档大纲切片及其动作列表
// 入参: outlines 原大纲列表
// 返回: []OutlineElem 独立副本
func cloneOutlines(outlines []OutlineElem) []OutlineElem {
	if outlines == nil {
		return nil
	}
	result := make([]OutlineElem, len(outlines))
	for i, elem := range outlines {
		result[i] = OutlineElem{
			Title:       elem.Title,
			Count:       elem.Count,
			Expanded:    elem.Expanded,
			Actions:     cloneActions(elem.Actions),
			OutlineElem: cloneOutlines(elem.OutlineElem),
		}
	}
	return result
}

// cloneBookmarks 深拷贝书签切片及其跳转目标指针字段
// 入参: bookmarks 原书签列表
// 返回: []Bookmark 独立副本
func cloneBookmarks(bookmarks []Bookmark) []Bookmark {
	if bookmarks == nil {
		return nil
	}
	result := make([]Bookmark, len(bookmarks))
	for i, bookmark := range bookmarks {
		result[i] = Bookmark{Name: bookmark.Name, Dest: bookmark.Dest.effective()}
	}
	return result
}

// cloneDocument 深拷贝文档结构，保留权限声明标记并隔离全部切片与指针字段
// 入参: doc 原文档结构
// 返回: *Document 独立副本
func cloneDocument(doc *Document) *Document {
	if doc == nil {
		return nil
	}
	copied := *doc
	copied.CommonData.PublicRes = slices.Clone(doc.CommonData.PublicRes)
	copied.CommonData.DocumentRes = slices.Clone(doc.CommonData.DocumentRes)
	copied.CommonData.TemplatePage = slices.Clone(doc.CommonData.TemplatePage)
	copied.Pages.Page = slices.Clone(doc.Pages.Page)
	copied.Outlines.OutlineElem = cloneOutlines(doc.Outlines.OutlineElem)
	copied.Permissions = *clonePermissions(&doc.Permissions)
	copied.Actions = cloneActions(doc.Actions)
	copied.VPreferences = cloneVPreferences(doc.VPreferences)
	copied.Bookmarks.Bookmark = cloneBookmarks(doc.Bookmarks.Bookmark)
	if doc.Attachments.Attachment != nil {
		attachments := make([]Attachment, len(doc.Attachments.Attachment))
		for i, attachment := range doc.Attachments.Attachment {
			attachment.Size = cloneFloat64Ptr(attachment.Size)
			attachments[i] = attachment
		}
		copied.Attachments.Attachment = attachments
	}
	copied.CustomTags.CustomTag = slices.Clone(doc.CustomTags.CustomTag)
	if doc.Extensions.Extension != nil {
		extensions := make([]Extension, len(doc.Extensions.Extension))
		for i, ext := range doc.Extensions.Extension {
			ext.Property = slices.Clone(ext.Property)
			ext.ExtendData = slices.Clone(ext.ExtendData)
			if ext.Data != nil {
				data := make([]ExtensionData, len(ext.Data))
				for j, item := range ext.Data {
					item.Attr = slices.Clone(item.Attr)
					data[j] = item
				}
				ext.Data = data
			}
			extensions[i] = ext
		}
		copied.Extensions.Extension = extensions
	}
	return &copied
}

// OFD 返回包入口OFD.xml的只读深拷贝副本，避免篡改内部状态
// 返回: OFD 入口结构副本
func (r *Reader) OFD() OFD {
	if r == nil || r.ofd == nil {
		return OFD{}
	}
	doc := *r.ofd
	if r.ofd.DocBody != nil {
		doc.DocBody = make([]DocBody, len(r.ofd.DocBody))
		for i, body := range r.ofd.DocBody {
			body.DocInfo = *cloneDocInfo(&body.DocInfo)
			body.Versions = cloneDocVersions(body.Versions)
			doc.DocBody[i] = body
		}
	}
	return doc
}

// Path 返回打开阅读器时记录的源文件路径
// 返回: string 文件路径
func (r *Reader) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

// RootDir 返回当前文档根目录路径
// 返回: string 文档根目录
func (r *Reader) RootDir() string {
	if r == nil {
		return ""
	}
	return r.rootDir
}

// Close 关闭阅读器持有的文件并释放当前实例占用的敏感凭据租约，重复调用安全
// 返回: error 错误信息
func (r *Reader) Close() error {
	if r == nil {
		return nil
	}
	r.encHold.release()
	r.encHold = nil
	r.encryption = nil
	r.encrypted = nil
	if r.closer != nil {
		closer := r.closer
		r.closer = nil
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
	if _, err := r.docStructureContext(ctx); err != nil {
		return nil, err
	}
	if r.pageContents == nil {
		r.pageContents = resourceCache[pageContentKey, PageContent](r.resources, 16<<20)
	}
	if r.pageHeaders == nil {
		r.pageHeaders = &pageHeaderStore{items: make(map[string]pageHeaderEntry)}
	}
	if r.stampSealCache == nil {
		r.stampSealCache = newStampSealCache(r.resources)
	}
	if r.maxIDCache == nil {
		r.maxIDCache = &archiveMaxIDCache{}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.ensureDocPageIDs()
	next := *r
	doc := *r.doc
	next.doc = &doc
	next.docPageIDsDoc = next.doc
	next.closer = nil
	next.encHold = nil
	r.cloneMutableState(&next)
	next.bindEncryptionHold()
	return &next, nil
}

// DocumentCount 获取包内文档数量
// 返回: int 文档数量
func (r *Reader) DocumentCount() int {
	if r.ofd == nil {
		return 0
	}
	return len(r.ofd.DocBody)
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
	next := r.deriveView(nil, index, nil)
	next.pdfDestinations = r.pdfDestinations.clone()
	if err := next.initRootContext(ctx); err != nil {
		return nil, err
	}
	if _, err := next.docContext(ctx); err != nil {
		return nil, err
	}
	next.bindEncryptionHold()
	return next, nil
}

// Doc 获取当前文档结构的独立副本
// 返回: *Document 文档结构副本, error 错误信息
func (r *Reader) Doc() (*Document, error) {
	return r.DocContext(context.Background())
}

// DocContext 获取当前文档结构及关联内容的独立副本，取消或失败时保留原阅读状态
// 入参: ctx 取消上下文
// 返回: *Document 文档结构副本, error 读取或取消错误
func (r *Reader) DocContext(ctx context.Context) (*Document, error) {
	doc, err := r.docContext(ctx)
	if err != nil {
		return nil, err
	}
	return cloneDocument(doc), nil
}

// docInternal 返回内部共享文档结构供包内只读使用
// 返回: *Document 内部文档结构, error 错误信息
func (r *Reader) docInternal() (*Document, error) {
	return r.docContext(context.Background())
}

// docContext 加载当前文档结构及关联内容并返回内部共享结构，供包内只读路径避免额外拷贝
// 入参: ctx 取消上下文
// 返回: *Document 内部文档结构, error 读取或取消错误
func (r *Reader) docContext(ctx context.Context) (*Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.doc != nil && !r.relatedPending {
		return r.doc, nil
	}
	view := *r
	r.cloneMutableState(&view)
	doc, err := view.docStructureContext(ctx)
	if err != nil {
		return nil, err
	}
	if view.relatedPending {
		if err := view.ensureAllAnnotationsContext(ctx); err != nil {
			return nil, err
		}
		if err := view.ensureAllStampsContext(ctx); err != nil {
			return nil, err
		}
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
	r.annots, r.stamps, r.pendingStamps = view.annots, view.stamps, view.pendingStamps
	r.annotationFiles, r.pageAnnotFiles = view.annotationFiles, view.pageAnnotFiles
	r.pageAnnotsLoaded, r.pageStampsLoaded = view.pageAnnotsLoaded, view.pageStampsLoaded
	r.annotIndexLoaded, r.sigIndexLoaded = view.annotIndexLoaded, view.sigIndexLoaded
	r.pageAnnotCanon, r.annotCanon, r.annotCanonLen = view.pageAnnotCanon, view.annotCanon, view.annotCanonLen
	r.pendingStampKeys, r.stampCanon, r.stampCanonLen = view.pendingStampKeys, view.stampCanon, view.stampCanonLen
	return doc, nil
}

// DocStructure 获取当前文档结构与资源索引的独立副本，不预加载全包批注与印章媒体
// 返回: *Document 文档结构副本, error 错误信息
func (r *Reader) DocStructure() (*Document, error) {
	return r.DocStructureContext(context.Background())
}

// DocStructureContext 获取当前文档结构与资源索引的独立副本并响应取消，批注与印章保留按需惰性加载
// 入参: ctx 取消上下文
// 返回: *Document 文档结构副本, error 读取或取消错误
func (r *Reader) DocStructureContext(ctx context.Context) (*Document, error) {
	doc, err := r.docStructureContext(ctx)
	if err != nil {
		return nil, err
	}
	return cloneDocument(doc), nil
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
	return r.readPageContentInternal(ctx, page, false, false, true)
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
	doc, err := r.docStructureContext(ctx)
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
	if r.doc == nil && r.ofd != nil {
		if _, err := r.docStructureContext(ctx); err != nil {
			return PageArea{}, err
		}
	}
	content, err := r.readPageHeaderContext(ctx, page)
	if err != nil {
		return PageArea{}, err
	}
	return r.resolvePageAreaContext(ctx, content.Area, content.Template)
}

// PageAreas 批量读取页面区域，大页数时并行预读未缓存页头
// 入参: pages 页面列表
// 返回: []PageArea 页面区域列表, error 读取或解析错误
func (r *Reader) PageAreas(pages []Page) ([]PageArea, error) {
	return r.PageAreasContext(context.Background(), pages)
}

// PageAreasContext 批量读取页面区域并解析模板继承，大页数时并行预读未缓存页头
// 入参: ctx 取消上下文, pages 页面列表
// 返回: []PageArea 页面区域列表, error 读取、解析或取消错误
func (r *Reader) PageAreasContext(ctx context.Context, pages []Page) ([]PageArea, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	doc, err := r.docStructureContext(ctx)
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return []PageArea{}, nil
	}
	rootDir := strings.TrimPrefix(strings.ReplaceAll(r.rootDir, "\\", "/"), "/")
	if rootDir == "" {
		if docRoot, rootErr := r.DocumentRootContext(ctx); rootErr == nil {
			r.rootDir = path.Dir(docRoot)
			rootDir = strings.TrimPrefix(strings.ReplaceAll(r.rootDir, "\\", "/"), "/")
		}
	}
	names := make([]string, len(pages))
	for i, page := range pages {
		names[i] = resPathFast(r, rootDir, page.BaseLoc)
	}
	if err := r.preloadPageHeaderNamesContext(ctx, names); err != nil {
		return nil, err
	}
	defaultArea := doc.CommonData.PageArea
	hasTemplates := len(doc.CommonData.TemplatePage) > 0
	areas := make([]PageArea, len(pages))
	store := r.pageHeaders
	if store != nil {
		store.mu.RLock()
	}
	for i, name := range names {
		var content PageContent
		var ok bool
		if content, ok = r.pageHeaderCache[name]; !ok && store != nil {
			if entry, exists := store.items[name]; exists {
				fileData, inFiles := r.files[name]
				if !inFiles && entry.source == nil || inFiles && sameByteSlice(entry.source, fileData) {
					content, ok = entry.content, true
				}
			}
		}
		if !ok {
			if store != nil {
				store.mu.RUnlock()
			}
			var readErr error
			content, readErr = r.readPageHeaderContext(ctx, pages[i])
			if readErr != nil {
				return nil, readErr
			}
			if store != nil {
				store.mu.RLock()
			}
		}
		if content.Area != (PageArea{}) {
			areas[i] = content.Area
			continue
		}
		if !hasTemplates || len(content.Template) == 0 {
			areas[i] = defaultArea
			continue
		}
		if store != nil {
			store.mu.RUnlock()
		}
		area, resErr := r.resolvePageAreaContext(ctx, content.Area, content.Template)
		if resErr != nil {
			return nil, resErr
		}
		areas[i] = area
		if store != nil {
			store.mu.RLock()
		}
	}
	if store != nil {
		store.mu.RUnlock()
	}
	return areas, ctx.Err()
}

// PageFont 按当前页面局部PageRes优先的作用域查找字体声明，不扫描未读取页面
// 入参: page 已读取页面内容，可为nil, id 字体资源标识
// 返回: Font 字体声明, bool 是否命中
func (r *Reader) PageFont(page *PageContent, id string) (Font, bool) {
	if r == nil || id == "" {
		return Font{}, false
	}
	if _, err := r.docStructure(); err != nil {
		return Font{}, false
	}
	var local []*parsedRes
	if page != nil {
		local = page.pageResources
	}
	font, ok := r.resolveFontScoped(local, id)
	if !ok || font == nil {
		return Font{}, false
	}
	return *font, true
}

// DocumentFonts 获取文档级PublicRes与DocumentRes中声明的字体列表，不扫描分页PageRes
// 返回: []Font 文档级字体声明列表, error 结构读取错误
func (r *Reader) DocumentFonts() ([]Font, error) {
	if _, err := r.docStructure(); err != nil {
		return nil, err
	}
	fonts := make([]Font, 0, len(r.fontCache))
	for _, id := range slices.Sorted(maps.Keys(r.fontCache)) {
		if font := r.fontCache[id]; font != nil {
			fonts = append(fonts, *font)
		}
	}
	return fonts, nil
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
	if r.rootDir == "" {
		if docRoot, err := r.DocumentRoot(); err == nil {
			r.rootDir = path.Dir(docRoot)
		}
	}
	resLink = path.Clean(resLink)
	rootDir := strings.TrimPrefix(strings.ReplaceAll(r.rootDir, "\\", "/"), "/")
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
	if r.ofd == nil {
		return ""
	}
	return r.ofd.Version
}

// DocType 获取文档类型
// 返回: string 文档类型
func (r *Reader) DocType() string {
	if r.ofd == nil {
		return ""
	}
	return r.ofd.DocType
}

// DocInfo 获取文档元数据的独立副本
// 返回: *DocInfo 元数据副本, error 错误信息
func (r *Reader) DocInfo() (*DocInfo, error) {
	if r.ofd == nil || r.documentIndex < 0 || r.documentIndex >= len(r.ofd.DocBody) {
		return nil, fmt.Errorf("no docbody found")
	}
	return cloneDocInfo(&r.ofd.DocBody[r.documentIndex].DocInfo), nil
}

// Permissions 获取文档权限信息的独立副本，不加载注解或签名
// 返回: *Permissions 权限信息副本, error 错误信息
func (r *Reader) Permissions() (*Permissions, error) {
	doc, err := r.docStructure()
	if err != nil {
		return nil, err
	}
	return clonePermissions(&doc.Permissions), nil
}

// Outlines 获取文档大纲的独立副本，不加载注解或签名
// 返回: []OutlineElem 大纲列表副本, error 错误信息
func (r *Reader) Outlines() ([]OutlineElem, error) {
	doc, err := r.docStructure()
	if err != nil {
		return nil, err
	}
	return cloneOutlines(doc.Outlines.OutlineElem), nil
}

// Actions 获取文档动作的独立副本，不加载注解或签名
// 返回: []Action 动作列表副本, error 错误信息
func (r *Reader) Actions() ([]Action, error) {
	doc, err := r.docStructure()
	if err != nil {
		return nil, err
	}
	return cloneActions(doc.Actions), nil
}

// Bookmarks 获取文档书签的独立副本，不加载注解或签名
// 返回: []Bookmark 书签列表副本, error 错误信息
func (r *Reader) Bookmarks() ([]Bookmark, error) {
	doc, err := r.docStructure()
	if err != nil {
		return nil, err
	}
	return cloneBookmarks(doc.Bookmarks.Bookmark), nil
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
	return slices.Clone(doc.CustomTags.CustomTag), nil
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

// Read 读取数据并累计单条目解压字节数
// 入参: data 目标缓冲
// 返回: int 读取字节数, error 读取或超限错误
func (l *limitedEntryReader) Read(data []byte) (int, error) {
	if int64(len(data)) > l.remaining+1 {
		data = data[:l.remaining+1]
	}
	n, err := l.ReadCloser.Read(data)
	l.remaining -= int64(n)
	if l.remaining < 0 {
		return n, fmt.Errorf("package entry %s (%d bytes): %w", l.name, l.limit, ErrEntryTooLarge)
	}
	return n, err
}

// get 按路径与内存覆盖状态查询页面头部缓存
// 入参: name 规范化包内路径, fileData 内存文件字节, inFiles 是否存在内存覆盖
// 返回: PageContent 页面头部, bool 是否命中
func (s *pageHeaderStore) get(name string, fileData []byte, inFiles bool) (PageContent, bool) {
	if s == nil {
		return PageContent{}, false
	}
	s.mu.RLock()
	entry, ok := s.items[name]
	s.mu.RUnlock()
	if !ok {
		return PageContent{}, false
	}
	if !inFiles && entry.source == nil || inFiles && sameByteSlice(entry.source, fileData) {
		return entry.content, true
	}
	return PageContent{}, false
}

// put 写入单个页面头部缓存条目
// 入参: name 规范化包内路径, content 页面头部, source 内存覆盖字节切片
func (s *pageHeaderStore) put(name string, content PageContent, source []byte) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.items == nil {
		s.items = make(map[string]pageHeaderEntry)
	}
	s.items[name] = pageHeaderEntry{content: content, source: source}
	s.mu.Unlock()
}

// putBatch 批量写入预读完成的页面头部缓存条目
// 入参: names 路径列表, contents 页面头部列表, valid 有效标记列表, files 内存覆盖映射
func (s *pageHeaderStore) putBatch(names []string, contents []PageContent, valid []bool, files map[string][]byte) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if len(s.items) == 0 {
		s.items = make(map[string]pageHeaderEntry, len(names))
	}
	for i, name := range names {
		if valid[i] {
			var source []byte
			if data, ok := files[name]; ok {
				source = data
			}
			s.items[name] = pageHeaderEntry{content: contents[i], source: source}
		}
	}
	s.mu.Unlock()
}

// parse 解析单个页面头部，优先复用解压器进行前导扫描，复杂结构回退流式XML解析
// 入参: ctx 取消上下文, r 阅读器, name 规范化包内路径
// 返回: PageContent 页面头部, error 读取、解析或取消错误
func (s *pageHeaderScanner) parse(ctx context.Context, r *Reader, name string) (PageContent, error) {
	if err := ctx.Err(); err != nil {
		return PageContent{}, err
	}
	if _, inFiles := r.files[name]; !inFiles {
		if zf, ok := r.packageFile(name); ok && (zf.Method == zip.Deflate || zf.Method == zip.Store) {
			if len(s.buf) == 0 {
				s.buf = make([]byte, 4096)
			}
			if raw, openErr := zf.OpenRaw(); openErr == nil {
				var reader io.Reader = raw
				if zf.Method == zip.Deflate {
					if s.flate == nil {
						s.flate = flate.NewReader(raw)
					} else {
						_ = s.flate.(flate.Resetter).Reset(raw, nil)
					}
					reader = s.flate
				}
				n, readErr := readHeaderPrefix(reader, s.buf)
				if readErr == nil || readErr == io.EOF {
					head := s.buf[:n]
					if complete, empty := scanFastPageHeader(head); complete {
						if empty {
							return PageContent{}, ctx.Err()
						}
						return parsePageHeaderReaderContext(ctx, bytes.NewReader(head))
					}
				}
			}
		}
	}
	return r.parsePageHeaderStreamContext(ctx, name)
}

// close 释放复用的解压器资源
func (s *pageHeaderScanner) close() {
	if s.flate != nil {
		_ = s.flate.Close()
		s.flate = nil
	}
}

// get 查询与当前不可变归档匹配的最大标识缓存
// 入参: archiveID 压缩归档标识, encrypted 加密源指针
// 返回: uint64 最大标识, bool 是否命中
func (c *archiveMaxIDCache) get(archiveID uint64, encrypted *encryptedPackageSource) (uint64, bool) {
	if c == nil || (archiveID == 0 && encrypted == nil) {
		return 0, false
	}
	c.mu.RLock()
	ok := c.ready && c.archiveID == archiveID && c.encrypted == encrypted
	value := c.maxID
	c.mu.RUnlock()
	return value, ok
}

// put 缓存当前不可变归档扫描完成的最大标识
// 入参: archiveID 压缩归档标识, encrypted 加密源指针, value 最大标识
func (c *archiveMaxIDCache) put(archiveID uint64, encrypted *encryptedPackageSource, value uint64) {
	if c == nil || (archiveID == 0 && encrypted == nil) {
		return
	}
	c.mu.Lock()
	c.archiveID = archiveID
	c.encrypted = encrypted
	c.maxID = value
	c.ready = true
	c.mu.Unlock()
}

// encryptedSource 返回当前阅读器关联的按需解密源，优先使用直接挂载的实例
// 返回: *encryptedPackageSource 加密源，非加密包时为nil
func (r *Reader) encryptedSource() *encryptedPackageSource {
	if r == nil {
		return nil
	}
	if r.encrypted != nil {
		return r.encrypted
	}
	if r.zip == nil && r.encryption != nil && r.encryption.source != nil {
		return r.encryption.source
	}
	return nil
}

// deriveView 基于当前阅读器派生保留底层输入、加密源与共享缓存的轻量视图
// 入参: files 替换的文件覆盖层，nil表示复用原覆盖层, docIndex 文档索引, selectedVersion 显式版本选择，nil表示不指定
// 返回: *Reader 派生视图
func (r *Reader) deriveView(files map[string][]byte, docIndex int, selectedVersion *string) *Reader {
	viewFiles := r.files
	stampCache := r.stampSealCache
	if files != nil {
		viewFiles = files
		stampCache = nil
	}
	var ver *string
	if selectedVersion != nil {
		id := *selectedVersion
		ver = &id
	}
	sameVersion := (r.selectedVersion == nil && selectedVersion == nil) || (r.selectedVersion != nil && selectedVersion != nil && *r.selectedVersion == *selectedVersion)
	pageReady := r.pageReady
	memPages := r.memPages
	if files != nil || docIndex != r.documentIndex || !sameVersion {
		pageReady = nil
		memPages = nil
	}
	return &Reader{
		path:            r.path,
		zip:             r.zip,
		encrypted:       r.encryptedSource(),
		sourceFile:      r.sourceFile,
		archiveID:       r.archiveID,
		files:           viewFiles,
		encryption:      r.encryption,
		documentIndex:   docIndex,
		selectedVersion: ver,
		pageReady:       pageReady,
		memPages:        memPages,
		pageContents:    r.pageContents,
		pageHeaders:     r.pageHeaders,
		stampSealCache:  stampCache,
		maxIDCache:      r.maxIDCache,
		resources:       r.resources,
	}
}

// cloneMutableState 深拷贝阅读器上的全部可变资源缓存、索引、批注与印章状态到目标实例
// 入参: next 待填充独立可变状态的目标阅读器
func (r *Reader) cloneMutableState(next *Reader) {
	if r == nil || next == nil {
		return
	}
	spaces := make(map[*ColorSpace]*ColorSpace)
	fonts := make(map[*Font]*Font)
	drawParams := make(map[*DrawParam]*DrawParam)
	composites := make(map[*CompositeGraphicUnit]*CompositeGraphicUnit)
	if r.resFileCache != nil {
		next.resFileCache = make(map[string]*parsedRes, len(r.resFileCache))
		for k, v := range r.resFileCache {
			next.resFileCache[k] = v.cloneWith(spaces, fonts, drawParams, composites)
		}
	} else {
		next.resFileCache = nil
	}
	next.colorSpaceCache = cloneResourceMapPtr(r.colorSpaceCache, spaces, cloneColorSpacePtr)
	next.docColorSpaces = cloneResourceIndexPtr(r.docColorSpaces, spaces, cloneColorSpacePtr)
	next.colorProfiles = maps.Clone(r.colorProfiles)
	next.fontCache = cloneResourceMapPtr(r.fontCache, fonts, cloneFontPtr)
	next.docFonts = cloneResourceIndexPtr(r.docFonts, fonts, cloneFontPtr)
	next.fontFaces = cloneEditorMap(r.fontFaces)
	next.fontResourcesRead = r.fontResourcesRead
	next.drawParamCache = cloneResourceMapPtr(r.drawParamCache, drawParams, cloneDrawParamPtr)
	next.docDrawParams = cloneResourceIndexPtr(r.docDrawParams, drawParams, cloneDrawParamPtr)
	next.compositeGraphicUnitCache = cloneResourceMapPtr(r.compositeGraphicUnitCache, composites, cloneCompositeUnitPtr)
	next.docComposites = cloneResourceIndexPtr(r.docComposites, composites, cloneCompositeUnitPtr)
	next.resMap = maps.Clone(r.resMap)
	next.docResPaths = r.docResPaths.clone()
	next.mediaCache = maps.Clone(r.mediaCache)
	next.docMedias = r.docMedias.clone()
	next.resourcesRead = maps.Clone(r.resourcesRead)
	next.resourceFilesByKind = cloneEditorMap(r.resourceFilesByKind)
	next.imageCatalog = slices.Clone(r.imageCatalog)
	if r.resourceInfoCache != nil {
		next.resourceInfoCache = slices.Clone(r.resourceInfoCache)
		for i := range next.resourceInfoCache {
			next.resourceInfoCache[i].References = slices.Clone(next.resourceInfoCache[i].References)
		}
	} else {
		next.resourceInfoCache = nil
	}
	next.pdfDestinations = r.pdfDestinations.clone()
	next.pageHeaderCache = cloneEditorMap(r.pageHeaderCache)
	next.pageHeaders = r.pageHeaders
	next.stampSealCache = r.stampSealCache
	next.maxIDCache = r.maxIDCache
	next.annots = cloneEditorMap(r.annots)
	next.stamps = cloneEditorMap(r.stamps)
	next.pendingStamps = cloneEditorMap(r.pendingStamps)
	next.annotationFiles = cloneEditorMap(r.annotationFiles)
	next.pageAnnotFiles = cloneEditorMap(r.pageAnnotFiles)
	next.pageAnnotsLoaded = maps.Clone(r.pageAnnotsLoaded)
	next.pageStampsLoaded = maps.Clone(r.pageStampsLoaded)
	next.pageAnnotCanon = nil
	next.annotCanon = nil
	next.annotCanonLen = 0
	next.pendingStampKeys = nil
	next.stampCanon = nil
	next.stampCanonLen = 0
	next.relatedPending = r.relatedPending
	next.annotIndexLoaded = r.annotIndexLoaded
	next.sigIndexLoaded = r.sigIndexLoaded
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
	if r.ofd == nil || r.documentIndex < 0 || r.documentIndex >= len(r.ofd.DocBody) {
		return nil, fmt.Errorf("no docbody found")
	}
	docAttr := r.ofd.DocBody[r.documentIndex]
	view := *r
	docRootPath, err := view.DocumentRootContext(ctx)
	if err != nil {
		return nil, err
	}
	view.rootDir = path.Dir(docRootPath)
	data, err := view.readFileViewContext(ctx, docRootPath)
	if err != nil {
		return nil, err
	}
	var doc Document
	decoder := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal document.xml: %w", err)
	}
	view.doc = &doc
	doc.Signatures = view.versionSignaturesContext(ctx, docAttr.Signatures)
	r.cloneMutableState(&view)
	loadState := &resLoadState{}
	for _, res := range doc.CommonData.PublicRes {
		if err := view.loadResContext(ctx, res, loadState); err != nil {
			return nil, err
		}
	}
	for _, res := range doc.CommonData.DocumentRes {
		if err := view.loadResContext(ctx, res, loadState); err != nil {
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
	if r.resFileCache == nil {
		r.resFileCache = make(map[string]*parsedRes)
	}
	maps.Copy(r.resFileCache, view.resFileCache)
	if r.resourcesRead == nil {
		r.resourcesRead = make(map[string]bool)
	}
	maps.Copy(r.resourcesRead, view.resourcesRead)
	if r.resourceFilesByKind == nil {
		r.resourceFilesByKind = make(map[string]map[string]string)
	}
	for k, v := range view.resourceFilesByKind {
		if r.resourceFilesByKind[k] == nil {
			r.resourceFilesByKind[k] = make(map[string]string, len(v))
		}
		maps.Copy(r.resourceFilesByKind[k], v)
	}
	if r.resMap == nil {
		r.resMap = make(map[string]string)
	}
	maps.Copy(r.resMap, view.resMap)
	if r.mediaCache == nil {
		r.mediaCache = make(map[string]MultiMedia)
	}
	maps.Copy(r.mediaCache, view.mediaCache)
	if r.fontCache == nil {
		r.fontCache = make(map[string]*Font)
	}
	maps.Copy(r.fontCache, view.fontCache)
	if r.colorSpaceCache == nil {
		r.colorSpaceCache = make(map[string]*ColorSpace)
	}
	maps.Copy(r.colorSpaceCache, view.colorSpaceCache)
	if r.colorProfiles == nil {
		r.colorProfiles = make(map[colorProfileKey]*colorProfile)
	}
	maps.Copy(r.colorProfiles, view.colorProfiles)
	if r.drawParamCache == nil {
		r.drawParamCache = make(map[string]*DrawParam)
	}
	maps.Copy(r.drawParamCache, view.drawParamCache)
	if r.compositeGraphicUnitCache == nil {
		r.compositeGraphicUnitCache = make(map[string]*CompositeGraphicUnit)
	}
	maps.Copy(r.compositeGraphicUnitCache, view.compositeGraphicUnitCache)
	r.docColorSpaces = view.docColorSpaces
	r.docMedias = view.docMedias
	r.docResPaths = view.docResPaths
	r.docFonts = view.docFonts
	r.docDrawParams = view.docDrawParams
	r.docComposites = view.docComposites
	r.documentRoot, r.versionInfo, r.rootDir = view.documentRoot, view.versionInfo, view.rootDir
	r.doc, r.relatedPending = view.doc, view.relatedPending
	r.docPageIDs, r.docPageIDsDoc, r.docPageIDsLen = nil, nil, 0
}

// ensureDocPageIDs 构建当前文档页面规范标识到原文标识的常数时间索引
func (r *Reader) ensureDocPageIDs() {
	if r == nil || r.doc == nil {
		return
	}
	pages := r.doc.Pages.Page
	if r.docPageIDs != nil && r.docPageIDsDoc == r.doc && r.docPageIDsLen == len(pages) {
		return
	}
	index := make(map[string]string, len(pages))
	for _, page := range pages {
		if canon := editorResourceID(page.ID); canon != "" {
			if _, exists := index[canon]; !exists {
				index[canon] = page.ID
			}
		}
	}
	r.docPageIDs = index
	r.docPageIDsDoc = r.doc
	r.docPageIDsLen = len(pages)
}

// resolveDocPageIDByCanon 按规范十进制标识常数时间查找文档内首个匹配的页面原文标识
// 入参: key 规范化十进制页面标识
// 返回: string 页面原文标识, bool 是否命中
func (r *Reader) resolveDocPageIDByCanon(key string) (string, bool) {
	if r == nil || r.doc == nil || key == "" {
		return "", false
	}
	r.ensureDocPageIDs()
	id, ok := r.docPageIDs[key]
	return id, ok
}

// parseDocumentOnlyContext 仅解码当前文档根XML结构，不重新加载文档级资源索引
// 入参: ctx 取消上下文
// 返回: *Document 文档结构, error 读取、解析或取消错误
func (r *Reader) parseDocumentOnlyContext(ctx context.Context) (*Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.ofd == nil || r.documentIndex < 0 || r.documentIndex >= len(r.ofd.DocBody) {
		return nil, fmt.Errorf("no docbody found")
	}
	docRootPath := r.documentRoot
	if docRootPath == "" {
		var err error
		docRootPath, err = r.DocumentRootContext(ctx)
		if err != nil {
			return nil, err
		}
	}
	data, err := r.readFileViewContext(ctx, docRootPath)
	if err != nil {
		return nil, err
	}
	var doc Document
	decoder := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal document.xml: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	doc.Signatures = r.versionSignaturesContext(ctx, r.ofd.DocBody[r.documentIndex].Signatures)
	return &doc, nil
}

// reloadDocumentResourcesContext 复用未修改的resFileCache并重建文档级聚合资源索引
// 入参: ctx 取消上下文, doc 当前文档结构
// 返回: error 读取或取消错误
func (r *Reader) reloadDocumentResourcesContext(ctx context.Context, doc *Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.resourcesRead = make(map[string]bool)
	r.resourceFilesByKind = make(map[string]map[string]string)
	r.resMap = make(map[string]string)
	r.mediaCache = make(map[string]MultiMedia)
	r.fontCache = make(map[string]*Font)
	r.fontFaces = make(map[string][]*FontFace)
	r.colorSpaceCache = make(map[string]*ColorSpace)
	r.colorProfiles = make(map[colorProfileKey]*colorProfile)
	r.drawParamCache = make(map[string]*DrawParam)
	r.compositeGraphicUnitCache = make(map[string]*CompositeGraphicUnit)
	r.docColorSpaces = &resourceIndex[*ColorSpace]{}
	r.docMedias = &resourceIndex[MultiMedia]{}
	r.docResPaths = &resourceIndex[string]{}
	r.docFonts = &resourceIndex[*Font]{}
	r.docDrawParams = &resourceIndex[*DrawParam]{}
	r.docComposites = &resourceIndex[*CompositeGraphicUnit]{}
	r.imageCatalog = nil
	r.resourceInfoCache = nil
	r.fontResourcesRead = false
	loadState := &resLoadState{}
	for _, res := range doc.CommonData.PublicRes {
		if err := r.loadResContext(ctx, res, loadState); err != nil {
			return err
		}
	}
	for _, res := range doc.CommonData.DocumentRes {
		if err := r.loadResContext(ctx, res, loadState); err != nil {
			return err
		}
	}
	return ctx.Err()
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
	if r.zip != nil && r.archiveID == 0 {
		r.archiveID = nextReaderArchiveID.Add(1)
	}
	if r.pageContents == nil {
		r.pageContents = resourceCache[pageContentKey, PageContent](r.resources, 16<<20)
	}
	r.ofd = &ofd
	r.doc, r.versionInfo = nil, nil
	r.documentRoot, r.rootDir = "", ""
	r.relatedPending, r.fontResourcesRead = false, false
	r.annotIndexLoaded, r.sigIndexLoaded = false, false
	r.imageCatalog = nil
	r.resourceInfoCache = nil
	r.stamps, r.pendingStamps, r.annots, r.annotationFiles = nil, nil, nil, nil
	r.pageAnnotFiles, r.pageAnnotsLoaded, r.pageStampsLoaded = nil, nil, nil
	r.resFileCache = make(map[string]*parsedRes)
	r.resMap = make(map[string]string)
	r.resourcesRead = make(map[string]bool)
	r.mediaCache = make(map[string]MultiMedia)
	r.resourceFilesByKind = make(map[string]map[string]string)
	r.fontCache = make(map[string]*Font)
	r.fontFaces = make(map[string][]*FontFace)
	r.colorSpaceCache = make(map[string]*ColorSpace)
	r.colorProfiles = make(map[colorProfileKey]*colorProfile)
	r.drawParamCache = make(map[string]*DrawParam)
	r.compositeGraphicUnitCache = make(map[string]*CompositeGraphicUnit)
	r.docColorSpaces = &resourceIndex[*ColorSpace]{}
	r.docMedias = &resourceIndex[MultiMedia]{}
	r.docResPaths = &resourceIndex[string]{}
	r.docFonts = &resourceIndex[*Font]{}
	r.docDrawParams = &resourceIndex[*DrawParam]{}
	r.docComposites = &resourceIndex[*CompositeGraphicUnit]{}
	r.pageHeaderCache = make(map[string]PageContent)
	r.pageHeaders = &pageHeaderStore{items: make(map[string]pageHeaderEntry)}
	r.stampSealCache = newStampSealCache(r.resources)
	if r.maxIDCache == nil {
		r.maxIDCache = &archiveMaxIDCache{}
	}
	r.docPageIDs, r.docPageIDsDoc, r.docPageIDsLen = nil, nil, 0
	r.pageAnnotCanon, r.annotCanon, r.annotCanonLen = nil, nil, 0
	r.pendingStampKeys, r.stampCanon, r.stampCanonLen = nil, nil, 0
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
	if r.zip != nil {
		count = len(r.zip.File)
	}
	index := make(map[string]*zip.File, count)
	names := make(map[string]bool, count+len(r.files))
	if r.zip != nil {
		for _, f := range r.zip.File {
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
	if src := r.encryptedSource(); src != nil {
		for _, name := range src.names() {
			if err := ctx.Err(); err != nil {
				return err
			}
			names[name] = true
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.fileIndex, r.fileNames = index, names
	return nil
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
	clean := cleanPackagePath(name)
	if data, ok := r.files[clean]; ok {
		return data, nil
	}
	if page := r.memPages[clean]; page != nil {
		return encodeOFDXMLContext(ctx, func(x *ofdXML) { x.page(*page) })
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

// entryLimit 返回单条目最大解压字节数，0或负值表示不限制
// 返回: int64 字节上限
func (r *Reader) entryLimit() int64 {
	if r.resources != nil {
		return r.resources.options.MaxEntryBytes
	}
	return defaultMaxEntryBytes
}

// readFileData 统一解析包内路径并区分缓存借用与独立读取
// 入参: name 文件名
// 返回: []byte 文件内容, bool 是否借用缓存, error 错误信息
func (r *Reader) readFileData(name string) ([]byte, bool, error) {
	name = cleanPackagePath(name)
	if data, ok := r.files[name]; ok {
		return data, true, nil
	}
	if page := r.memPages[name]; page != nil {
		data, err := encodeOFDXMLContext(context.Background(), func(x *ofdXML) { x.page(*page) })
		return data, false, err
	}
	if _, ok := r.packageFile(name); ok {
		input, err := r.openFile(name)
		if err != nil {
			return nil, false, err
		}
		defer input.Close()
		data, err := io.ReadAll(input)
		return data, false, err
	}
	if src := r.encryptedSource(); src != nil && src.has(name) {
		input, err := r.openFile(name)
		if err != nil {
			return nil, false, err
		}
		defer input.Close()
		data, err := io.ReadAll(input)
		return data, false, err
	}
	return nil, false, fmt.Errorf("file not found: %s: %w", name, os.ErrNotExist)
}

// openFile 打开文档内的文件流
// 入参: name 文件名
// 返回: io.ReadCloser 文件流, error 错误信息
func (r *Reader) openFile(name string) (io.ReadCloser, error) {
	name = cleanPackagePath(name)
	if data, ok := r.files[name]; ok {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	if page := r.memPages[name]; page != nil {
		data, err := encodeOFDXMLContext(context.Background(), func(x *ofdXML) { x.page(*page) })
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	if f, ok := r.packageFile(name); ok {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		if limit := r.entryLimit(); limit > 0 {
			return &limitedEntryReader{ReadCloser: rc, name: name, remaining: limit, limit: limit}, nil
		}
		return rc, nil
	}
	if src := r.encryptedSource(); src != nil && src.has(name) {
		rc, err := src.open(name)
		if err != nil {
			return nil, err
		}
		if limit := r.entryLimit(); limit > 0 {
			return &limitedEntryReader{ReadCloser: rc, name: name, remaining: limit, limit: limit}, nil
		}
		return rc, nil
	}
	return nil, fmt.Errorf("file not found: %s: %w", name, os.ErrNotExist)
}

// packageFile 获取包内文件
// 入参: name 文件路径
// 返回: *zip.File 压缩包文件, bool 是否存在
func (r *Reader) packageFile(name string) (*zip.File, bool) {
	f, ok := r.fileIndex[cleanPackagePath(name)]
	return f, ok
}

// hasPackageFile 判断规范化包内路径对应的物理文件是否存在于当前包中
// 入参: name 包内路径
// 返回: bool 是否存在
func (r *Reader) hasPackageFile(name string) bool {
	clean := cleanPackagePath(name)
	if clean == "" || clean == "." {
		return false
	}
	if _, ok := r.files[clean]; ok {
		return true
	}
	if r.memPages[clean] != nil {
		return true
	}
	if _, ok := r.fileIndex[clean]; ok {
		return true
	}
	if src := r.encryptedSource(); src != nil {
		return src.has(clean)
	}
	return false
}

// resourceFileByKind 按资源类别查找文档级声明所在的资源XML路径
// 入参: kind 资源类别，空字符串按标准类别依次匹配, id 资源标识
// 返回: string 资源XML路径, bool 是否命中
func (r *Reader) resourceFileByKind(kind, id string) (string, bool) {
	switch kind {
	case "Image", "Audio", "Video":
		kind = "MultiMedia"
	case "":
		for _, k := range []string{"Font", "MultiMedia", "ColorSpace", "DrawParam", "CompositeGraphicUnit"} {
			if name, ok := resourceValue(r.resourceFilesByKind[k], id); ok && name != "" {
				return name, true
			}
		}
		return "", false
	}
	return resourceValue(r.resourceFilesByKind[kind], id)
}

// loadResContext 加载文档级资源索引并借助批量索引状态以常数时间覆写旧条目
// 入参: ctx 取消上下文, resPath 资源路径, state 批量加载状态
// 返回: error 取消错误
func (r *Reader) loadResContext(ctx context.Context, resPath string, state *resLoadState) error {
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
	parsed, err := r.parseResFileContext(ctx, fullPath)
	if err != nil || parsed == nil {
		return err
	}
	r.applyParsedRes(parsed, state)
	return ctx.Err()
}

// applyParsedRes 将已解析的资源表原子合并到文档级全局索引与缓存
// 入参: parsed 已解析的资源表, state 批量加载索引状态
func (r *Reader) applyParsedRes(parsed *parsedRes, state *resLoadState) {
	if parsed == nil {
		return
	}
	if r.resFileCache == nil {
		r.resFileCache = make(map[string]*parsedRes)
	}
	r.resFileCache[parsed.path] = parsed
	if r.resourcesRead == nil {
		r.resourcesRead = make(map[string]bool)
	}
	if r.docColorSpaces == nil {
		r.docColorSpaces = &resourceIndex[*ColorSpace]{}
	}
	if r.docMedias == nil {
		r.docMedias = &resourceIndex[MultiMedia]{}
	}
	if r.docResPaths == nil {
		r.docResPaths = &resourceIndex[string]{}
	}
	if r.docFonts == nil {
		r.docFonts = &resourceIndex[*Font]{}
	}
	if r.docDrawParams == nil {
		r.docDrawParams = &resourceIndex[*DrawParam]{}
	}
	if r.docComposites == nil {
		r.docComposites = &resourceIndex[*CompositeGraphicUnit]{}
	}
	if r.colorSpaceCache == nil && len(parsed.colorSpaces.exact) > 0 {
		r.colorSpaceCache = make(map[string]*ColorSpace, len(parsed.colorSpaces.exact))
	}
	mergeResourceIndexOverride(r.docColorSpaces, &parsed.colorSpaces, &state.idxColorSpace)
	mergeMapResourceOverride(r.colorSpaceCache, parsed.colorSpaces.exact, &state.colorSpaces)
	mergeResourceFilesByKind(r, "ColorSpace", parsed.colorSpaces.exact, parsed.path, state)
	if r.mediaCache == nil && len(parsed.medias.exact) > 0 {
		r.mediaCache = make(map[string]MultiMedia, len(parsed.medias.exact))
	}
	mergeResourceIndexOverride(r.docMedias, &parsed.medias, &state.idxMedias)
	mergeMapResourceOverride(r.mediaCache, parsed.medias.exact, &state.medias)
	mergeResourceFilesByKind(r, "MultiMedia", parsed.medias.exact, parsed.path, state)
	if r.resMap == nil && len(parsed.resPaths.exact) > 0 {
		r.resMap = make(map[string]string, len(parsed.resPaths.exact))
	}
	mergeResourceIndexOverride(r.docResPaths, &parsed.resPaths, &state.idxResPaths)
	mergeMapResourceOverride(r.resMap, parsed.resPaths.exact, &state.resPaths)
	if r.fontCache == nil && len(parsed.fonts.exact) > 0 {
		r.fontCache = make(map[string]*Font, len(parsed.fonts.exact))
	}
	mergeResourceIndexOverride(r.docFonts, &parsed.fonts, &state.idxFonts)
	mergeMapResourceOverride(r.fontCache, parsed.fonts.exact, &state.fonts)
	mergeResourceFilesByKind(r, "Font", parsed.fonts.exact, parsed.path, state)
	if r.drawParamCache == nil && len(parsed.drawParams.exact) > 0 {
		r.drawParamCache = make(map[string]*DrawParam, len(parsed.drawParams.exact))
	}
	mergeResourceIndexOverride(r.docDrawParams, &parsed.drawParams, &state.idxDrawParams)
	mergeMapResourceOverride(r.drawParamCache, parsed.drawParams.exact, &state.drawParams)
	mergeResourceFilesByKind(r, "DrawParam", parsed.drawParams.exact, parsed.path, state)
	if r.compositeGraphicUnitCache == nil && len(parsed.composites.exact) > 0 {
		r.compositeGraphicUnitCache = make(map[string]*CompositeGraphicUnit, len(parsed.composites.exact))
	}
	mergeResourceIndexOverride(r.docComposites, &parsed.composites, &state.idxComposites)
	mergeMapResourceOverride(r.compositeGraphicUnitCache, parsed.composites.exact, &state.composites)
	mergeResourceFilesByKind(r, "CompositeGraphicUnit", parsed.composites.exact, parsed.path, state)
	r.resourcesRead[parsed.path] = true
}

// parseResFileContext 解析单个资源文件并独立缓存，不覆写文档级全局映射
// 入参: ctx 取消上下文, fullPath 规范化资源文件包内路径
// 返回: *parsedRes 独立资源表, error 读取、解析或取消错误
func (r *Reader) parseResFileContext(ctx context.Context, fullPath string) (*parsedRes, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fullPath = cleanPackagePath(fullPath)
	if fullPath == "" || fullPath == "." {
		return nil, nil
	}
	if r.resFileCache != nil {
		if cached, ok := r.resFileCache[fullPath]; ok {
			return cached, nil
		}
	}
	data, err := r.readFileViewContext(ctx, fullPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("failed to read resource %s: %w", fullPath, err)
	}
	var res Res
	decoder := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
	if err := decoder.Decode(&res); err != nil {
		return nil, fmt.Errorf("failed to unmarshal resource %s: %w", fullPath, err)
	}
	parsed := &parsedRes{path: fullPath}
	baseLoc := res.BaseLoc
	for i := range res.ColorSpaces.ColorSpace {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cs := res.ColorSpaces.ColorSpace[i]
		if cs.Profile != "" {
			cs.Profile = r.resourceLocation(fullPath, baseLoc, cs.Profile)
		}
		parsed.colorSpaces.put(cs.ID, &cs)
	}
	for _, mm := range res.MultiMedias.MultiMedia {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if mm.MediaFile != "" {
			if finalPath := r.resourceLocation(fullPath, baseLoc, mm.MediaFile); finalPath != "" {
				mm.MediaFile = finalPath
				parsed.medias.put(mm.ID, mm)
				parsed.resPaths.put(mm.ID, finalPath)
			}
		}
	}
	for i := range res.Fonts.Font {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f := res.Fonts.Font[i]
		if f.FontFile != "" {
			f.FontFile = r.resourceLocation(fullPath, baseLoc, f.FontFile)
		}
		parsed.fonts.put(f.ID, &f)
	}
	for i := range res.DrawParams.DrawParam {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dp := res.DrawParams.DrawParam[i]
		parsed.drawParams.put(dp.ID, &dp)
	}
	for i := range res.CompositeGraphicUnits.CompositeGraphicUnit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cgu := res.CompositeGraphicUnits.CompositeGraphicUnit[i]
		parsed.composites.put(cgu.ID, &cgu)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.resFileCache == nil {
		r.resFileCache = make(map[string]*parsedRes)
	}
	r.resFileCache[fullPath] = parsed
	return parsed, nil
}

// collectPageResourcesContext 收集当前页面或模板页自身的局部资源表
// 入参: ctx 取消上下文, fullPath 页面文件路径, content 页面内容
// 返回: []*parsedRes 局部资源列表, error 取消错误
func (r *Reader) collectPageResourcesContext(ctx context.Context, fullPath string, content *PageContent) ([]*parsedRes, error) {
	var list []*parsedRes
	for _, res := range content.PageRes {
		parsed, err := r.parseResFileContext(ctx, resolveResourcePath(fullPath, "", res))
		if err != nil {
			return nil, err
		}
		if parsed != nil {
			list = append(list, parsed)
		}
	}
	return list, ctx.Err()
}

// resolveColorSpaceScoped 按页面局部资源优先查找颜色空间
// 入参: pageResources 页面局部资源, id 资源标识
// 返回: *ColorSpace 颜色空间, bool 是否存在
func (r *Reader) resolveColorSpaceScoped(pageResources []*parsedRes, id string) (*ColorSpace, bool) {
	for i := len(pageResources) - 1; i >= 0; i-- {
		if cs, ok := pageResources[i].colorSpaces.get(id); ok && cs != nil {
			return cs, true
		}
	}
	if r == nil {
		return nil, false
	}
	if cs, ok := r.docColorSpaces.get(id); ok && cs != nil {
		return cs, true
	}
	cs, ok := resourceValue(r.colorSpaceCache, id)
	return cs, ok && cs != nil
}

// resolveResPathScoped 按页面局部资源优先查找媒体路径
// 入参: pageResources 页面局部资源, id 资源标识
// 返回: string 媒体路径, bool 是否存在
func (r *Reader) resolveResPathScoped(pageResources []*parsedRes, id string) (string, bool) {
	for i := len(pageResources) - 1; i >= 0; i-- {
		if p, ok := pageResources[i].resPaths.get(id); ok && p != "" {
			return p, true
		}
	}
	if r == nil {
		return "", false
	}
	if p, ok := r.docResPaths.get(id); ok && p != "" {
		return p, true
	}
	p, ok := resourceValue(r.resMap, id)
	return p, ok && p != ""
}

// resolveFontScoped 按页面局部资源优先查找字体定义
// 入参: pageResources 页面局部资源, id 资源标识
// 返回: *Font 字体定义, bool 是否存在
func (r *Reader) resolveFontScoped(pageResources []*parsedRes, id string) (*Font, bool) {
	for i := len(pageResources) - 1; i >= 0; i-- {
		if f, ok := pageResources[i].fonts.get(id); ok && f != nil {
			return f, true
		}
	}
	if r == nil {
		return nil, false
	}
	if f, ok := r.docFonts.get(id); ok && f != nil {
		return f, true
	}
	f, ok := resourceValue(r.fontCache, id)
	return f, ok && f != nil
}

// resolveCompositeScoped 按页面局部资源优先查找复合图元定义
// 入参: pageResources 页面局部资源, id 资源标识
// 返回: *CompositeGraphicUnit 复合图元定义, bool 是否存在
func (r *Reader) resolveCompositeScoped(pageResources []*parsedRes, id string) (*CompositeGraphicUnit, bool) {
	for i := len(pageResources) - 1; i >= 0; i-- {
		if unit, ok := pageResources[i].composites.get(id); ok && unit != nil {
			return unit, true
		}
	}
	if r == nil {
		return nil, false
	}
	if unit, ok := r.docComposites.get(id); ok && unit != nil {
		return unit, true
	}
	unit, ok := resourceValue(r.compositeGraphicUnitCache, id)
	return unit, ok && unit != nil
}

// resolveMediaScoped 按页面局部资源优先查找多媒体定义
// 入参: pageResources 页面局部资源, id 资源标识
// 返回: MultiMedia 多媒体定义, bool 是否存在
func (r *Reader) resolveMediaScoped(pageResources []*parsedRes, id string) (MultiMedia, bool) {
	for i := len(pageResources) - 1; i >= 0; i-- {
		if m, ok := pageResources[i].medias.get(id); ok {
			return m, true
		}
	}
	if r == nil {
		return MultiMedia{}, false
	}
	if m, ok := r.docMedias.get(id); ok {
		return m, true
	}
	return resourceValue(r.mediaCache, id)
}

// resourceLocation 按资源XML所在位置解析文件，文档目录外的共享文件保留绝对路径
// 入参: name 资源XML包内路径, base 资源基准目录, file 文件声明
// 返回: string 可供ResPath解析的文件位置
func (r *Reader) resourceLocation(name, base, file string) string {
	location := resolveResourcePath(name, base, file)
	if location == "" {
		return ""
	}
	root := cleanPackagePath(r.rootDir)
	if root != "." && strings.HasPrefix(location, root+"/") {
		return strings.TrimPrefix(location, root+"/")
	}
	return "/" + location
}

// readPageContent 读取只读页面内容，供库内渲染、导出与文字检索零拷贝复用
// 入参: ctx 取消上下文, page 页面对象, textOnly 是否仅用于文字提取, template 是否按模板页读取
// 返回: *PageContent 页面内容, error 错误信息
func (r *Reader) readPageContent(ctx context.Context, page Page, textOnly, template bool) (*PageContent, error) {
	return r.readPageContentReadOnly(ctx, page, textOnly, template)
}

// readPageContentReadOnly 读取只读页面内容，命中缓存时跳过深拷贝直接共享图元切片
// 入参: ctx 取消上下文, page 页面对象, textOnly 是否仅用于文字提取, template 是否按模板页读取
// 返回: *PageContent 只读页面内容, error 错误信息
func (r *Reader) readPageContentReadOnly(ctx context.Context, page Page, textOnly, template bool) (*PageContent, error) {
	return r.readPageContentInternal(ctx, page, textOnly, template, false)
}

// injectMemoryPage 以写时复制方式向阅读器注入单页已构建内容，供渐进同步零XML往返复用
// 入参: pagePath 规范化页面路径, content 独立页面内容副本
func (r *Reader) injectMemoryPage(pagePath string, content *PageContent) {
	next := maps.Clone(r.memPages)
	if next == nil {
		next = make(map[string]*PageContent, 1)
	}
	next[pagePath] = content
	r.memPages = next
}

// readPageContentInternal 读取页面内容并按需克隆以隔离外部修改
// 入参: ctx 取消上下文, page 页面对象, textOnly 是否仅用于文字提取, template 是否按模板页读取, clone 是否深拷贝缓存对象
// 返回: *PageContent 页面内容, error 错误信息
func (r *Reader) readPageContentInternal(ctx context.Context, page Page, textOnly, template, clone bool) (*PageContent, error) {
	if !template && r.pageReady != nil && !r.pageReady[page.ID] {
		return nil, ErrIncompleteDocument
	}
	if r.doc == nil && r.ofd != nil {
		if _, err := r.docStructureContext(ctx); err != nil {
			return nil, err
		}
	}
	fullPath := r.ResPath(page.BaseLoc)
	cleanPath := cleanPackagePath(fullPath)
	var content PageContent
	var err error
	if !template && r.memPages != nil && r.files[cleanPath] == nil && r.memPages[cleanPath] != nil {
		direct := r.memPages[cleanPath]
		if clone {
			content, err = clonePageContentWithTyped(ctx, *direct)
			if err != nil {
				return nil, err
			}
		} else {
			content = *direct
		}
	} else {
		if r.pageContents == nil {
			r.pageContents = resourceCache[pageContentKey, PageContent](r.resources, 16<<20)
		}
		maxCacheData := (r.pageContents.limit/4 - 4096) / 16
		if r.zip != nil && r.archiveID != 0 && r.files[cleanPath] == nil {
			zf, ok := r.packageFile(cleanPath)
			if !ok {
				return nil, fmt.Errorf("file not found: %s: %w", cleanPath, os.ErrNotExist)
			}
			if zf.UncompressedSize64 > uint64(max(0, maxCacheData)) {
				data, readErr := r.readFileViewContext(ctx, fullPath)
				if readErr != nil {
					return nil, readErr
				}
				content, err = decodePageContent(ctx, data, textOnly)
			} else {
				var parsed PageContent
				parsed, err = r.pageContents.load(ctx, pageContentKey{archiveID: r.archiveID, path: cleanPath, textOnly: textOnly}, func() (PageContent, int, error) {
					data, readErr := r.readFileViewContext(ctx, fullPath)
					if readErr != nil {
						return PageContent{}, 0, readErr
					}
					decoded, decErr := decodePageContent(ctx, data, textOnly)
					return decoded, len(data)*16 + 4096, decErr
				})
				if err == nil {
					if clone {
						content, err = clonePageContentWithTyped(ctx, parsed)
						if err != nil {
							return nil, err
						}
					} else {
						content = parsed
					}
				} else if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
			}
		} else {
			data, readErr := r.readFileViewContext(ctx, fullPath)
			if readErr != nil {
				return nil, readErr
			}
			if len(data) > maxCacheData {
				content, err = decodePageContent(ctx, data, textOnly)
			} else {
				var parsed PageContent
				parsed, err = r.pageContents.load(ctx, pageContentKey{digest: sha256.Sum256(data), textOnly: textOnly}, func() (PageContent, int, error) {
					decoded, decErr := decodePageContent(ctx, data, textOnly)
					return decoded, len(data)*16 + 4096, decErr
				})
				if err == nil {
					if clone {
						content, err = clonePageContentWithTyped(ctx, parsed)
						if err != nil {
							return nil, err
						}
					} else {
						content = parsed
					}
				}
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal page content: %w", err)
	}
	content.ID = page.ID
	if template {
		content.Template = nil
	}
	content.pageResources, err = r.collectPageResourcesContext(ctx, fullPath, &content)
	if err != nil {
		return nil, err
	}
	content.Area, err = r.resolvePageAreaContext(ctx, content.Area, content.Template)
	if err != nil {
		return nil, err
	}
	if !template && !textOnly {
		if _, err := r.pageAnnotationsContext(ctx, page.ID); err != nil {
			return nil, err
		}
		if err := r.ensurePageStampsContext(ctx, page.ID); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &content, nil
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
	fileData, inFiles := r.files[name]
	if content, ok := r.pageHeaders.get(name, fileData, inFiles); ok {
		return content, nil
	}
	content, err := r.parsePageHeaderStreamContext(ctx, name)
	if err != nil {
		return PageContent{}, err
	}
	if r.pageHeaders == nil {
		r.pageHeaders = &pageHeaderStore{items: make(map[string]pageHeaderEntry)}
	}
	var source []byte
	if inFiles {
		source = fileData
	}
	r.pageHeaders.put(name, content, source)
	return content, nil
}

// preloadPageHeadersContext 并行预读未缓存的页面头部并写入共享缓存
// 入参: ctx 取消上下文, pages 待预读页面列表
// 返回: error 取消错误
func (r *Reader) preloadPageHeadersContext(ctx context.Context, pages []Page) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rootDir := strings.TrimPrefix(strings.ReplaceAll(r.rootDir, "\\", "/"), "/")
	names := make([]string, len(pages))
	for i, page := range pages {
		names[i] = resPathFast(r, rootDir, page.BaseLoc)
	}
	return r.preloadPageHeaderNamesContext(ctx, names)
}

// preloadPageHeaderNamesContext 按规范化路径列表并行预读未缓存的页面头部
// 入参: ctx 取消上下文, pageNames 规范化包内路径列表
// 返回: error 取消错误
func (r *Reader) preloadPageHeaderNamesContext(ctx context.Context, pageNames []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(pageNames) < 32 {
		return nil
	}
	workers := runtime.GOMAXPROCS(0)
	if r.resources != nil && r.resources.options.Parallelism > 0 && r.resources.options.Parallelism < workers {
		workers = r.resources.options.Parallelism
	}
	seen := make(map[string]bool, len(pageNames))
	names := make([]string, 0, len(pageNames))
	store := r.pageHeaders
	if store != nil {
		store.mu.RLock()
	}
	for _, name := range pageNames {
		if name == "" || seen[name] {
			continue
		}
		if _, cached := r.pageHeaderCache[name]; cached {
			continue
		}
		if store != nil {
			if entry, cached := store.items[name]; cached {
				fileData, inFiles := r.files[name]
				if !inFiles && entry.source == nil || inFiles && sameByteSlice(entry.source, fileData) {
					continue
				}
			}
		}
		seen[name] = true
		names = append(names, name)
	}
	if store != nil {
		store.mu.RUnlock()
	}
	if len(names) < 32 {
		return nil
	}
	if r.pageHeaders == nil {
		r.pageHeaders = &pageHeaderStore{items: make(map[string]pageHeaderEntry, len(names))}
	}
	contents := make([]PageContent, len(names))
	valid := make([]bool, len(names))
	if workers <= 1 {
		var scanner pageHeaderScanner
		defer scanner.close()
		for i, name := range names {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if content, err := scanner.parse(ctx, r, name); err == nil {
				contents[i] = content
				valid[i] = true
			}
		}
	} else {
		if workers > len(names)/16 {
			workers = max(2, len(names)/16)
		}
		var next atomic.Int64
		var wg sync.WaitGroup
		wg.Add(workers)
		for range workers {
			go func() {
				defer wg.Done()
				var scanner pageHeaderScanner
				defer scanner.close()
				for {
					start := int(next.Add(32) - 32)
					if start >= len(names) || ctx.Err() != nil {
						return
					}
					end := min(start+32, len(names))
					for idx := start; idx < end; idx++ {
						if ctx.Err() != nil {
							return
						}
						if content, err := scanner.parse(ctx, r, names[idx]); err == nil {
							contents[idx] = content
							valid[idx] = true
						}
					}
				}
			}()
		}
		wg.Wait()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.pageHeaders.putBatch(names, contents, valid, r.files)
	return nil
}

// parsePageHeaderStreamContext 流式解析单个页面头部，不修改共享缓存
// 入参: ctx 取消上下文, name 规范化包内路径
// 返回: PageContent 页面头部, error 读取、解析或取消错误
func (r *Reader) parsePageHeaderStreamContext(ctx context.Context, name string) (PageContent, error) {
	if err := ctx.Err(); err != nil {
		return PageContent{}, err
	}
	f, err := r.openFile(name)
	if err != nil {
		return PageContent{}, err
	}
	defer f.Close()
	return parsePageHeaderReaderContext(ctx, imageInput{ReadCloser: f, ctx: ctx})
}

// resolvePageArea 解析页面区域的继承关系
// 入参: area 页面区域, templates 模板引用
// 返回: PageArea 页面区域, error 错误信息
func (r *Reader) resolvePageArea(area PageArea, templates []Template) (PageArea, error) {
	return r.resolvePageAreaContext(context.Background(), area, templates)
}

// mergePageArea 按字段补全页面区域中未声明的矩形框
// 入参: dst 当前页面区域, src 父级页面区域
// 返回: PageArea 合并后的页面区域
func mergePageArea(dst, src PageArea) PageArea {
	if strings.TrimSpace(dst.PhysicalBox) == "" {
		dst.PhysicalBox = src.PhysicalBox
	}
	if strings.TrimSpace(dst.ApplicationBox) == "" {
		dst.ApplicationBox = src.ApplicationBox
	}
	if strings.TrimSpace(dst.ContentBox) == "" {
		dst.ContentBox = src.ContentBox
	}
	if strings.TrimSpace(dst.BleedBox) == "" {
		dst.BleedBox = src.BleedBox
	}
	return dst
}

// resolvePageAreaContext 按页面、模板页和文档公共数据顺序解析页面区域继承，读取过程中响应取消
// 入参: ctx 取消上下文, area 页面区域, templates 模板引用
// 返回: PageArea 页面区域, error 读取或取消错误
func (r *Reader) resolvePageAreaContext(ctx context.Context, area PageArea, templates []Template) (PageArea, error) {
	if err := ctx.Err(); err != nil {
		return PageArea{}, err
	}
	if strings.TrimSpace(area.PhysicalBox) != "" || r.doc == nil {
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
			area = mergePageArea(area, content.Area)
			if strings.TrimSpace(area.PhysicalBox) != "" {
				return area, nil
			}
			break
		}
	}
	return mergePageArea(area, r.doc.CommonData.PageArea), nil
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
