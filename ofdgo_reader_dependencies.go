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
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// documentFiles 按标准路径遍历独立文档依赖，签名摘要引用不作为内容依赖
// 入参: ctx 取消上下文, index 文档索引
// 返回: map[string]bool 包内文件路径, error 读取或取消错误
func (r *Reader) documentFiles(ctx context.Context, index int) (map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if index < 0 || index >= r.DocumentCount() {
		return nil, fmt.Errorf("document index out of range: %d", index)
	}
	queue := []string{cleanPackagePath(r.OFD.DocBody[index].DocRoot)}
	data, err := r.readFile("OFD.xml")
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	body := root.childAt("DocBody", index)
	if body == nil {
		return nil, fmt.Errorf("document body not found: %d", index)
	}
	var collect func(*editorXML)
	collect = func(node *editorXML) {
		if node.name.Local == "Cover" || node.name.Local == "Signatures" || node.name.Local == "DocRoot" {
			if value := strings.TrimSpace(editorImportText(data, node)); value != "" {
				queue = append(queue, resolveResourcePath("OFD.xml", "", value))
			}
		}
		if node.name.Local == "Version" && node.attr("BaseLoc") != "" {
			queue = append(queue, resolveResourcePath("OFD.xml", "", node.attr("BaseLoc")))
		}
		for _, child := range node.children {
			collect(child)
		}
	}
	collect(body)
	seen := make(map[string]bool)
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		actual := queue[0]
		queue = queue[1:]
		if seen[actual] {
			continue
		}
		seen[actual] = true
		if !r.fileNames[actual] {
			continue
		}
		input, err := r.openFile(actual)
		if err != nil {
			return nil, err
		}
		buffer := bufio.NewReader(imageInput{ReadCloser: input, context: ctx})
		if prefix, _ := buffer.Peek(3); bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
			buffer.Discard(3)
		}
		header, _ := buffer.Peek(128)
		for len(header) > 0 && len(bytes.TrimSpace(header)) == 0 {
			buffer.Discard(len(header))
			header, _ = buffer.Peek(128)
		}
		if !bytes.HasPrefix(bytes.TrimSpace(header), []byte("<")) {
			input.Close()
			continue
		}
		decoder := xml.NewDecoder(buffer)
		base, resource, depth := "", false, 0
		var text strings.Builder
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				input.Close()
				return nil, err
			}
			switch node := token.(type) {
			case xml.StartElement:
				if depth == 0 && classifyOFDNamespace(node.Name.Space) == ofdXMLUnknown {
					input.Close()
					goto nextFile
				}
				if classifyOFDNamespace(node.Name.Space) == ofdXMLUnknown {
					input.Close()
					return nil, fmt.Errorf("cannot determine document dependencies in %s", actual)
				}
				if depth == 0 {
					resource = node.Name.Local == "Res"
				}
				for _, attr := range node.Attr {
					if resource && depth == 0 && attr.Name.Local == "BaseLoc" {
						base = attr.Value
					} else if attr.Name.Local == "BaseLoc" || attr.Name.Local == "FileLoc" || node.Name.Local == "DrawParam" && attr.Name.Local == "Link" || node.Name.Local == "ColorSpace" && attr.Name.Local == "Profile" {
						queue = append(queue, resolveResourcePath(actual, base, attr.Value))
					}
				}
				depth++
				text.Reset()
			case xml.CharData:
				text.Write(node)
			case xml.EndElement:
				switch node.Name.Local {
				case "DocRoot", "Signatures", "Cover", "PublicRes", "DocumentRes", "PageRes", "Annotations", "Attachments", "FileLoc", "Profile", "SignedValue", "SchemaLoc", "ExtendData", "File", "FontFile", "MediaFile", "CustomTags", "Extensions":
					if value := strings.TrimSpace(text.String()); value != "" {
						queue = append(queue, resolveResourcePath(actual, base, value))
					}
				}
				depth--
				text.Reset()
			}
		}
		input.Close()
	nextFile:
	}
	return seen, ctx.Err()
}
