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

// DocumentVersionCount 获取当前文档的版本数量，不读取版本描述文件
// 返回: int 版本数量, error 入口读取或结构错误
func (r *Reader) DocumentVersionCount() (int, error) {
	if r.documentIndex >= 0 && r.documentIndex < r.DocumentCount() && !r.OFD.DocBody[r.documentIndex].versioned {
		return 0, nil
	}
	entries, err := r.documentVersionEntries()
	return len(entries), err
}

// DocumentVersions 按入口顺序读取当前文档的版本信息，不切换文档或读取文件清单中的内容
// 返回独立副本；没有版本列表时返回nil，不以版本号推测默认版本
// 返回: []DocumentVersionInfo 版本信息, error 读取或描述错误
func (r *Reader) DocumentVersions() ([]DocumentVersionInfo, error) {
	entries, err := r.documentVersionEntries()
	if err != nil {
		return nil, err
	}
	for i, entry := range entries {
		info, err := r.readDocumentVersion(entry.ID, entry.Location)
		if err != nil {
			return nil, fmt.Errorf("version %s: %w", entry.ID, err)
		}
		info.Index, info.Current = entry.Index, entry.Current
		entries[i] = info
	}
	return entries, nil
}

// documentVersionEntries 读取当前文档的版本入口，不提前加载各版本描述
// 返回: []DocumentVersionInfo 入口属性, error 读取或描述错误
func (r *Reader) documentVersionEntries() ([]DocumentVersionInfo, error) {
	if r.documentIndex < 0 || r.documentIndex >= r.DocumentCount() {
		return nil, fmt.Errorf("no docbody found")
	}
	data, err := r.readFile("OFD.xml")
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	body := root.childAt("DocBody", r.documentIndex)
	if body == nil {
		return nil, fmt.Errorf("document body not found: %d", r.documentIndex)
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

// readDocumentVersion 读取版本描述，按描述文件位置解析文件清单和文档入口
// 入参: id 入口中的版本标识, location 描述文件路径
// 返回: DocumentVersionInfo 版本信息, error 读取或描述错误
func (r *Reader) readDocumentVersion(id, location string) (DocumentVersionInfo, error) {
	var info DocumentVersionInfo
	data, err := r.readFile(location)
	if err != nil {
		return info, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return info, err
	}
	if root.name.Local != "DocVersion" || strings.TrimSpace(root.attr("ID")) != id {
		return info, fmt.Errorf("invalid version descriptor %s", location)
	}
	list, docRoot := root.child("FileList"), root.child("DocRoot")
	if list == nil || docRoot == nil {
		return info, fmt.Errorf("missing version FileList or DocRoot in %s", location)
	}
	if root.childAt("FileList", 1) != nil || root.childAt("DocRoot", 1) != nil {
		return info, fmt.Errorf("duplicate version FileList or DocRoot in %s", location)
	}
	entry, err := versionFileLocation(location, editorImportText(data, docRoot))
	if err != nil {
		return info, err
	}
	info = DocumentVersionInfo{ID: id, Location: location, Version: root.attr("Version"), Name: root.attr("Name"), CreationDate: root.attr("CreationDate"), DocRoot: entry}
	ids := map[string]bool{id: true}
	found := false
	for _, node := range list.children {
		if !node.matchesOFD("File") {
			continue
		}
		fileID := strings.TrimSpace(node.attr("ID"))
		if !ofdXMLIDValid(fileID) || ids[fileID] {
			return DocumentVersionInfo{}, fmt.Errorf("invalid or duplicate version file ID %q", fileID)
		}
		ids[fileID] = true
		name, err := versionFileLocation(location, editorImportText(data, node))
		if err != nil {
			return DocumentVersionInfo{}, err
		}
		found = found || name == entry
		info.Files = append(info.Files, VersionFile{ID: fileID, Location: name})
	}
	if !found {
		return DocumentVersionInfo{}, fmt.Errorf("version DocRoot absent from FileList: %s", entry)
	}
	return info, nil
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
