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
	"fmt"
	"image"
	"path"
	"slices"
	"strings"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
)

// ResourceTarget 描述当前页可绑定资源动作的图元，Bound表示已有该资源的点击动作
type ResourceTarget struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Bound bool   `json:"bound"`
}

// Resources 获取当前编辑快照的资源，覆盖正文、模板、注解、图案和复合图元
// 入参: ctx 取消上下文
// 返回: []ResourceInfo 资源及引用位置, error 快照或结构错误
func (e *Editor) Resources(ctx context.Context) ([]ResourceInfo, error) {
	reader, err := e.Reader(ctx)
	if err != nil {
		return nil, err
	}
	return reader.Resources(ctx)
}

// ResourceTargets 列出当前页可独立绑定音视频或附件动作的图元及绑定状态
// 模板和共享复合资源的内部成员不作为普通页面图元返回
// 入参: ctx 取消上下文, kind 为Audio、Video或Attachment, id 资源标识, page 零基页码
// 返回: []ResourceTarget 按绘制顺序排列的候选, error 资源、页面或取消错误
func (e *Editor) ResourceTargets(ctx context.Context, kind, id string, page int) ([]ResourceTarget, error) {
	if kind != "Audio" && kind != "Video" && kind != "Attachment" {
		return nil, fmt.Errorf("resource does not support actions")
	}
	items, err := e.Resources(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := findResource(items, kind, id); err != nil {
		return nil, err
	}
	content, err := e.page(page)
	if err != nil {
		return nil, err
	}
	var result []ResourceTarget
	for _, layer := range content.Content.Layer {
		for _, object := range layer.Objects {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			actions, err := resourceObjectActions(object)
			if err != nil {
				continue
			}
			target := ResourceTarget{ID: editorObjectID(object), Type: object.Type}
			if object.Type == "TextObject" {
				target.Text = object.TextObject.Text()
			}
			target.Bound = slices.ContainsFunc(actions, func(action Action) bool { return resourceActionMatches(action, kind, id) })
			result = append(result, target)
		}
	}
	return result, ctx.Err()
}

// ImportMedia 导入可独立管理的音视频资源，即使尚未绑定动作也保留在当前文档中
// 不解码或转码，格式由调用方提供；修改隔离其他文档及历史版本，支持撤销
// 入参: ctx 取消上下文, kind 为Audio或Video, format 媒体格式, data 完整媒体数据
// 返回: string 新资源标识, error 数据或包结构错误
func (e *Editor) ImportMedia(ctx context.Context, kind, format string, data []byte) (string, error) {
	if kind != "Audio" && kind != "Video" || len(data) == 0 {
		return "", fmt.Errorf("nonempty Audio or Video resource required")
	}
	reader, err := e.Reader(ctx)
	if err != nil {
		return "", err
	}
	working, err := reader.EditorContext(ctx)
	if err != nil {
		return "", err
	}
	if err := working.prepareSourceIDsContext(ctx); err != nil {
		return "", err
	}
	id, err := working.AddMedia(kind, format, data)
	if err != nil {
		return "", err
	}
	resource := working.resources[len(working.resources)-1]
	encoded, err := encodeOFDXMLContext(ctx, func(x *ofdXML) { x.resources(nil, []editorResource{resource}, nil) })
	if err != nil {
		return "", err
	}
	doc, err := reader.DocContext(ctx)
	if err != nil {
		return "", err
	}
	parts := map[string][]byte{resource.name: resource.data}
	name, added, err := mergePackageResources(ctx, reader, parts, doc, working.packageDirectory(), encoded)
	if err != nil {
		return "", err
	}
	entry := reader.documentRoot
	content, err := reader.readFileViewContext(ctx, entry)
	if err != nil {
		return "", err
	}
	root, err := parseEditorXMLContext(ctx, content)
	if err != nil {
		return "", err
	}
	common := root.child("CommonData")
	if common == nil {
		return "", fmt.Errorf("missing CommonData")
	}
	if working.maxID > maxUnitID {
		return "", fmt.Errorf("MaxUnitID %d exceeds uint32 range", working.maxID)
	}
	patch := editorXMLPatch{common.open, common.open, common.textXML("MaxUnitID", formatUnitID(working.maxID))}
	if maximum := common.child("MaxUnitID"); maximum != nil {
		patch = editorXMLPatch{maximum.start, maximum.end, maximum.textXML("MaxUnitID", formatUnitID(working.maxID))}
	}
	patches := []editorXMLPatch{patch}
	if added {
		position := common.close
		for _, child := range common.children {
			if child.matchesOFD("TemplatePage") || child.matchesOFD("DefaultCS") {
				position = child.start
				break
			}
		}
		patches = append(patches, editorXMLPatch{position, position, common.textXML("DocumentRes", "/"+name)})
	}
	parts[entry] = editorPatchXML(content, patches)
	scan := &editorFontDocument{reader: reader, index: reader.DocumentIndex()}
	if err := scan.apply(ctx, parts); err != nil {
		return "", err
	}
	return id, e.commitResourceSnapshot(ctx, reader)
}

// ReplaceResource 替换当前文档图片、音视频或附件的文件，保留标识和全部使用位置
// 图片同时清除受影响对象的旧替换图、底纹缩略图及复合预览；字体须使用EmbedFont或ExternalizeFont
// 图片替换须满足关联蒙版与主图尺寸一致、蒙版像素为不透明黑白二值的要求
// 入参: ctx 取消上下文, kind 资源类型, id 资源标识, format 媒体格式，图片按实际数据确定, data 完整文件
// 返回: error 校验或提交错误，失败时文档不变
func (e *Editor) ReplaceResource(ctx context.Context, kind, id, format string, data []byte) error {
	if !slices.Contains([]string{"Image", "Audio", "Video", "Attachment"}, kind) {
		return fmt.Errorf("use typed definition or font binding operations for %s", kind)
	}
	return e.changeResource(ctx, kind, id, func(catalog *resourceCatalog, item ResourceInfo, targetIdx int, target editorFontText, changes map[string][]byte) error {
		reader := catalog.scan.reader
		if len(data) == 0 {
			return fmt.Errorf("empty media data")
		}
		if kind == "Image" {
			img, actual, err := baseimage.DecodePixelsContext(ctx, data)
			if err != nil {
				return err
			}
			if err := validateResourceImageMask(ctx, catalog, targetIdx, img); err != nil {
				return err
			}
			format = actual
		}
		if item.Size == uint64(len(data)) && strings.EqualFold(item.Format, strings.TrimSpace(format)) {
			equal, err := packageDataEqual(ctx, reader, item.File, data)
			if err != nil || equal {
				return err
			}
		}
		name := packageAvailableName(reader, changes, path.Join(e.packageDirectory(), "Res", "Media", "Media_"+item.ID))
		changes[name] = bytes.Clone(data)
		fragment, err := editorXMLStandaloneContext(ctx, target.file.data[target.node.start:target.node.end], target.node)
		if err != nil {
			return err
		}
		root, err := parseEditorXMLContext(ctx, fragment)
		if err != nil {
			return err
		}
		file := root.child("MediaFile")
		if file == nil {
			return fmt.Errorf("missing MediaFile")
		}
		attribute, err := editorXMLAttributePatch(fragment, root, "Format", strings.ToUpper(strings.TrimSpace(format)))
		if err != nil {
			return err
		}
		fragment = editorPatchXML(fragment, []editorXMLPatch{attribute, {file.start, file.end, file.textXML("MediaFile", "/"+name)}})
		changes[target.file.name] = editorPatchXML(target.file.data, []editorXMLPatch{{target.node.start, target.node.end, fragment}})
		return nil
	}, data)
}

// DeleteResource 删除未被引用的资源声明，仅清理全包中可确认无引用的文件
// 未知扩展导致引用不完整时拒绝删除；其他文档及历史版本保持不变
// 入参: ctx 取消上下文, kind 资源类型, id 资源标识
// 返回: error 仍有引用、结构或提交错误
func (e *Editor) DeleteResource(ctx context.Context, kind, id string) error {
	return e.changeResource(ctx, kind, id, nil, nil)
}

// BindMedia 追加或移除指定图元的音视频点击播放动作，保留停止、暂停等其他动作
// 已有播放动作时保留其区域和参数，重复操作不产生修改
// 入参: ctx 取消上下文, id 媒体标识, page 零基页码, object 图元标识, bind 为false时移除该媒体的点击播放动作
// 返回: error 资源、对象或提交错误
func (e *Editor) BindMedia(ctx context.Context, id string, page int, object string, bind bool) error {
	if _, err := e.page(page); err != nil {
		return err
	}
	kind := ""
	for _, resource := range e.resources {
		if media := resource.image; media != nil && sameResourceID(media.ID, id) && (media.Type == "Audio" || media.Type == "Video") {
			kind, id = media.Type, media.ID
			break
		}
	}
	if kind == "" && e.source != nil {
		shadowed := false
		for _, parsed := range slices.Backward(e.pageLocalScope(page)) {
			if media, ok := parsed.medias.get(id); ok {
				if media.Type == "Audio" || media.Type == "Video" {
					kind, id = media.Type, media.ID
				} else {
					shadowed = true
				}
				break
			}
			if parsedHasResource(parsed, id, "") {
				shadowed = true
				break
			}
		}
		if kind == "" && !shadowed {
			if media, ok := e.source.reader.docMedias.get(id); ok && (media.Type == "Audio" || media.Type == "Video") {
				kind, id = media.Type, media.ID
			}
		}
		if kind == "" && shadowed {
			return fmt.Errorf("audio or video resource not found")
		}
	}
	if kind == "" {
		items, err := e.Resources(ctx)
		if err != nil {
			return err
		}
		for _, item := range items {
			if sameResourceID(item.ID, id) && (item.Kind == "Audio" || item.Kind == "Video") {
				kind, id = item.Kind, item.ID
				break
			}
		}
	}
	if kind == "" {
		return fmt.Errorf("audio or video resource not found")
	}
	return e.bindResourceAction(ctx, kind, id, page, object, bind)
}

// BindAttachment 追加或移除指定图元的附件点击动作，附件标识须属于当前文档及所选版本
// 保留其他动作及已有附件动作的区域和窗口设置，重复操作不产生修改
// 入参: ctx 取消上下文, id 附件标识, page 零基页码, object 图元标识, bind 为false时移除该附件的点击动作
// 返回: error 附件、对象或提交错误
func (e *Editor) BindAttachment(ctx context.Context, id string, page int, object string, bind bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	attachments, err := e.AttachmentsContext(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, attachment := range attachments {
		if err := ctx.Err(); err != nil {
			return err
		}
		if attachment.ID == id {
			if found {
				return fmt.Errorf("duplicate attachment ID: %s", id)
			}
			found = true
		}
	}
	if !found {
		return fmt.Errorf("attachment not found: %s", id)
	}
	return e.bindResourceAction(ctx, "Attachment", id, page, object, bind)
}

// AddMedia 注册音频或视频资源，保持原始编码，资源被动作引用后写入文档
// 本方法不转码或验证媒体编解码器，播放能力由调用方提供
// 相同类型、格式及内容复用标识；需要独立控制时可通过CopyMedia复制资源
// 入参: kind 为Audio或Video, format 媒体格式，可为空, data 完整媒体文件
// 返回: string 资源标识, error 无效类型或数据
func (e *Editor) AddMedia(kind, format string, data []byte) (string, error) {
	if kind != "Audio" && kind != "Video" {
		return "", fmt.Errorf("media type must be Audio or Video")
	}
	if len(data) == 0 {
		return "", fmt.Errorf("empty media data")
	}
	format = strings.ToUpper(strings.TrimSpace(format))
	key := editorResourceKey{checksum: sha256.Sum256(data), index: -1, kind: kind + "/" + format}
	if id, ok := e.resourceID[key]; ok {
		return id, nil
	}
	if err := e.prepareSourceIDs(); err != nil {
		return "", err
	}
	id := e.nextID()
	name := e.packageName("Res/Media/Media_" + id)
	e.resources = append(e.resources, editorResource{name: name, data: bytes.Clone(data), image: &MultiMedia{ID: id, Type: kind, Format: format, MediaFile: "/" + name}})
	e.resourceID[key] = id
	return id, nil
}

// CopyMedia 复制音视频资源定义，共用原始文件，以新标识区分播放控制
// 原页面资源须先通过Page读取；未被动作引用的副本不写入文档
// 入参: id 音视频资源标识
// 返回: string 新资源标识, error 资源缺失、类型或包结构错误
func (e *Editor) CopyMedia(id string) (string, error) {
	var copy editorResource
	for _, resource := range e.resources {
		if resource.image != nil && sameResourceID(resource.image.ID, id) {
			copy = resource
			break
		}
	}
	if copy.image == nil && e.source != nil {
		for _, parsed := range slices.Backward(e.pageScope) {
			if media, ok := parsed.medias.get(id); ok && (media.Type == "Audio" || media.Type == "Video") {
				media.MediaFile = "/" + cleanPackagePath(e.source.reader.ResPath(media.MediaFile))
				copy.image = &media
				break
			}
		}
	}
	if copy.image == nil && e.source != nil {
		media, err := e.source.reader.Media(id)
		if err != nil {
			return "", err
		}
		media.MediaFile = "/" + cleanPackagePath(e.source.reader.ResPath(media.MediaFile))
		copy.image = &media
	}
	if copy.image == nil || copy.image.Type != "Audio" && copy.image.Type != "Video" {
		return "", fmt.Errorf("audio or video resource %q is unavailable", id)
	}
	if err := e.prepareSourceIDs(); err != nil {
		return "", err
	}
	media := *copy.image
	media.ID = e.nextID()
	copy.image = &media
	e.resources = append(e.resources, copy)
	return media.ID, nil
}

// editorResourcePreview 记录失效传播节点及修改片段，不保留原始XML解析树
type editorResourcePreview struct {
	parent   *editorResourcePreview
	resource string
	file     string
	patches  []editorXMLPatch
}

// resourceImageMaskKey 区分包内共享图片文件及尚未提交的替换像素
type resourceImageMaskKey struct {
	file        string
	replacement bool
}

// findUniqueCatalogResource 在资源目录中定位无歧义的唯一资源声明及其所在文件与原文节点
// 入参: catalog 资源目录, kind 资源类型, id 资源标识
// 返回: ResourceInfo 资源条目, int 目录索引, string 声明所在资源文件, editorFontText 原文节点, error 未找到或跨文件同名歧义错误
func findUniqueCatalogResource(catalog *resourceCatalog, kind, id string) (ResourceInfo, int, string, editorFontText, error) {
	foundIdx := -1
	count := 0
	for i, item := range catalog.items {
		if resourceMatches(item, kind, id) {
			if count == 0 {
				foundIdx = i
			}
			count++
		}
	}
	if count == 0 {
		return ResourceInfo{}, -1, "", editorFontText{}, fmt.Errorf("resource not found: %s/%s", kind, id)
	}
	if count > 1 {
		return ResourceInfo{}, -1, "", editorFontText{}, fmt.Errorf("ambiguous resource %s/%s defined in multiple resource files", kind, id)
	}
	item := catalog.items[foundIdx]
	resFile := catalog.itemFiles[foundIdx]
	target := catalog.nodes[item.Kind+"/"+item.ID]
	if resFile != "" {
		if scoped, ok := catalog.nodes[resFile+":"+item.Kind+"/"+item.ID]; ok {
			target = scoped
		}
	}
	return item, foundIdx, resFile, target, nil
}

// changeResource 校验引用后在独立快照中修改声明，借用既有版本隔离和历史提交路径
// 入参: ctx 取消上下文, kind 资源类型, id 标识, replace 替换操作，nil表示删除, data 附件替换数据
// 返回: error 引用、结构或提交错误
func (e *Editor) changeResource(ctx context.Context, kind, id string, replace func(*resourceCatalog, ResourceInfo, int, editorFontText, map[string][]byte) error, data []byte) error {
	reader, err := e.Reader(ctx)
	if err != nil {
		return err
	}
	catalog, err := reader.resourceCatalog(ctx)
	if err != nil {
		return err
	}
	item, targetIdx, targetFile, target, err := findUniqueCatalogResource(catalog, kind, id)
	if err != nil {
		return err
	}
	if replace == nil && !item.Complete {
		return fmt.Errorf("resource references contain unsupported content")
	}
	if replace == nil && len(item.References) != 0 {
		return fmt.Errorf("resource is still referenced")
	}
	if kind == "Attachment" {
		working, err := reader.EditorContext(ctx)
		if err != nil {
			return err
		}
		revision := working.Revision()
		if replace == nil {
			err = working.DeleteAttachmentContext(ctx, item.ID)
		} else {
			err = working.ReplaceAttachmentContext(ctx, item.ID, data)
		}
		if err != nil {
			return err
		}
		if working.Revision() == revision {
			return ctx.Err()
		}
		reader, err = working.Reader(ctx)
		if err != nil {
			return err
		}
		if err := pruneUnreferencedFiles(ctx, reader, item.File); err != nil {
			return err
		}
		return e.commitResourceSnapshot(ctx, reader)
	}
	changes := make(map[string][]byte)
	if replace == nil {
		node := target.node
		if node.parent != nil && len(node.parent.children) == 1 {
			node = node.parent
		}
		changes[target.file.name] = editorPatchXML(target.file.data, []editorXMLPatch{{start: node.start, end: node.end}})
	} else if err := replace(catalog, item, targetIdx, target, changes); err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	if replace != nil && kind == "Image" {
		if err := invalidateScopedResourcePreviews(ctx, catalog, changes, targetFile, item.Kind, item.ID); err != nil {
			return err
		}
	}
	if err := catalog.scan.apply(ctx, changes); err != nil {
		return err
	}
	if err := pruneUnreferencedFiles(ctx, reader, item.File); err != nil {
		return err
	}
	return e.commitResourceSnapshot(ctx, reader)
}

// validateResourceImageMask 校验图片替换涉及的全部蒙版引用，按文件作用域精确匹配目标资源并复用校验结果
// 待替换资源单独校验，不影响仍使用同一原文件或同名局部资源的其他声明
// 入参: ctx 取消上下文, catalog 当前文档资源, targetIdx 待替换资源在catalog.items中的索引, replacement 新图片
// 返回: error 资源、尺寸、像素或取消错误
func validateResourceImageMask(ctx context.Context, catalog *resourceCatalog, targetIdx int, replacement image.Image) error {
	updated := resourceImageMaskKey{replacement: true}
	sizes := map[resourceImageMaskKey]image.Point{updated: replacement.Bounds().Size()}
	validated := make(map[resourceImageMaskKey]image.Point)
	keyFor := func(idx int) resourceImageMaskKey {
		if idx == targetIdx {
			return updated
		}
		return resourceImageMaskKey{file: catalog.items[idx].File}
	}
	for _, entry := range catalog.images {
		if err := ctx.Err(); err != nil {
			return err
		}
		rawSource, rawMask := entry.node.attr("ResourceID"), entry.node.attr("ImageMask")
		if editorResourceID(rawMask) == "" {
			continue
		}
		sourceIdx, okSource := catalog.resolveRefIndex(entry.file.name, "ImageObject", "ResourceID", rawSource)
		maskIdx, okMask := catalog.resolveRefIndex(entry.file.name, "ImageObject", "ImageMask", rawMask)
		if (!okSource || sourceIdx != targetIdx) && (!okMask || maskIdx != targetIdx) {
			continue
		}
		if !okSource {
			return fmt.Errorf("resource not found: Image/%s", editorResourceID(rawSource))
		}
		if !okMask {
			return fmt.Errorf("resource not found: Image/%s", editorResourceID(rawMask))
		}
		sourceKey := keyFor(sourceIdx)
		size, ok := sizes[sourceKey]
		if !ok {
			data, err := catalog.scan.reader.readFileViewContext(ctx, sourceKey.file)
			if err != nil {
				return err
			}
			config, _, err := baseimage.DecodeConfigDataContext(ctx, data)
			if err != nil {
				return err
			}
			size = image.Pt(config.Width, config.Height)
			sizes[sourceKey] = size
		}
		maskKey := keyFor(maskIdx)
		if previous, ok := validated[maskKey]; ok {
			if previous != size {
				return fmt.Errorf("image mask %s dimensions differ from source %s", editorResourceID(rawMask), editorResourceID(rawSource))
			}
			continue
		}
		img := replacement
		if !maskKey.replacement {
			data, err := catalog.scan.reader.readFileViewContext(ctx, maskKey.file)
			if err != nil {
				return err
			}
			img, _, err = baseimage.DecodeContext(ctx, data)
			if err != nil {
				return err
			}
		}
		if err := validateImageMask(ctx, img, size); err != nil {
			return fmt.Errorf("image object %s in %s: %w", entry.node.attr("ID"), entry.file.name, err)
		}
		sizes[maskKey] = size
		validated[maskKey] = size
	}
	return ctx.Err()
}

// resolveCatalogScopedKey 解析XML引用在所属文件作用域下命中的精确资源声明键
// 入参: catalog 资源目录, cleanFile 所在文件规范路径, elemName 元素名, ownerDrawParamID 所属DrawParam标识, role 引用角色, rawValue 原始值
// 返回: string 形如res:file:Kind:ID的作用域限定键，未命中返回空字符串
func resolveCatalogScopedKey(catalog *resourceCatalog, cleanFile, elemName, ownerDrawParamID, role, rawValue string) string {
	id := editorResourceID(rawValue)
	if id == "" {
		return ""
	}
	expected := resourceExpectedKind(elemName, role)
	scope := catalog.scopeForFile(cleanFile)
	if role == "Relative" && ownerDrawParamID != "" {
		if pos := slices.Index(scope, cleanFile); pos >= 0 {
			if id == ownerDrawParamID {
				scope = scope[pos+1:]
			} else {
				scope = scope[pos:]
			}
		}
	}
	if expected != "" {
		for _, resFile := range scope {
			if catalog.resKinds[resFile][id] == expected {
				return "res:" + resFile + ":" + expected + ":" + id
			}
		}
	}
	for _, resFile := range scope {
		if kind := catalog.resKinds[resFile][id]; kind != "" {
			return "res:" + resFile + ":" + kind + ":" + id
		}
	}
	return ""
}

// invalidateResourcePreviews 单次解析文档结构，沿资源引用传播失效状态并移除旧替换图及底纹预览
// 入参: ctx 取消上下文, scan 当前文档结构, changes 待提交文件, ids 资源标识
// 返回: error 解析或取消错误
func invalidateResourcePreviews(ctx context.Context, scan *editorFontDocument, changes map[string][]byte, ids ...string) error {
	if len(ids) == 0 {
		return ctx.Err()
	}
	reader := scan.reader
	if scan.index != reader.DocumentIndex() {
		var err error
		reader, err = reader.DocumentContext(ctx, scan.index)
		if err != nil {
			return err
		}
	}
	doc, err := reader.DocContext(ctx)
	if err != nil {
		return err
	}
	defaultCS := formatUnitID(uint64(doc.CommonData.DefaultCS))
	references := make(map[string][]*editorResourcePreview)
	for name, file := range scan.files {
		data, exists := changes[name]
		if !exists {
			data = file.data
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return err
		}
		var visit func(*editorXML, *editorResourcePreview) error
		visit = func(node *editorXML, parent *editorResourcePreview) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !node.matchesOFD(node.name.Local) {
				return nil
			}
			current := parent
			if node.parent != nil && (node.matchesOFD("DrawParam") && node.parent.matchesOFD("DrawParams") || node.matchesOFD("CompositeGraphicUnit") && node.parent.matchesOFD("CompositeGraphicUnits")) {
				current = &editorResourcePreview{parent: parent, resource: node.attr("ID"), file: name}
				if node.matchesOFD("CompositeGraphicUnit") {
					for _, key := range []string{"Thumbnail", "Substitution"} {
						if child := node.child(key); child != nil {
							current.patches = append(current.patches, editorXMLPatch{start: child.start, end: child.end})
						}
					}
				}
			}
			if node.matchesOFD("CellContent") && node.parent != nil && node.parent.matchesOFD("Pattern") && node.hasAttr("Thumbnail") {
				patch, err := editorXMLAttributeChange(data, node, "Thumbnail", nil)
				if err != nil {
					return err
				}
				current = &editorResourcePreview{parent: parent, file: name, patches: []editorXMLPatch{patch}}
			}
			if node.matchesOFD("ImageObject") && node.hasAttr("Substitution") {
				patch, err := editorXMLAttributeChange(data, node, "Substitution", nil)
				if err != nil {
					return err
				}
				preview := &editorResourcePreview{file: name, patches: []editorXMLPatch{patch}}
				for _, key := range []string{"ResourceID", "ImageMask"} {
					if id := editorResourceID(node.attr(key)); id != "" {
						references[id] = append(references[id], preview)
					}
				}
			}
			if current != nil {
				for _, key := range []string{"ResourceID", "ImageMask", "Substitution", "DrawParam", "ColorSpace", "Font"} {
					if id := editorResourceID(node.attr(key)); id != "" {
						references[id] = append(references[id], current)
					}
				}
				if resourceColorNode(node) && node.attr("ColorSpace") == "" {
					references[defaultCS] = append(references[defaultCS], current)
				}
				if node.matchesOFD("DrawParam") {
					if id := editorResourceID(node.attr("Relative")); id != "" {
						references[id] = append(references[id], current)
					}
				}
			}
			for _, child := range node.children {
				if err := visit(child, current); err != nil {
					return err
				}
			}
			return nil
		}
		if err := visit(root, nil); err != nil {
			return err
		}
	}
	affected := make(map[*editorResourcePreview]bool)
	patches := make(map[string][]editorXMLPatch)
	visited := make(map[string]bool)
	pending := slices.Clone(ids)
	for len(pending) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := editorResourceID(pending[0])
		pending = pending[1:]
		if id == "" || visited[id] {
			continue
		}
		visited[id] = true
		for _, preview := range references[id] {
			if err := ctx.Err(); err != nil {
				return err
			}
			for current := preview; current != nil && !affected[current]; current = current.parent {
				if err := ctx.Err(); err != nil {
					return err
				}
				affected[current] = true
				if current.resource != "" {
					pending = append(pending, current.resource)
				}
				if len(current.patches) != 0 {
					patches[current.file] = append(patches[current.file], current.patches...)
				}
			}
		}
	}
	for name, edits := range patches {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, exists := changes[name]
		if !exists {
			data = scan.files[name].data
		}
		changes[name] = editorPatchXML(data, edits)
	}
	return ctx.Err()
}

