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
	"errors"
	"fmt"
	"io"
)

// DetectFormat 按文件内容识别PDF或OFD，不读取压缩内容或关闭输入
// 识别结果不代表文档完整有效，仍需通过对应阅读器解析
// 入参: source 随机读取源, size 文件字节数
// 返回: string 格式标识pdf或ofd, error 读取错误或不支持的格式
func DetectFormat(source io.ReaderAt, size int64) (string, error) {
	if source == nil || size <= 0 {
		return "", fmt.Errorf("empty document source")
	}
	source = io.NewSectionReader(source, 0, size)
	var header [1024]byte
	data := header[:min(size, int64(len(header)))]
	n, err := source.ReadAt(data, 0)
	if err != nil && err != io.EOF {
		return "", err
	}
	if n != len(data) {
		return "", io.ErrUnexpectedEOF
	}
	if bytes.HasPrefix(data, []byte("%PDF-")) {
		return "pdf", nil
	}
	archive, err := zip.NewReader(source, size)
	if err == nil {
		reader := &Reader{Zip: archive}
		if err := reader.indexPackage(); err != nil {
			return "", err
		}
		if reader.fileNames["OFD.xml"] || reader.fileNames["Encryptions.xml"] {
			return "ofd", nil
		}
	} else {
		if !errors.Is(err, zip.ErrFormat) {
			return "", err
		}
		if bytes.Contains(data, []byte("%PDF-")) {
			return "pdf", nil
		}
	}
	return "", fmt.Errorf("unsupported input format")
}
