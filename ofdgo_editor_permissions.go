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
	"bytes"
	"encoding/xml"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

// 视图首选项枚举、Document节点顺序与日期时间解析格式
var (
	vPreferencesPageModes   = []string{"None", "FullScreen", "UseOutlines", "UseThumbs", "UseCustomTags", "UseLayers", "UseAttatchs", "UseBookmarks"}
	vPreferencesPageLayouts = []string{"OnePage", "OneColumn", "TwoPageL", "TwoColumnL", "TwoPageR", "TwoColumnR"}
	vPreferencesTabDisplays = []string{"FileName", "DocTitle"}
	vPreferencesZoomModes   = []string{"Default", "FitHeight", "FitWidth", "FitRect"}
	permissionsAfterOrder   = []string{"Actions", "VPreferences", "Bookmarks", "Annotations", "CustomTags", "Attachments", "Extensions"}
	vPreferencesAfterOrder  = []string{"Bookmarks", "Annotations", "CustomTags", "Attachments", "Extensions"}
	ofdDateTimeLayouts      = []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05", "2006-01-02"}
)

// clonePermissions 复制文档权限结构体及其有效期指针
// 入参: p 原始权限指针
// 返回: *Permissions 独立副本
func clonePermissions(p *Permissions) *Permissions {
	if p == nil {
		return nil
	}
	copied := *p
	if p.ValidPeriod != nil {
		period := *p.ValidPeriod
		copied.ValidPeriod = &period
	}
	return &copied
}

// cloneVPreferences 复制视图首选项结构体及其指针字段
// 入参: v 原始视图首选项指针
// 返回: *VPreferences 独立副本
func cloneVPreferences(v *VPreferences) *VPreferences {
	if v == nil {
		return nil
	}
	copied := *v
	if v.HideToolbar != nil {
		value := *v.HideToolbar
		copied.HideToolbar = &value
	}
	if v.HideMenubar != nil {
		value := *v.HideMenubar
		copied.HideMenubar = &value
	}
	if v.HideWindowUI != nil {
		value := *v.HideWindowUI
		copied.HideWindowUI = &value
	}
	if v.Zoom != nil {
		value := *v.Zoom
		copied.Zoom = &value
	}
	return &copied
}

// validatePermissions 校验权限声明中的份数上限与有效期日期合法性
// 入参: p 待校验权限声明
// 返回: error 校验错误
func validatePermissions(p *Permissions) error {
	if p == nil {
		return nil
	}
	if p.Copies < -1 {
		return fmt.Errorf("invalid print copies limit: %d", p.Copies)
	}
	if p.ValidPeriod != nil {
		startText := strings.TrimSpace(p.ValidPeriod.StartDate)
		endText := strings.TrimSpace(p.ValidPeriod.EndDate)
		var startTime, endTime time.Time
		var err error
		if startText != "" {
			startTime, err = parseOFDDateTime(startText)
			if err != nil {
				return fmt.Errorf("invalid valid period start date %q: %w", startText, err)
			}
		}
		if endText != "" {
			endTime, err = parseOFDDateTime(endText)
			if err != nil {
				return fmt.Errorf("invalid valid period end date %q: %w", endText, err)
			}
		}
		if startText != "" && endText != "" && startTime.After(endTime) {
			return fmt.Errorf("valid period start date %q is after end date %q", startText, endText)
		}
	}
	return nil
}

// validateVPreferences 校验视图首选项枚举取值及ZoomMode与Zoom互斥规则
// 入参: v 待校验视图首选项
// 返回: error 校验错误
func validateVPreferences(v *VPreferences) error {
	if v == nil {
		return nil
	}
	pageMode := strings.TrimSpace(v.PageMode)
	if pageMode != "" && !slices.Contains(vPreferencesPageModes, pageMode) {
		return fmt.Errorf("invalid PageMode %q", v.PageMode)
	}
	pageLayout := strings.TrimSpace(v.PageLayout)
	if pageLayout != "" && !slices.Contains(vPreferencesPageLayouts, pageLayout) {
		return fmt.Errorf("invalid PageLayout %q", v.PageLayout)
	}
	tabDisplay := strings.TrimSpace(v.TabDisplay)
	if tabDisplay != "" && !slices.Contains(vPreferencesTabDisplays, tabDisplay) {
		return fmt.Errorf("invalid TabDisplay %q", v.TabDisplay)
	}
	zoomMode := strings.TrimSpace(v.ZoomMode)
	if zoomMode != "" && !slices.Contains(vPreferencesZoomModes, zoomMode) {
		return fmt.Errorf("invalid ZoomMode %q", v.ZoomMode)
	}
	if v.Zoom != nil {
		if !finite(*v.Zoom) || *v.Zoom <= 0 {
			return fmt.Errorf("invalid Zoom value %v", *v.Zoom)
		}
		if zoomMode != "" {
			return fmt.Errorf("ZoomMode and Zoom are mutually exclusive")
		}
	}
	return nil
}

