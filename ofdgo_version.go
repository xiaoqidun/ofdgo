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
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// DocumentVersionInfo 文档版本信息，Location、DocRoot及Files中的路径均相对于包根目录
// Index为版本号，Version为该版本适用的格式版本，CreationDate保留原始日期文本
type DocumentVersionInfo struct {
	ID           string
	Index        int32
	Current      bool
	Location     string
	Version      string
	Name         string
	CreationDate string
	DocRoot      string
	Files        []VersionFile
}

// VersionFile 文档版本中的文件标识与包内路径
type VersionFile struct {
	ID       string
	Location string
}

// parseVersionEntries 校验一个文档的版本列表，复用已解析的主入口节点
// 入参: ctx 取消上下文, body 文档入口节点
// 返回: []DocumentVersionInfo 版本入口属性, error 结构或取消错误
func parseVersionEntries(ctx context.Context, body *editorXML) ([]DocumentVersionInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	versions := body.child("Versions")
	if versions == nil {
		return nil, nil
	}
	if body.childAt("Versions", 1) != nil {
		return nil, fmt.Errorf("multiple version lists")
	}
	var result []DocumentVersionInfo
	ids := make(map[string]bool)
	for _, node := range versions.children {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !node.matchesOFD("Version") {
			continue
		}
		id := strings.TrimSpace(node.attr("ID"))
		if !ofdXMLIDValid(id) || ids[id] {
			return nil, fmt.Errorf("invalid or duplicate version ID %q", id)
		}
		ids[id] = true
		index, err := strconv.ParseInt(strings.TrimSpace(node.attr("Index")), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid version Index for %s: %w", id, err)
		}
		current := false
		if node.hasAttr("Current") {
			switch strings.TrimSpace(node.attr("Current")) {
			case "true", "1":
				current = true
			case "false", "0":
			default:
				return nil, fmt.Errorf("invalid version Current for %s", id)
			}
		}
		location, err := versionFileLocation("OFD.xml", node.attr("BaseLoc"))
		if err != nil {
			return nil, fmt.Errorf("version %s: %w", id, err)
		}
		result = append(result, DocumentVersionInfo{ID: id, Index: int32(index), Current: current, Location: location})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("empty version list")
	}
	return result, nil
}

// versionFileLocation 解析版本描述中的ST_Loc，拒绝空路径和包外路径
// 入参: owner 所在XML路径, value 引用路径
// 返回: string 包内路径, error 路径错误
func versionFileLocation(owner, value string) (string, error) {
	name := resolveResourcePath(owner, "", value)
	if name == "" || !validPackagePath(name) {
		return "", fmt.Errorf("invalid version file location %q", value)
	}
	return name, nil
}

// DocumentVersionCount 获取当前文档的版本数量，不读取版本描述文件
// 返回: int 版本数量, error 入口读取或结构错误
func (r *Reader) DocumentVersionCount() (int, error) {
	if r.documentIndex >= 0 && r.documentIndex < r.DocumentCount() && !r.ofd.DocBody[r.documentIndex].versioned {
		return 0, nil
	}
	entries, err := r.documentVersionEntries()
	return len(entries), err
}

// DocumentVersions 按入口顺序读取当前文档的版本信息，不切换文档或读取文件清单中的内容
// 返回独立副本；没有版本列表时返回nil，不以版本号推测默认版本
// 返回: []DocumentVersionInfo 版本信息, error 读取或描述错误
func (r *Reader) DocumentVersions() ([]DocumentVersionInfo, error) {
	return r.DocumentVersionsContext(context.Background())
}

