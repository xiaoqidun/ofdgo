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
	"io"
	"path"
	"slices"
	"strings"
)

// ImageInfo 描述文档内的原始图片资源，独立蒙版保留为独立资源
// 不包含对象裁剪、旋转、透明度及显示尺寸，不代表页面合成后的外观
type ImageInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Format   string `json:"format"`
	Location string `json:"-"`
}

// Images 列出文档、页面和模板声明的图片资源，不解码图片像素
// 包含未使用及重复引用的资源声明，同一资源标识与路径组合只返回一次，不加载注解或签名
// 入参: ctx 取消上下文
// 返回: []ImageInfo 图片资源, error 读取或取消错误
func (r *Reader) Images(ctx context.Context) ([]ImageInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.imageCatalog != nil {
		return slices.Clone(r.imageCatalog), nil
	}
	doc, err := r.docStructureContext(ctx)
	if err != nil {
		return nil, err
	}
	parsedDoc := make(map[string][]ImageInfo)
	docMediaTypes := make(map[string]map[string]string)
	docEffectiveFile := make(map[string]string)
	for _, rawName := range append(slices.Clone(doc.CommonData.PublicRes), doc.CommonData.DocumentRes...) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := cleanPackagePath(r.ResPath(rawName))
		if name == "" || name == "." {
			continue
		}
		if _, loaded := parsedDoc[name]; !loaded {
			fileItems, mediaTypes, err := r.parseImageResFileContext(ctx, name)
			if err != nil {
				return nil, err
			}
			parsedDoc[name] = fileItems
			docMediaTypes[name] = mediaTypes
		}
		for key, mediaType := range docMediaTypes[name] {
			if mediaType == "Image" {
				docEffectiveFile[key] = name
			} else {
				delete(docEffectiveFile, key)
			}
		}
	}
	seen := make(map[string]bool)
	emitted := make(map[imageDedupKey]bool)
	items := make([]ImageInfo, 0)
	for _, rawName := range append(slices.Clone(doc.CommonData.DocumentRes), doc.CommonData.PublicRes...) {
		name := cleanPackagePath(r.ResPath(rawName))
		if name == "" || name == "." || seen[name] {
			continue
		}
		seen[name] = true
		for _, item := range parsedDoc[name] {
			key := imageResourceKey(item.ID)
			if docEffectiveFile[key] != name {
				continue
			}
			dedup := imageDedupKey{id: key, location: item.Location}
			if !emitted[dedup] {
				emitted[dedup] = true
				items = append(items, item)
			}
		}
	}
	pages := slices.Clone(doc.Pages.Page)
	for _, template := range doc.CommonData.TemplatePage {
		pages = append(pages, Page{BaseLoc: template.BaseLoc})
	}
	pageResources := make([]string, 0)
	for _, page := range pages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header, err := r.readPageHeaderContext(ctx, page)
		if err != nil {
			return nil, err
		}
		for _, name := range header.PageRes {
			pageResources = append(pageResources, cleanPackagePath(resolveResourcePath(r.ResPath(page.BaseLoc), "", name)))
		}
	}
	for _, name := range pageResources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if name == "" || name == "." || seen[name] {
			continue
		}
		seen[name] = true
		fileItems, _, err := r.parseImageResFileContext(ctx, name)
		if err != nil {
			return nil, err
		}
		for _, item := range fileItems {
			key := imageResourceKey(item.ID)
			dedup := imageDedupKey{id: key, location: item.Location}
			if !emitted[dedup] {
				emitted[dedup] = true
				items = append(items, item)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.imageCatalog = items
	return slices.Clone(items), nil
}

// Image 查找指定图片，复用媒体索引或图片清单，尚未建立索引时读取资源声明
// 入参: ctx 取消上下文, id 图片资源标识
// 返回: ImageInfo 图片资源, error 读取或取消错误
func (r *Reader) Image(ctx context.Context, id string) (ImageInfo, error) {
	if err := ctx.Err(); err != nil {
		return ImageInfo{}, err
	}
	name, _ := r.resourceFileByKind("MultiMedia", id)
	if name != "" {
		if r.resourcesRead[name] {
			media, ok := r.docMedias.get(id)
			if !ok {
				media, ok = resourceValue(r.mediaCache, id)
			}
			if ok && media.Type == "Image" && media.MediaFile != "" {
				location := r.ResPath(media.MediaFile)
				return ImageInfo{ID: id, Name: path.Base(location), Format: media.Format, Location: location}, nil
			}
		} else {
			data, err := r.readFileViewContext(ctx, name)
			if err != nil {
				return ImageInfo{}, err
			}
			var res Res
			decoder := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
			if err := decoder.Decode(&res); err != nil {
				return ImageInfo{}, err
			}
			for _, media := range res.MultiMedias.MultiMedia {
				if err := ctx.Err(); err != nil {
					return ImageInfo{}, err
				}
				if imageResourceKey(media.ID) == imageResourceKey(id) && media.Type == "Image" && media.MediaFile != "" {
					location := resolveResourcePath(name, res.BaseLoc, media.MediaFile)
					return ImageInfo{ID: id, Name: path.Base(location), Format: media.Format, Location: location}, nil
				}
			}
		}
	}
	items := r.imageCatalog
	if items == nil {
		var err error
		items, err = r.Images(ctx)
		if err != nil {
			return ImageInfo{}, err
		}
	}
	key := imageResourceKey(id)
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return ImageInfo{}, err
		}
		if imageResourceKey(item.ID) == key {
			return item, nil
		}
	}
	return ImageInfo{}, fmt.Errorf("image resource %q not found", id)
}

