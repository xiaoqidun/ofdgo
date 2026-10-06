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

// imageOutput 在分块写出图片时检查调用方取消状态
type imageOutput struct {
	context context.Context
	writer  io.Writer
}

// imageInput 在读取资源数据时检查调用方取消状态
type imageInput struct {
	io.ReadCloser
	context context.Context
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

// Read 检查取消状态后读取原始数据
// 入参: data 数据缓冲区
// 返回: int 读取字节数, error 读取或取消错误
func (r imageInput) Read(data []byte) (int, error) {
	if err := r.context.Err(); err != nil {
		return 0, err
	}
	return r.ReadCloser.Read(data)
}

// Write 检查取消状态后写出原始数据
// 入参: data 数据块
// 返回: int 写入字节数, error 写入或取消错误
func (w imageOutput) Write(data []byte) (int, error) {
	if err := w.context.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(data)
}

// Images 列出文档、页面和模板声明的图片资源，不解码图片像素
// 包含未使用及重复引用的资源声明，同一资源标识只返回一次
// 入参: ctx 取消上下文
// 返回: []ImageInfo 图片资源, error 读取或取消错误
func (r *Reader) Images(ctx context.Context) ([]ImageInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.imageCatalog != nil {
		return slices.Clone(r.imageCatalog), nil
	}
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	resources := make([]string, 0)
	for _, name := range append(slices.Clone(doc.CommonData.DocumentRes), doc.CommonData.PublicRes...) {
		resources = append(resources, r.ResPath(name))
	}
	pages := slices.Clone(doc.Pages.Page)
	for _, template := range doc.CommonData.TemplatePage {
		pages = append(pages, Page{BaseLoc: template.BaseLoc})
	}
	for _, page := range pages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header, err := r.readPageHeader(page)
		if err != nil {
			return nil, err
		}
		for _, name := range header.PageRes {
			resources = append(resources, resolveResourcePath(r.ResPath(page.BaseLoc), "", name))
		}
	}
	seen := make(map[string]bool)
	ids := make(map[string]ImageInfo)
	items := make([]ImageInfo, 0)
	for _, name := range resources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		data, err := r.readFile(name)
		if err != nil {
			return nil, err
		}
		var res Res
		if err := xml.Unmarshal(data, &res); err != nil {
			return nil, err
		}
		for _, media := range res.MultiMedias.MultiMedia {
			if media.Type != "Image" {
				continue
			}
			location := resolveResourcePath(name, res.BaseLoc, media.MediaFile)
			if media.ID == "" || location == "" {
				return nil, fmt.Errorf("invalid image resource")
			}
			item := ImageInfo{ID: media.ID, Name: path.Base(location), Format: media.Format, Location: location}
			key := imageResourceKey(item.ID)
			if previous, ok := ids[key]; ok {
				if previous.Name != item.Name || previous.Format != item.Format || previous.Location != item.Location {
					return nil, fmt.Errorf("conflicting image resource %q", item.ID)
				}
				continue
			}
			ids[key] = item
			items = append(items, item)
		}
	}
	r.imageCatalog = items
	return slices.Clone(items), nil
}

// Image 查找指定图片，已加载资源直接读取所在资源文件，不扫描其他页面
// 入参: ctx 取消上下文, id 图片资源标识
// 返回: ImageInfo 图片资源, error 读取或取消错误
func (r *Reader) Image(ctx context.Context, id string) (ImageInfo, error) {
	if err := ctx.Err(); err != nil {
		return ImageInfo{}, err
	}
	name, _ := resourceValue(r.resourceFiles, id)
	if name != "" {
		data, err := r.readFile(name)
		if err != nil {
			return ImageInfo{}, err
		}
		var res Res
		if err := xml.Unmarshal(data, &res); err != nil {
			return ImageInfo{}, err
		}
		for _, media := range res.MultiMedias.MultiMedia {
			if imageResourceKey(media.ID) == imageResourceKey(id) && media.Type == "Image" && media.MediaFile != "" {
				location := resolveResourcePath(name, res.BaseLoc, media.MediaFile)
				return ImageInfo{ID: id, Name: path.Base(location), Format: media.Format, Location: location}, nil
			}
		}
		return ImageInfo{}, fmt.Errorf("image resource %q not found", id)
	}
	items, err := r.Images(ctx)
	if err != nil {
		return ImageInfo{}, err
	}
	for _, item := range items {
		if imageResourceKey(item.ID) == imageResourceKey(id) {
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
	return imageInput{ReadCloser: input, context: ctx}, nil
}

// WriteImages 将原始图片逐个写入ZIP，不改变图片编码，文件序号避免同名覆盖
// 入参: ctx 取消上下文, writer 输出流, ids 资源标识，省略则导出全部
// 返回: error 读取、写出或取消错误
func (r *Reader) WriteImages(ctx context.Context, writer io.Writer, ids ...string) error {
	var items []ImageInfo
	if len(ids) == 0 {
		var err error
		items, err = r.Images(ctx)
		if err != nil {
			return err
		}
	} else {
		seen := make(map[string]bool)
		for _, id := range ids {
			key := imageResourceKey(id)
			if seen[key] {
				continue
			}
			item, err := r.Image(ctx, id)
			if err != nil {
				return err
			}
			items = append(items, item)
			seen[key] = true
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
