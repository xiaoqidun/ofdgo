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
	"maps"
	"mime"
	"strings"

	"github.com/xiaoqidun/pdfgo"
)

// screenAnnotation 转换屏幕注解的音视频呈现，不忽略必须遵守的播放器约束
// 入参: ctx 取消上下文, page PDF页面, annotation 屏幕注解, strict 严格检查开关
// 返回: error 动作、媒体或外观错误
func (p *pdfImporter) screenAnnotation(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, strict bool) error {
	if annotation.Dictionary["A"] == nil {
		return p.appearanceAnnotation(ctx, page, annotation)
	}
	appearance := annotation
	appearance.Dictionary = maps.Clone(annotation.Dictionary)
	delete(appearance.Dictionary, "A")
	additional, err := p.reader.Resolve(annotation.Dictionary["AA"])
	if err != nil {
		return err
	}
	if dict, ok := additional.(pdfgo.Dictionary); ok {
		dict = maps.Clone(dict)
		delete(dict, "U")
		appearance.Dictionary["AA"] = dict
	}
	if appearance.Dictionary["AP"] == nil {
		appearance.Dictionary["AP"] = pdfgo.Dictionary{"N": &pdfgo.Stream{Dictionary: pdfgo.Dictionary{
			"Type": pdfgo.Name("XObject"), "Subtype": pdfgo.Name("Form"), "BBox": pdfgo.Array{pdfgo.Integer(0), pdfgo.Integer(0), pdfgo.Integer(1), pdfgo.Integer(1)},
		}}}
	}
	flags, err := p.reader.ReadAnnotationFlags(annotation)
	if err != nil {
		return err
	}
	if flags&64 != 0 {
		return p.appearanceAnnotation(ctx, page, appearance)
	}
	var actions []Action
	currentPage := p.page
	if err := p.reader.WalkActions(ctx, annotation.Dictionary["A"], func(info pdfgo.ActionInfo) error {
		current := annotation
		current.Dictionary = pdfgo.Dictionary{"A": info.Dictionary}
		converted, err := p.linkActions(current, strict, &currentPage)
		actions = append(actions, converted...)
		return err
	}); err != nil {
		return err
	}
	return p.appearanceAnnotation(ctx, page, appearance, actions...)
}

// renditionLinkActions 按屏幕关联转换播放及控制，切换视频时先停止其他候选
// 入参: object PDF媒体动作, source 触发注解，零值表示非图元动作, strict 是否禁止语义损失, currentPage 动作链当前页
// 返回: []Action 有序OFD动作，无法转换时为空, error 参数、资源或严格模式错误
func (p *pdfImporter) renditionLinkActions(object pdfgo.Object, source pdfgo.Annotation, strict bool, currentPage int) ([]Action, error) {
	playback, err := p.reader.ReadRenditionPlayback(p.ctx, object)
	if err != nil {
		if !strict && errors.Is(err, pdfgo.ErrInvalidAction) {
			p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("invalid PDF rendition action ignored: %v", err)})
			return nil, nil
		}
		return nil, err
	}
	if playback.Operation == nil || *playback.Operation < 0 || *playback.Operation >= 4 {
		return nil, p.renditionActionLoss("conditional rendition playback or script", strict)
	}
	if _, ok := p.pages[playback.Page]; !ok {
		return nil, p.renditionActionLoss("screen page is outside the document", strict)
	}
	if *playback.Operation != 0 {
		return p.renditionControls(playback.Annotation.Reference, *playback.Operation, "", strict)
	}
	selected, err := playback.Rendition.Select(p.ctx, func(candidate *pdfgo.Rendition) (bool, error) {
		_, _, viable, err := p.nativeRendition(candidate)
		return viable, err
	})
	if err != nil {
		return nil, err
	}
	if selected == nil {
		return nil, p.renditionActionLoss("no equivalent media rendition", strict)
	}
	converted, kind, _, err := p.nativeRendition(selected)
	if err != nil {
		return nil, err
	}
	if kind == "Video" {
		parameters, _, err := p.renditionParameters(selected.Screen, map[pdfgo.Name]func(pdfgo.Object) bool{
			"W": func(value pdfgo.Object) bool { return value == pdfgo.Integer(0) || value == pdfgo.Integer(3) },
		})
		if err != nil {
			return nil, err
		}
		window := pdfgo.Integer(3)
		if value, ok := parameters["W"].(pdfgo.Integer); ok {
			window = value
		}
		matches := window == 0 && source.Subtype == ""
		if window == 3 && source.Subtype != "" {
			index := p.pageIndexes[playback.Page]
			matches = currentPage == index && p.page == index
			if matches {
				if err := p.moviePlaybackBounds(converted.Movie, source, playback.Annotation, currentPage, strict); err != nil {
					return nil, err
				}
			}
		}
		if !matches {
			if err := p.renditionActionLoss("media playback window differs in OFD", strict); err != nil {
				return nil, err
			}
		}
	}
	id, err := p.renditionResource(selected, kind, playback)
	if err != nil || id == "" {
		return nil, err
	}
	controls, err := p.renditionControls(playback.Annotation.Reference, 1, id, strict)
	if err != nil || controls == nil {
		return nil, err
	}
	if kind == "Audio" {
		converted.Sound.ResourceID = id
	} else {
		converted.Movie.ResourceID = id
	}
	if len(selected.Play) != 0 || len(selected.Screen) != 0 || len(selected.Clip.BestEffort) != 0 {
		p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF media rendition converted; best-effort presentation preferences may differ in OFD"})
	}
	return append(controls, converted), nil
}