// OpenImage 打开原始图片资源流，不解码、不重采样或重新压缩
// 加密文档返回解密后的资源，调用方负责关闭并完整读取以校验ZIP数据
// 入参: ctx 取消上下文, id 图片资源标识
// 返回: io.ReadCloser 原始图片流, error 读取错误
func (r *Reader) OpenImage(ctx context.Context, id string) (io.ReadCloser, error) {
	item, err := r.Image(ctx, id)
	if err != nil {
		return nil, err
	}
	input, err := r.openFile(item.Location)
	if err != nil {
		return nil, err
	}
	return imageInput{ReadCloser: input, ctx: ctx}, nil
}

// WriteImages 将原始图片逐个写入ZIP，不改变图片编码，文件序号避免同名覆盖
// 入参: ctx 取消上下文, writer 输出流, ids 资源标识，省略则导出全部
// 返回: error 读取、写出或取消错误
func (r *Reader) WriteImages(ctx context.Context, writer io.Writer, ids ...string) error {
	catalog, err := r.Images(ctx)
	if err != nil {
		return err
	}
	var items []ImageInfo
	if len(ids) == 0 {
		items = catalog
	} else {
		seen := make(map[string]bool, len(ids))
		for _, id := range ids {
			key := imageResourceKey(id)
			if seen[key] {
				continue
			}
			seen[key] = true
			matched := false
			for _, item := range catalog {
				if err := ctx.Err(); err != nil {
					return err
				}
				if imageResourceKey(item.ID) == key {
					items = append(items, item)
					matched = true
				}
			}
			if !matched {
				return fmt.Errorf("image resource %q not found", id)
			}
		}
	}
	archive := zip.NewWriter(imageOutput{ctx, writer})
	for i, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := strings.Map(func(c rune) rune {
			if c < 32 || strings.ContainsRune(`<>:"/\|?*`, c) {
				return '_'
			}
			return c
		}, item.Name)
		header := &zip.FileHeader{Name: fmt.Sprintf("%04d-%s", i+1, name), Method: zip.Store}
		output, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		input, err := r.openFile(item.Location)
		if err != nil {
			return err
		}
		_, err = io.Copy(imageOutput{ctx, output}, input)
		closeErr := input.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return archive.Close()
}

// imageDedupKey 按规范化资源标识与包内文件路径对图片清单去重
type imageDedupKey struct {
	id       string
	location string
}

// imageOutput 在分块写出图片时检查调用方取消状态
type imageOutput struct {
	ctx    context.Context
	writer io.Writer
}

// Write 检查取消状态后写出原始数据
// 入参: data 数据块
// 返回: int 写入字节数, error 写入或取消错误
func (w imageOutput) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(data)
}

// imageInput 在读取资源数据时检查调用方取消状态
type imageInput struct {
	io.ReadCloser
	ctx context.Context
}

// Read 检查取消状态后读取原始数据
// 入参: data 数据缓冲区
// 返回: int 读取字节数, error 读取或取消错误
func (r imageInput) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReadCloser.Read(data)
}

// parseImageResFileContext 解析单个资源文件内的图片声明并校验同一文件内的标识冲突
// 入参: ctx 取消上下文, name 规范化资源XML包内路径
// 返回: []ImageInfo 图片列表, map[string]string 多媒体类型映射, error 读取、冲突或取消错误
func (r *Reader) parseImageResFileContext(ctx context.Context, name string) ([]ImageInfo, map[string]string, error) {
	data, err := r.readFileViewContext(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	var res Res
	decoder := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
	if err := decoder.Decode(&res); err != nil {
		return nil, nil, err
	}
	scoped := make(map[string]ImageInfo)
	mediaTypes := make(map[string]string)
	items := make([]ImageInfo, 0, len(res.MultiMedias.MultiMedia))
	for _, media := range res.MultiMedias.MultiMedia {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		key := imageResourceKey(media.ID)
		if prevType, exists := mediaTypes[key]; exists && prevType != media.Type {
			return nil, nil, fmt.Errorf("conflicting image resource %q", media.ID)
		}
		if media.Type != "Image" {
			if key != "" {
				mediaTypes[key] = media.Type
			}
			continue
		}
		location := cleanPackagePath(resolveResourcePath(name, res.BaseLoc, media.MediaFile))
		if media.ID == "" || location == "" || location == "." {
			return nil, nil, fmt.Errorf("invalid image resource")
		}
		item := ImageInfo{ID: media.ID, Name: path.Base(location), Format: media.Format, Location: location}
		if prev, exists := scoped[key]; exists {
			if prev.Name != item.Name || prev.Format != item.Format || prev.Location != item.Location {
				return nil, nil, fmt.Errorf("conflicting image resource %q", item.ID)
			}
			continue
		}
		mediaTypes[key] = media.Type
		scoped[key] = item
		items = append(items, item)
	}
	return items, mediaTypes, ctx.Err()
}

// imageResourceKey 合并有效十进制标识，非标准标识保留原值，不相互混淆
// 入参: id 图片标识
// 返回: string 查找键
func imageResourceKey(id string) string {
	if key := editorResourceID(id); key != "" {
		return key
	}
	return id
}