// invalidateScopedResourcePreviews 单次解析文档结构，按资源所属作用域沿引用传播失效状态并移除旧替换图及底纹预览
// 入参: ctx 取消上下文, catalog 当前资源目录, changes 待提交文件, targetFile 目标资源所在文件, targetKind 目标资源类型, targetID 目标资源标识
// 返回: error 解析或取消错误
func invalidateScopedResourcePreviews(ctx context.Context, catalog *resourceCatalog, changes map[string][]byte, targetFile, targetKind, targetID string) error {
	idKey := editorResourceID(targetID)
	if idKey == "" || targetKind == "" {
		return ctx.Err()
	}
	scan := catalog.scan
	reader := scan.reader
	if scan.index != reader.DocumentIndex() {
		var err error
		reader, err = reader.DocumentContext(ctx, scan.index)
		if err != nil {
			return err
		}
	}
	doc, err := reader.DocContext(ctx)
	if err != nil {
		return err
	}
	defaultCS := ""
	if doc.CommonData.DefaultCS != 0 {
		defaultCS = formatUnitID(uint64(doc.CommonData.DefaultCS))
	}
	references := make(map[string][]*editorResourcePreview)
	for name, file := range scan.files {
		cleanName := cleanPackagePath(name)
		data, exists := changes[name]
		if !exists {
			data = file.data
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return err
		}
		_, isExtFile := catalog.extParents[cleanName]
		var fileRootPreview *editorResourcePreview
		if isExtFile {
			fileRootPreview = &editorResourcePreview{resource: "file:" + cleanName, file: name}
		}
		var visit func(*editorXML, *editorResourcePreview, string) error
		visit = func(node *editorXML, parent *editorResourcePreview, ownerDrawParamID string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !node.matchesOFD(node.name.Local) {
				return nil
			}
			current := parent
			if node.parent != nil && (node.matchesOFD("DrawParam") && node.parent.matchesOFD("DrawParams") || node.matchesOFD("CompositeGraphicUnit") && node.parent.matchesOFD("CompositeGraphicUnits")) {
				declKind := node.name.Local
				declID := editorResourceID(node.attr("ID"))
				current = &editorResourcePreview{parent: parent, resource: "res:" + cleanName + ":" + declKind + ":" + declID, file: name}
				if node.matchesOFD("DrawParam") {
					ownerDrawParamID = declID
					if link := strings.TrimSpace(node.attr("Link")); link != "" {
						ext := cleanPackagePath(resolveResourcePath(name, root.attr("BaseLoc"), link))
						references["file:"+ext] = append(references["file:"+ext], current)
					}
				}
				if node.matchesOFD("CompositeGraphicUnit") {
					if loc := strings.TrimSpace(node.attr("BaseLoc")); loc != "" {
						ext := cleanPackagePath(resolveResourcePath(name, root.attr("BaseLoc"), loc))
						references["file:"+ext] = append(references["file:"+ext], current)
					}
					for _, key := range []string{"Thumbnail", "Substitution"} {
						if child := node.child(key); child != nil {
							current.patches = append(current.patches, editorXMLPatch{start: child.start, end: child.end})
						}
					}
				}
			}
			if node.matchesOFD("CellContent") && node.parent != nil && node.parent.matchesOFD("Pattern") && node.hasAttr("Thumbnail") {
				patch, err := editorXMLAttributeChange(data, node, "Thumbnail", nil)
				if err != nil {
					return err
				}
				current = &editorResourcePreview{parent: parent, file: name, patches: []editorXMLPatch{patch}}
			}
			if node.matchesOFD("ImageObject") && node.hasAttr("Substitution") {
				patch, err := editorXMLAttributeChange(data, node, "Substitution", nil)
				if err != nil {
					return err
				}
				preview := &editorResourcePreview{file: name, patches: []editorXMLPatch{patch}}
				for _, key := range []string{"ResourceID", "ImageMask"} {
					if dep := resolveCatalogScopedKey(catalog, cleanName, node.name.Local, ownerDrawParamID, key, node.attr(key)); dep != "" {
						references[dep] = append(references[dep], preview)
					}
				}
			}
			if current != nil {
				for _, key := range []string{"ResourceID", "ImageMask", "Substitution", "DrawParam", "ColorSpace", "Font"} {
					if dep := resolveCatalogScopedKey(catalog, cleanName, node.name.Local, ownerDrawParamID, key, node.attr(key)); dep != "" {
						references[dep] = append(references[dep], current)
					}
				}
				if defaultCS != "" && resourceColorNode(node) && node.attr("ColorSpace") == "" {
					if dep := resolveCatalogScopedKey(catalog, cleanName, node.name.Local, ownerDrawParamID, "ColorSpace", defaultCS); dep != "" {
						references[dep] = append(references[dep], current)
					}
				}
				if node.matchesOFD("DrawParam") {
					if dep := resolveCatalogScopedKey(catalog, cleanName, node.name.Local, ownerDrawParamID, "Relative", node.attr("Relative")); dep != "" {
						references[dep] = append(references[dep], current)
					}
				}
			}
			for _, child := range node.children {
				if err := visit(child, current, ownerDrawParamID); err != nil {
					return err
				}
			}
			return nil
		}
		if err := visit(root, fileRootPreview, ""); err != nil {
			return err
		}
	}
	affected := make(map[*editorResourcePreview]bool)
	patches := make(map[string][]editorXMLPatch)
	visited := make(map[string]bool)
	var pending []string
	if targetFile != "" {
		pending = append(pending, "res:"+cleanPackagePath(targetFile)+":"+targetKind+":"+idKey)
	} else {
		for resFile, kinds := range catalog.resKinds {
			if kinds[idKey] == targetKind {
				pending = append(pending, "res:"+resFile+":"+targetKind+":"+idKey)
			}
		}
	}
	for len(pending) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		currentKey := pending[0]
		pending = pending[1:]
		if currentKey == "" || visited[currentKey] {
			continue
		}
		visited[currentKey] = true
		for _, preview := range references[currentKey] {
			if err := ctx.Err(); err != nil {
				return err
			}
			for current := preview; current != nil && !affected[current]; current = current.parent {
				if err := ctx.Err(); err != nil {
					return err
				}
				affected[current] = true
				if current.resource != "" {
					pending = append(pending, current.resource)
				}
				if len(current.patches) != 0 {
					patches[current.file] = append(patches[current.file], current.patches...)
				}
			}
		}
	}
	for name, edits := range patches {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, exists := changes[name]
		if !exists {
			data = scan.files[name].data
		}
		changes[name] = editorPatchXML(data, edits)
	}
	return ctx.Err()
}