// renditionResource 注册所选媒体，按屏幕区分播放实例并共用原始文件
// 入参: selected 已选择的原生呈现, kind 媒体类型, playback 播放目标
// 返回: string 资源标识，不可用时为空, error 文件或资源错误
func (p *pdfImporter) renditionResource(selected *pdfgo.Rendition, kind string, playback pdfgo.RenditionPlayback) (string, error) {
	format := pdfMediaFormat(*selected.Clip.File)
	if format == "" {
		mediaType, _, _ := mime.ParseMediaType(selected.Clip.ContentType)
		format = strings.TrimPrefix(strings.SplitN(mediaType, "/", 2)[1], "x-")
	}
	id, err := p.mediaResource(p.ctx, *selected.Clip.File, kind, format, false)
	if err != nil || id == "" {
		return "", err
	}
	return p.mediaInstance(id, playback.Annotation, p.pageIndexes[playback.Page])
}

// renditionActionLoss 按导入策略报告媒体控制或呈现差异，不执行脚本
// 入参: reason 无法等价转换的原因, strict 是否禁止语义损失
// 返回: error 严格模式错误
func (p *pdfImporter) renditionActionLoss(reason string, strict bool) error {
	if strict {
		return &pdfgo.UnsupportedError{Feature: reason}
	}
	p.warning(pdfgo.Diagnostic{Message: "PDF rendition action: " + reason})
	return nil
}

// nativeRendition 检查可等价转换的媒体分支，视频不接受无法表达的音量及循环
// 入参: rendition 媒体呈现或选择器
// 返回: Action 播放动作, string 媒体类别, bool 是否可用, error 参数错误
func (p *pdfImporter) nativeRendition(rendition *pdfgo.Rendition) (Action, string, bool, error) {
	result := Action{Event: "CLICK"}
	viable, err := p.renditionCriteria(rendition)
	if err != nil || !viable {
		return result, "", false, err
	}
	if rendition.Subtype == "SR" {
		return result, "", true, nil
	}
	clip := rendition.Clip
	if clip == nil || clip.Subtype != "MCD" || clip.File == nil || len(clip.MustHonor) != 0 || len(clip.Players) != 0 {
		return result, "", false, nil
	}
	if clip.File.Embedded == nil && p.resolveFile == nil {
		return result, "", false, nil
	}
	for key, object := range clip.Permissions {
		value, err := p.reader.Resolve(object)
		if err != nil {
			return result, "", false, err
		}
		if key == "Type" && value == pdfgo.Name("MediaPermissions") {
			continue
		}
		if key != "TF" {
			return result, "", false, nil
		}
		if _, ok := value.(pdfgo.String); !ok {
			return result, "", false, fmt.Errorf("invalid media temporary-file permission")
		}
	}
	mediaType, _, err := mime.ParseMediaType(clip.ContentType)
	if err != nil || !strings.HasPrefix(mediaType, "audio/") && !strings.HasPrefix(mediaType, "video/") {
		return result, "", false, nil
	}
	audio := strings.HasPrefix(mediaType, "audio/")
	play, viable, err := p.renditionParameters(rendition.Play, map[pdfgo.Name]func(pdfgo.Object) bool{
		"V": func(value pdfgo.Object) bool {
			number, ok := value.(pdfgo.Integer)
			return ok && number >= 0 && number <= 100 && (audio || number == 100)
		},
		"RC": func(value pdfgo.Object) bool {
			return value == pdfgo.Integer(1) || value == pdfgo.Real(1) || audio && (value == pdfgo.Integer(0) || value == pdfgo.Real(0))
		},
		"A": func(value pdfgo.Object) bool { return value == pdfgo.Boolean(true) },
		"F": func(value pdfgo.Object) bool { return value == pdfgo.Integer(0) || value == pdfgo.Integer(5) },
	})
	if err != nil || !viable {
		return result, "", false, err
	}
	screen, viable, err := p.renditionParameters(rendition.Screen, map[pdfgo.Name]func(pdfgo.Object) bool{
		"W": func(value pdfgo.Object) bool { return !audio && value == pdfgo.Integer(0) || value == pdfgo.Integer(3) },
	})
	if err != nil || !viable {
		return result, "", false, err
	}
	if screen["W"] == pdfgo.Integer(0) {
		valid, err := p.renditionFloatingWindow(rendition.Screen)
		if err != nil || !valid {
			return result, "", false, err
		}
	}
	volume := 100
	if value := play["V"]; value != nil {
		volume = int(value.(pdfgo.Integer))
	}
	repeat := false
	if value := play["RC"]; value != nil && value != pdfgo.Integer(1) && value != pdfgo.Real(1) {
		repeat = true
	}
	if audio {
		result.Sound = &Sound{Volume: &volume, Repeat: repeat}
		return result, "Audio", true, nil
	}
	result.Movie = &Movie{Operator: "Play"}
	return result, "Video", true, nil
}

