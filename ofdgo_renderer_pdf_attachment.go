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
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/xiaoqidun/pdfgo"
)

// writeAttachments 在页面绘制后读取附件，保留文件字节，不自动添加个人信息和时间
// 入参: ctx 取消上下文, options PDF写出配置
// 返回: error 附件读取或取消错误
func (n *pdfNavigation) writeAttachments(ctx context.Context, options *pdfgo.RewriteOptions) error {
	if len(n.attachments) == 0 {
		return nil
	}
	options.Attachments = make(map[string]*pdfgo.EmbeddedFile, len(n.attachments))
	for _, id := range slices.Sorted(maps.Keys(n.attachments)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		attachment := n.attachments[id]
		input, err := n.source.openFile(n.source.ResPath(attachment.FileLoc))
		if err != nil {
			return fmt.Errorf("read attachment %s: %w", id, err)
		}
		data, err := io.ReadAll(imageInput{ReadCloser: input, context: ctx})
		closeErr := input.Close()
		if err != nil {
			return fmt.Errorf("read attachment %s: %w", id, err)
		}
		if closeErr != nil {
			return closeErr
		}
		options.Attachments[id] = &pdfgo.EmbeddedFile{Name: attachment.FileName(), Data: data}
	}
	return ctx.Err()
}