// bindResourceAction 修改单个资源的点击动作，沿用图元编辑的版本隔离及撤销机制
// 入参: ctx 取消上下文, kind 资源类型, id 资源标识, page 零基页码, object 图元标识, bind 是否绑定
// 返回: error 对象或提交错误
func (e *Editor) bindResourceAction(ctx context.Context, kind, id string, page int, object string, bind bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := e.Object(page, object)
	if err != nil {
		return err
	}
	actions, err := resourceObjectActions(target)
	if err != nil {
		return err
	}
	found := false
	actions = slices.DeleteFunc(slices.Clone(actions), func(action Action) bool {
		match := resourceActionMatches(action, kind, id)
		found = found || match
		return match && !bind
	})
	if bind && !found {
		action := Action{Event: "CLICK"}
		switch kind {
		case "Attachment":
			action.GotoA = &GotoA{AttachID: id}
		case "Audio":
			action.Sound = &Sound{ResourceID: id}
		case "Video":
			action.Movie = &Movie{ResourceID: id, Operator: "Play"}
		}
		actions = append(actions, action)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if bind == found {
		return nil
	}
	return e.SetObjectActions(page, []string{object}, actions)
}

// resourceObjectActions 读取支持动作的图元，不复制或修改动作
// 入参: object 页面图元
// 返回: []Action 原动作, error 不支持动作的图元类型
func resourceObjectActions(object GraphicObject) ([]Action, error) {
	switch object.Type {
	case "TextObject":
		return object.TextObject.Actions, nil
	case "ImageObject":
		return object.ImageObject.Actions, nil
	case "Path", "PathObject":
		return object.PathObject.Actions, nil
	case "CompositeObject", "CompositeGraphicUnit":
		return object.CompositeGraphicUnit.Actions, nil
	default:
		return nil, fmt.Errorf("object does not support actions")
	}
}

// resourceActionMatches 检查资源的点击动作，视频停止或暂停不视为播放绑定
// 入参: action 动作, kind 资源类型, id 资源标识
// 返回: bool 是否匹配
func resourceActionMatches(action Action, kind, id string) bool {
	if action.Event != "CLICK" {
		return false
	}
	switch kind {
	case "Attachment":
		return action.GotoA != nil && action.GotoA.AttachID == id
	case "Audio":
		return action.Sound != nil && sameResourceID(action.Sound.ResourceID, id)
	case "Video":
		return action.Movie != nil && sameResourceID(action.Movie.ResourceID, id) && (action.Movie.Operator == "" || action.Movie.Operator == "Play")
	default:
		return false
	}
}