// normalizePermissions 规范化权限声明的有效期字符串
// 入参: p 原始权限声明
// 返回: Permissions 规范化后的独立值
func normalizePermissions(p Permissions) Permissions {
	if p.ValidPeriod != nil {
		start := strings.TrimSpace(p.ValidPeriod.StartDate)
		end := strings.TrimSpace(p.ValidPeriod.EndDate)
		if start == "" && end == "" {
			p.ValidPeriod = nil
		} else {
			p.ValidPeriod = &ValidPeriod{StartDate: start, EndDate: end}
		}
	}
	return p
}

// normalizeVPreferences 按GB/T 33190标准默认值补齐视图首选项字段
// 入参: v 原始视图首选项
// 返回: VPreferences 规范化后的独立值
func normalizeVPreferences(v VPreferences) VPreferences {
	copied := *cloneVPreferences(&v)
	copied.PageMode = strings.TrimSpace(copied.PageMode)
	if copied.PageMode == "" {
		copied.PageMode = "None"
	}
	copied.PageLayout = strings.TrimSpace(copied.PageLayout)
	if copied.PageLayout == "" {
		copied.PageLayout = "OneColumn"
	}
	copied.TabDisplay = strings.TrimSpace(copied.TabDisplay)
	if copied.TabDisplay == "" {
		copied.TabDisplay = "DocTitle"
	}
	if copied.HideToolbar == nil {
		copied.HideToolbar = new(bool)
	}
	if copied.HideMenubar == nil {
		copied.HideMenubar = new(bool)
	}
	if copied.HideWindowUI == nil {
		copied.HideWindowUI = new(bool)
	}
	copied.ZoomMode = strings.TrimSpace(copied.ZoomMode)
	if copied.Zoom != nil {
		copied.ZoomMode = ""
	} else if copied.ZoomMode == "" {
		copied.ZoomMode = "Default"
	}
	return copied
}

// parseOFDDateTime 解析OFD标准支持的日期时间或日期文本
// 入参: value 日期文本
// 返回: time.Time 解析时间, error 格式错误
func parseOFDDateTime(value string) (time.Time, error) {
	for _, layout := range ofdDateTimeLayouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("expected ISO-8601 date or dateTime")
}

// patchDocumentPermissionsXML 按GB/T 33190节顺序保真更新Document.xml中的Permissions节点
// 入参: data 原Document.xml字节, p 新权限声明，nil表示删除
// 返回: []byte 补丁后的Document.xml, error 解析或编码错误
func patchDocumentPermissionsXML(data []byte, p *Permissions) ([]byte, error) {
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	ctx := root
	if existing := root.child("Permissions"); existing != nil {
		ctx = existing
	}
	encoded, err := encodePermissionsXML(ctx, p)
	if err != nil {
		return nil, err
	}
	return editorPatchDocumentNode(data, root, "Permissions", permissionsAfterOrder, encoded), nil
}

// patchDocumentVPreferencesXML 按GB/T 33190节顺序保真更新Document.xml中的VPreferences节点
// 入参: data 原Document.xml字节, v 新视图首选项，nil表示删除
// 返回: []byte 补丁后的Document.xml, error 解析或编码错误
func patchDocumentVPreferencesXML(data []byte, v *VPreferences) ([]byte, error) {
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	ctx := root
	if existing := root.child("VPreferences"); existing != nil {
		ctx = existing
	}
	encoded, err := encodeVPreferencesXML(ctx, v)
	if err != nil {
		return nil, err
	}
	return editorPatchDocumentNode(data, root, "VPreferences", vPreferencesAfterOrder, encoded), nil
}

