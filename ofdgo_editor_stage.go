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
	"encoding/xml"
	"hash/crc32"
	"io"
)

// editorStagedPage 保存单次写出的压缩页面及校验值，不持有未压缩XML
type editorStagedPage struct {
	data     []byte
	size     uint64
	checksum uint32
}

// editorPageStager 复用单次保存的页面压缩器，不跨文档共享状态
type editorPageStager struct {
	compressor *flate.Writer
}

// editorPageEncoding 同步统计编码字节及CRC，保留实际写入错误
type editorPageEncoding struct {
	writer   io.Writer
	size     uint64
	checksum uint32
}

// Write 写入页面字节并统计实际接收的部分
// 入参: data 未压缩编码数据
// 返回: int 实际字节数, error 写入或短写错误
func (w *editorPageEncoding) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.size += uint64(n)
	w.checksum = crc32.Update(w.checksum, crc32.IEEETable, data[:n])
	if n != len(data) && err == nil {
		err = io.ErrShortWrite
	}
	return n, err
}

// encode 边编码边压缩自产页面，同步收集引用及用字
// 入参: editor 保存快照, page 页面内容, references 本次引用扫描
// 返回: *editorStagedPage 压缩页面, error 编码或取消错误
func (s *editorPageStager) encode(editor *Editor, page PageContent, references *editorReferenceScan) (*editorStagedPage, error) {
	var data bytes.Buffer
	if s.compressor == nil {
		level := flate.DefaultCompression
		if editor.output != nil && editor.output.options.Mode != CompressionUnchanged {
			level = flate.BestCompression
		}
		var err error
		s.compressor, err = flate.NewWriter(&data, level)
		if err != nil {
			return nil, err
		}
	} else {
		s.compressor.Reset(&data)
	}
	output := editorPageEncoding{writer: s.compressor}
	x := &ofdXML{encoder: xml.NewEncoder(&output), references: references}
	if editor.output != nil {
		x.ctx = editor.output.ctx
	}
	x.page(page)
	if err := x.finish(); err != nil {
		return nil, err
	}
	if err := s.compressor.Close(); err != nil {
		return nil, err
	}
	if !x.checkContext() {
		return nil, x.err
	}
	return &editorStagedPage{data: data.Bytes(), size: output.size, checksum: output.checksum}, nil
}

// open 流式读取暂存页面，仅用于无法完整收集编码引用的分支
// 返回: io.ReadCloser 页面解压流
func (p *editorStagedPage) open() io.ReadCloser {
	return flate.NewReader(bytes.NewReader(p.data))
}

// writeStagedPage 直接写入已经压缩的自产页面，保持ZIP条目属性和取消语义
// 入参: archive 输出归档, header 条目属性, page 暂存页面
// 返回: error 写入、短写或取消错误
func (e *Editor) writeStagedPage(archive *zip.Writer, header zip.FileHeader, page *editorStagedPage) error {
	if e.output != nil && e.output.options.Mode != CompressionUnchanged {
		if err := editorProgress(e.OnWriteProgress).report("compress", e.output.completed, 0); err != nil {
			return err
		}
	}
	prepareOutputHeader(&header, zip.Deflate, page.size, uint64(len(page.data)), page.checksum)
	entry, err := archive.CreateRaw(&header)
	if err != nil {
		return err
	}
	for offset := 0; offset < len(page.data); {
		if e.output != nil {
			if err := e.output.ctx.Err(); err != nil {
				return err
			}
		}
		end := min(offset+64<<10, len(page.data))
		if n, err := entry.Write(page.data[offset:end]); err != nil {
			return err
		} else if n != end-offset {
			return io.ErrShortWrite
		}
		offset = end
	}
	if e.output != nil && e.output.options.Mode != CompressionUnchanged {
		e.output.completed++
		return editorProgress(e.OnWriteProgress).report("compress", e.output.completed, 0)
	}
	return nil
}
