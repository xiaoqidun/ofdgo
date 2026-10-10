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
	"errors"
	"fmt"

	"github.com/xiaoqidun/pdfgo"
)

// documentAttachments 导入文档名称树中的附件，与注解及动作引用共用附件资源
// 入参: ctx 取消上下文
// 返回: error 名称树、附件读取或写入错误
func (p *pdfImporter) documentAttachments(ctx context.Context) error {
	return p.reader.WalkEmbeddedFiles(ctx, func(_ string, file pdfgo.FileSpecification) error {
		if file.Embedded == nil {
			if p.warning == nil {
				return fmt.Errorf("PDF embedded file has no embedded data")
			}
			p.warning(pdfgo.Diagnostic{Message: "PDF embedded file without embedded data ignored"})
			return nil
		}
		data, err := file.Embedded.DecodeContext(ctx)
		if err != nil {
			var unsupported *pdfgo.UnsupportedError
			if p.warning != nil && errors.As(err, &unsupported) {
				p.warning(pdfgo.Diagnostic{Message: "PDF document attachment not transferred to OFD: " + err.Error()})
				return nil
			}
			return err
		}
		_, err = p.attachmentFile(file.Name, data)
		return err
	})
}