// encodePermissionsXML 按GB/T 33190表7顺序编码Permissions节点
// 入参: ctx 命名空间上下文节点, p 权限声明
// 返回: []byte 编码结果，p为nil时返回nil, error 编码错误
func encodePermissionsXML(ctx *editorXML, p *Permissions) ([]byte, error) {
	if p == nil {
		return nil, nil
	}
	var content []byte
	content = append(content, ctx.textXML("Edit", strconv.FormatBool(p.Edit))...)
	content = append(content, ctx.textXML("Annot", strconv.FormatBool(p.Annot))...)
	content = append(content, ctx.textXML("Export", strconv.FormatBool(p.Export))...)
	content = append(content, ctx.textXML("Signature", strconv.FormatBool(p.Signature))...)
	content = append(content, ctx.textXML("Watermark", strconv.FormatBool(p.Watermark))...)
	content = append(content, ctx.textXML("PrintScreen", strconv.FormatBool(p.PrintScreen))...)
	printAttrs := ofdAttrs{{Name: xml.Name{Local: "Printable"}, Value: strconv.FormatBool(p.Print)}}
	if p.Copies >= 0 {
		printAttrs = append(printAttrs, xml.Attr{Name: xml.Name{Local: "Copies"}, Value: strconv.Itoa(p.Copies)})
	}
	printNode, err := ctx.containerXML("Print", printAttrs, nil)
	if err != nil {
		return nil, err
	}
	content = append(content, printNode...)
	if p.ValidPeriod != nil && (p.ValidPeriod.StartDate != "" || p.ValidPeriod.EndDate != "") {
		var periodAttrs ofdAttrs
		if p.ValidPeriod.StartDate != "" {
			periodAttrs = append(periodAttrs, xml.Attr{Name: xml.Name{Local: "StartDate"}, Value: p.ValidPeriod.StartDate})
		}
		if p.ValidPeriod.EndDate != "" {
			periodAttrs = append(periodAttrs, xml.Attr{Name: xml.Name{Local: "EndDate"}, Value: p.ValidPeriod.EndDate})
		}
		periodNode, err := ctx.containerXML("ValidPeriod", periodAttrs, nil)
		if err != nil {
			return nil, err
		}
		content = append(content, periodNode...)
	}
	return ctx.containerXML("Permissions", nil, content)
}

// encodeVPreferencesXML 按GB/T 33190表8顺序编码VPreferences节点
// 入参: ctx 命名空间上下文节点, v 视图首选项
// 返回: []byte 编码结果，v为nil时返回nil, error 编码错误
func encodeVPreferencesXML(ctx *editorXML, v *VPreferences) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	var content []byte
	if v.PageMode != "" {
		content = append(content, ctx.textXML("PageMode", v.PageMode)...)
	}
	if v.PageLayout != "" {
		content = append(content, ctx.textXML("PageLayout", v.PageLayout)...)
	}
	if v.TabDisplay != "" {
		content = append(content, ctx.textXML("TabDisplay", v.TabDisplay)...)
	}
	if v.HideToolbar != nil {
		content = append(content, ctx.textXML("HideToolbar", strconv.FormatBool(*v.HideToolbar))...)
	}
	if v.HideMenubar != nil {
		content = append(content, ctx.textXML("HideMenubar", strconv.FormatBool(*v.HideMenubar))...)
	}
	if v.HideWindowUI != nil {
		content = append(content, ctx.textXML("HideWindowUI", strconv.FormatBool(*v.HideWindowUI))...)
	}
	if v.Zoom != nil {
		content = append(content, ctx.textXML("Zoom", ofdNumber(*v.Zoom))...)
	} else if v.ZoomMode != "" {
		content = append(content, ctx.textXML("ZoomMode", v.ZoomMode)...)
	}
	return ctx.containerXML("VPreferences", nil, content)
}