// renditionFloatingWindow 校验浮窗必需的像素尺寸，呈现偏好不写成视频固有尺寸
// 入参: screen 媒体屏幕参数
// 返回: bool 是否具备合法尺寸, error 引用错误
func (p *pdfImporter) renditionFloatingWindow(screen pdfgo.Dictionary) (bool, error) {
	value, err := p.reader.Resolve(screen["BE"])
	if err != nil {
		return false, err
	}
	preferences, _ := value.(pdfgo.Dictionary)
	value, err = p.reader.Resolve(preferences["F"])
	if err != nil {
		return false, err
	}
	window, ok := value.(pdfgo.Dictionary)
	if !ok {
		return false, nil
	}
	value, err = p.reader.Resolve(window["D"])
	if err != nil {
		return false, err
	}
	size, ok := value.(pdfgo.Array)
	if !ok || len(size) != 2 {
		return false, nil
	}
	for _, part := range size {
		value, err = p.reader.Resolve(part)
		if err != nil {
			return false, err
		}
		number, ok := value.(pdfgo.Integer)
		if !ok || number < 0 {
			return false, nil
		}
	}
	return true, nil
}

// renditionCriteria 检查媒体环境条件，未知尽力处理字段不影响候选可用性
// 入参: rendition 媒体呈现或选择器
// 返回: bool 是否可用, error 条件字典或引用错误
func (p *pdfImporter) renditionCriteria(rendition *pdfgo.Rendition) (bool, error) {
	for _, constraints := range []struct {
		dict     pdfgo.Dictionary
		required bool
	}{{rendition.MustHonor, true}, {rendition.BestEffort, false}} {
		for key, object := range constraints.dict {
			if key != "C" {
				if constraints.required {
					return false, nil
				}
				continue
			}
			value, err := p.reader.Resolve(object)
			if err != nil {
				return false, err
			}
			criteria, ok := value.(pdfgo.Dictionary)
			if !ok {
				return false, fmt.Errorf("invalid media criteria")
			}
			for name, object := range criteria {
				value, err := p.reader.Resolve(object)
				if err != nil {
					return false, err
				}
				if name == "Type" && value == pdfgo.Name("MediaCriteria") {
					continue
				}
				switch name {
				case "Type", "A", "C", "O", "S", "R", "D", "Z", "V", "P", "L":
					return false, nil
				default:
					if constraints.required {
						return false, nil
					}
				}
			}
		}
	}
	return true, nil
}

// renditionParameters 合并可等价表达的播放参数，不支持的偏好不阻断播放
// 入参: dict 参数字典, supported 字段及可表达值的检查
// 返回: pdfgo.Dictionary 合并后的值, bool 是否可用, error 字典或引用错误
func (p *pdfImporter) renditionParameters(dict pdfgo.Dictionary, supported map[pdfgo.Name]func(pdfgo.Object) bool) (pdfgo.Dictionary, bool, error) {
	values := make(pdfgo.Dictionary)
	for key := range dict {
		if key != "Type" && key != "MH" && key != "BE" {
			return nil, false, nil
		}
	}
	value, err := p.reader.Resolve(dict["MH"])
	if err != nil {
		return nil, false, err
	}
	mandatory, _ := value.(pdfgo.Dictionary)
	for _, key := range []pdfgo.Name{"BE", "MH"} {
		value, err := p.reader.Resolve(dict[key])
		if err != nil {
			return nil, false, err
		}
		if value == nil {
			continue
		}
		parameters, ok := value.(pdfgo.Dictionary)
		if !ok {
			return nil, false, fmt.Errorf("invalid media playback constraints")
		}
		for name, object := range parameters {
			if _, overridden := mandatory[name]; key == "BE" && overridden {
				continue
			}
			check := supported[name]
			if check == nil {
				if key == "MH" {
					return nil, false, nil
				}
				continue
			}
			resolved, err := p.reader.Resolve(object)
			if err != nil {
				return nil, false, err
			}
			if !check(resolved) {
				if key == "MH" {
					return nil, false, nil
				}
				continue
			}
			values[name] = resolved
		}
	}
	return values, true, nil
}
