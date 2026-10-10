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
	"crypto/sha256"
	"fmt"
	"strings"
)

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
