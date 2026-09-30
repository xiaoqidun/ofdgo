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
	"fmt"
	"io"
)

// Media 获取已加载的音视频资源，页面资源须先读取所属页面
// 入参: id 资源标识
// 返回: MultiMedia 资源信息, error 资源缺失或类型错误
func (r *Reader) Media(id string) (MultiMedia, error) {
	if _, err := r.Doc(); err != nil {
		return MultiMedia{}, err
	}
	media, ok := r.mediaCache[id]
	if !ok || media.Type != "Audio" && media.Type != "Video" {
		return MultiMedia{}, fmt.Errorf("audio or video resource %q is unavailable", id)
	}
	return media, nil
}

// OpenMedia 打开文档内音视频原始数据，不解码或执行播放
// 入参: id 资源标识
// 返回: io.ReadCloser 数据流，由调用方关闭, error 资源或文件读取错误
func (r *Reader) OpenMedia(id string) (io.ReadCloser, error) {
	media, err := r.Media(id)
	if err != nil {
		return nil, err
	}
	return r.openFile(media.MediaFile)
}
