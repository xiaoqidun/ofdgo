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
	"context"
	"io"
	"maps"
	"path"
	"slices"

	"github.com/xiaoqidun/pdfgo"
)

// mediaFile 复用同一包内文件的输出说明，不合并播放资源及窗口
// 入参: media 音视频资源
// 返回: *pdfgo.FileSpecification 待读取的共享文件
func (n *pdfNavigation) mediaFile(media MultiMedia) *pdfgo.FileSpecification {
	name := n.source.ResPath(media.MediaFile)
	if file := n.mediaFiles[name]; file != nil {
		return file
	}
	file := &pdfgo.FileSpecification{Name: path.Base(name)}
	if n.mediaFiles == nil {
		n.mediaFiles = make(map[string]*pdfgo.FileSpecification)
	}
	n.mediaFiles[name] = file
	return file
}

// loadMediaFiles 按需读取原始媒体，独立播放资源共用的文件只读取一次
// 入参: ctx 取消上下文, files 媒体资源及输出文件
// 返回: error 资源读取或取消错误
func (n *pdfNavigation) loadMediaFiles(ctx context.Context, files map[string]*pdfgo.FileSpecification) error {
	for _, id := range slices.Sorted(maps.Keys(files)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if files[id].Embedded != nil {
			continue
		}
		input, err := n.source.OpenMedia(id)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(imageInput{ReadCloser: input, context: ctx})
		closeErr := input.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		files[id].Embedded = &pdfgo.Stream{Data: data}
	}
	return ctx.Err()
}