// DocumentVersionsContext 按入口顺序读取版本信息，支持取消描述文件和文件清单的解析
// 返回独立副本，不切换当前版本，也不读取文件清单中的内容
// 入参: ctx 取消上下文
// 返回: []DocumentVersionInfo 版本信息, error 读取、描述或取消错误
func (r *Reader) DocumentVersionsContext(ctx context.Context) ([]DocumentVersionInfo, error) {
	entries, err := r.documentVersionEntriesContext(ctx)
	if err != nil {
		return nil, err
	}
	for i, entry := range entries {
		info, err := r.readDocumentVersionContext(ctx, entry.ID, entry.Location)
		if err != nil {
			return nil, fmt.Errorf("version %s: %w", entry.ID, err)
		}
		info.Index, info.Current = entry.Index, entry.Current
		entries[i] = info
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// documentVersionEntries 读取当前文档的版本入口，不提前加载各版本描述
// 返回: []DocumentVersionInfo 入口属性, error 读取或描述错误
func (r *Reader) documentVersionEntries() ([]DocumentVersionInfo, error) {
	return r.documentVersionEntriesContext(context.Background())
}

// documentVersionEntriesContext 读取版本入口，在读取和解析过程中检查取消状态
// 入参: ctx 取消上下文
// 返回: []DocumentVersionInfo 入口属性, error 读取、描述或取消错误
func (r *Reader) documentVersionEntriesContext(ctx context.Context) ([]DocumentVersionInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.documentIndex < 0 || r.documentIndex >= r.DocumentCount() {
		return nil, fmt.Errorf("no docbody found")
	}
	data, err := r.readFileViewContext(ctx, "OFD.xml")
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return nil, err
	}
	body := root.childAt("DocBody", r.documentIndex)
	if body == nil {
		return nil, fmt.Errorf("document body not found: %d", r.documentIndex)
	}
	return parseVersionEntries(ctx, body)
}

// readDocumentVersionContext 流式读取版本描述，按描述文件位置解析清单和文档入口
// 入参: ctx 取消上下文, id 入口中的版本标识, location 描述文件路径
// 返回: DocumentVersionInfo 版本信息, error 读取、描述或取消错误
func (r *Reader) readDocumentVersionContext(ctx context.Context, id, location string) (DocumentVersionInfo, error) {
	if err := ctx.Err(); err != nil {
		return DocumentVersionInfo{}, err
	}
	input, err := r.openFile(location)
	if err != nil {
		return DocumentVersionInfo{}, err
	}
	defer input.Close()
	decoder := xml.NewDecoder(imageInput{ReadCloser: input, ctx: ctx})
	var info DocumentVersionInfo
	var root, parent *editorXML
	var files, entry bool
	ids := map[string]bool{id: true}
	for {
		if err := ctx.Err(); err != nil {
			return DocumentVersionInfo{}, err
		}
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return DocumentVersionInfo{}, err
		}
		switch start := token.(type) {
		case xml.StartElement:
			node := &editorXML{name: start.Name, attrs: start.Attr, parent: parent}
			if parent == nil {
				if root != nil || start.Name.Local != "DocVersion" || strings.TrimSpace(node.attr("ID")) != id {
					return DocumentVersionInfo{}, fmt.Errorf("invalid version descriptor %s", location)
				}
				root, parent = node, node
				info = DocumentVersionInfo{ID: id, Location: location, Version: node.attr("Version"), Name: node.attr("Name"), CreationDate: node.attr("CreationDate")}
				continue
			}
			switch {
			case parent == root && node.matchesOFD("FileList"):
				if files {
					return DocumentVersionInfo{}, fmt.Errorf("duplicate version FileList in %s", location)
				}
				files, parent = true, node
			case parent == root && node.matchesOFD("DocRoot"):
				if entry {
					return DocumentVersionInfo{}, fmt.Errorf("duplicate version DocRoot in %s", location)
				}
				var value string
				if err := decoder.DecodeElement(&value, &start); err != nil {
					return DocumentVersionInfo{}, err
				}
				info.DocRoot, err = versionFileLocation(location, value)
				if err != nil {
					return DocumentVersionInfo{}, err
				}
				entry = true
			case parent != root && node.matchesOFD("File"):
				fileID := strings.TrimSpace(node.attr("ID"))
				if !ofdXMLIDValid(fileID) || ids[fileID] {
					return DocumentVersionInfo{}, fmt.Errorf("invalid or duplicate version file ID %q", fileID)
				}
				ids[fileID] = true
				var value string
				if err := decoder.DecodeElement(&value, &start); err != nil {
					return DocumentVersionInfo{}, err
				}
				name, err := versionFileLocation(location, value)
				if err != nil {
					return DocumentVersionInfo{}, err
				}
				info.Files = append(info.Files, VersionFile{ID: fileID, Location: name})
			default:
				if err := decoder.Skip(); err != nil {
					return DocumentVersionInfo{}, err
				}
			}
		case xml.EndElement:
			parent = parent.parent
		}
	}
	if !files || !entry {
		return DocumentVersionInfo{}, fmt.Errorf("missing version FileList or DocRoot in %s", location)
	}
	for _, file := range info.Files {
		if err := ctx.Err(); err != nil {
			return DocumentVersionInfo{}, err
		}
		if file.Location == info.DocRoot {
			return info, nil
		}
	}
	return DocumentVersionInfo{}, fmt.Errorf("version DocRoot absent from FileList: %s", info.DocRoot)
}
