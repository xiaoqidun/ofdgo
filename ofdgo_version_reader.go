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

import "fmt"

// DocumentRoot 获取当前阅读入口的包内路径，不改写OFD中的主入口
// 未显式选择时使用唯一的Current版本；没有默认版本时使用主入口，多个默认版本返回错误
// 返回: string 阅读入口, error 版本选择或描述错误
func (r *Reader) DocumentRoot() (string, error) {
	if r.documentRoot != "" {
		return r.documentRoot, nil
	}
	if r.documentIndex < 0 || r.documentIndex >= r.DocumentCount() {
		return "", fmt.Errorf("no docbody found")
	}
	body := r.OFD.DocBody[r.documentIndex]
	if r.selectedVersion != nil && *r.selectedVersion == "" || !body.versioned && r.selectedVersion == nil {
		root, err := versionFileLocation("OFD.xml", body.DocRoot)
		if err == nil {
			r.documentRoot = root
		}
		return root, err
	}
	entries, err := r.documentVersionEntries()
	if err != nil {
		return "", err
	}
	var selected *DocumentVersionInfo
	for i := range entries {
		entry := &entries[i]
		if r.selectedVersion != nil && entry.ID == *r.selectedVersion || r.selectedVersion == nil && entry.Current {
			if selected != nil {
				return "", fmt.Errorf("multiple current document versions")
			}
			selected = entry
		}
	}
	if selected == nil {
		if r.selectedVersion != nil {
			return "", fmt.Errorf("document version not found: %s", *r.selectedVersion)
		}
		root, err := versionFileLocation("OFD.xml", body.DocRoot)
		if err == nil {
			r.documentRoot = root
		}
		return root, err
	}
	info, err := r.readDocumentVersion(selected.ID, selected.Location)
	if err != nil {
		return "", fmt.Errorf("version %s: %w", selected.ID, err)
	}
	info.Index, info.Current = selected.Index, selected.Current
	r.documentRoot, r.versionInfo = info.DocRoot, &info
	return r.documentRoot, nil
}

// DocumentVersionID 获取当前阅读的版本标识，主入口返回空字符串
// 返回: string 版本标识, error 版本选择或描述错误
func (r *Reader) DocumentVersionID() (string, error) {
	if _, err := r.DocumentRoot(); err != nil {
		return "", err
	}
	if r.versionInfo == nil {
		return "", nil
	}
	return r.versionInfo.ID, nil
}

// DocumentVersion 创建指定版本的独立阅读器，保留整个包，不改变当前阅读器或共享可变缓存
// 返回值不关闭输入文件，使用期间不得关闭持有输入文件的原阅读器
// 入参: id 版本标识，空值显式选择主入口；Document可重新按Current选择默认版本
// 返回: *Reader 版本阅读器, error 选择或读取错误
func (r *Reader) DocumentVersion(id string) (*Reader, error) {
	next := &Reader{Path: r.Path, Zip: r.Zip, files: r.files, encryption: r.encryption, documentIndex: r.documentIndex, selectedVersion: &id}
	if err := next.initRoot(); err != nil {
		return nil, err
	}
	if _, err := next.Doc(); err != nil {
		return nil, err
	}
	return next, nil
}

// documentEntry 获取指定文档的阅读入口，当前文档保留显式选择，其他文档使用默认版本
// 入参: index 文档索引
// 返回: string 包内路径, error 选择错误
func (r *Reader) documentEntry(index int) (string, error) {
	if index == r.documentIndex {
		return r.DocumentRoot()
	}
	view := *r
	view.documentIndex, view.documentRoot = index, ""
	view.selectedVersion, view.versionInfo = nil, nil
	return view.DocumentRoot()
}

// versionSignatures 按OFD入口解析包级签名路径，选中版本仅使用文件清单内的签名入口
// 入参: location OFD入口中的签名路径
// 返回: string 签名路径，不属于该版本时为空
func (r *Reader) versionSignatures(location string) string {
	if location == "" {
		return ""
	}
	name := resolveResourcePath("OFD.xml", "", location)
	if r.versionInfo == nil {
		return "/" + name
	}
	for _, file := range r.versionInfo.Files {
		if file.Location == name {
			return "/" + name
		}
	}
	return ""
}