// editorPatchDocumentNode 替换、删除或按标准节顺序插入Document根节点下的单实例子节点
// 入参: data 原XML, root 文档根节点, local 节点本地名, after 其后的标准节点集合, encoded 新节点字节
// 返回: []byte 补丁后的XML
func editorPatchDocumentNode(data []byte, root *editorXML, local string, after []string, encoded []byte) []byte {
	var patches []editorXMLPatch
	matched := false
	for _, child := range root.children {
		if !child.matchesOFD(local) {
			continue
		}
		if !matched {
			patches = append(patches, editorXMLPatch{child.start, child.end, encoded})
			matched = true
		} else {
			patches = append(patches, editorXMLPatch{child.start, child.end, nil})
		}
	}
	if matched {
		return editorPatchXML(data, patches)
	}
	if len(encoded) == 0 {
		return data
	}
	if root.open == root.end {
		return editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, root, encoded)})
	}
	position := root.close
	for _, child := range root.children {
		if child.matchesOFD(child.name.Local) && slices.Contains(after, child.name.Local) {
			position = child.start
			break
		}
	}
	return editorPatchXML(data, []editorXMLPatch{{position, position, encoded}})
}

// VPreferences 获取文档视图首选项的独立副本，未声明时返回nil，不加载注解或签名
// 返回: *VPreferences 视图首选项, error 错误信息
func (r *Reader) VPreferences() (*VPreferences, error) {
	doc, err := r.docStructure()
	if err != nil {
		return nil, err
	}
	return cloneVPreferences(doc.VPreferences), nil
}

// Permissions 获取当前文档显式声明的权限副本，未声明时返回nil
// 返回: *Permissions 权限声明副本, error 读取错误
func (e *Editor) Permissions() (*Permissions, error) {
	if e.source == nil || e.source.document == nil || !e.source.document.hasPermissions {
		return nil, nil
	}
	return clonePermissions(&e.source.document.Permissions), nil
}

// SetPermissions 设置或清除当前文档的Permissions声明并计入撤销历史，nil表示清除
// 入参: p 权限声明，nil时删除Permissions节点
// 返回: error 校验、编码或提交错误
func (e *Editor) SetPermissions(p *Permissions) error {
	if err := validatePermissions(p); err != nil {
		return err
	}
	current, err := e.Permissions()
	if err != nil {
		return err
	}
	var next *Permissions
	if p != nil {
		normalized := normalizePermissions(*p)
		next = &normalized
	}
	if reflect.DeepEqual(current, next) {
		return nil
	}
	return e.patchDocumentSection(func(data []byte) ([]byte, error) {
		return patchDocumentPermissionsXML(data, next)
	})
}

// VPreferences 获取当前文档显式声明的视图首选项副本，未声明时返回nil
// 返回: *VPreferences 视图首选项副本, error 读取错误
func (e *Editor) VPreferences() (*VPreferences, error) {
	if e.source == nil || e.source.document == nil || e.source.document.VPreferences == nil {
		return nil, nil
	}
	return cloneVPreferences(e.source.document.VPreferences), nil
}

// SetVPreferences 设置或清除当前文档的VPreferences声明并计入撤销历史，nil表示清除
// 入参: v 视图首选项，nil时删除VPreferences节点
// 返回: error 校验、编码或提交错误
func (e *Editor) SetVPreferences(v *VPreferences) error {
	if err := validateVPreferences(v); err != nil {
		return err
	}
	current, err := e.VPreferences()
	if err != nil {
		return err
	}
	var next *VPreferences
	if v != nil {
		normalized := normalizeVPreferences(*v)
		next = &normalized
	}
	if reflect.DeepEqual(current, next) {
		return nil
	}
	return e.patchDocumentSection(func(data []byte) ([]byte, error) {
		return patchDocumentVPreferencesXML(data, next)
	})
}

// patchDocumentSection 对当前文档根XML应用保真补丁并提交可撤销快照
// 入参: apply 文档XML补丁函数
// 返回: error 准备、解析或提交错误
func (e *Editor) patchDocumentSection(apply func([]byte) ([]byte, error)) error {
	base := e.source
	var err error
	if base == nil {
		base, err = e.importBase()
		if err != nil {
			return err
		}
	}
	name := base.reader.documentRoot
	data, err := base.reader.readFile(name)
	if err != nil {
		return err
	}
	updated, err := apply(data)
	if err != nil {
		return err
	}
	if bytes.Equal(data, updated) {
		return nil
	}
	return e.commitAnnotationParts(base, map[string][]byte{name: updated})
}
