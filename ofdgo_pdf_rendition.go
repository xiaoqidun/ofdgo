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
	if appearance.Dictionary["AP"] == nil {
		appearance.Dictionary["AP"] = pdfgo.Dictionary{"N": &pdfgo.Stream{Dictionary: pdfgo.Dictionary{
			"Type": pdfgo.Name("XObject"), "Subtype": pdfgo.Name("Form"), "BBox": pdfgo.Array{pdfgo.Integer(0), pdfgo.Integer(0), pdfgo.Integer(1), pdfgo.Integer(1)},
		}}}
	}
	fallback := func(reason string) error {
		if strict {
			return &pdfgo.UnsupportedError{Feature: reason}
		}
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF screen appearance retained; " + reason})
		delete(appearance.Dictionary, "AA")
		return p.appearanceAnnotation(ctx, page, appearance)
	}
	value, err := p.reader.Resolve(annotation.Dictionary["A"])
	if err != nil {
		return err
	}
	action, ok := value.(pdfgo.Dictionary)
	if !ok {
		return fmt.Errorf("invalid screen action")
	}
	subtype, err := p.reader.Resolve(action["S"])
	if err != nil {
		return err
	}
	operation, err := p.reader.Resolve(action["OP"])
	if err != nil {
		return err
	}
	if subtype != pdfgo.Name("Rendition") || operation != pdfgo.Integer(0) || action["Next"] != nil || annotation.Dictionary["AA"] != nil {
		return fallback("screen action cannot be represented in OFD")
	}
	if target, ok := action["AN"].(pdfgo.Reference); !ok || target != annotation.Reference || annotation.Dictionary["P"] != page.Reference {
		return fallback("screen action target is unavailable")
	}
	rendition, err := p.reader.ReadRendition(ctx, action["R"])
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fallback("media rendition unavailable: " + err.Error())
	}
	selected, err := rendition.Select(ctx, func(candidate *pdfgo.Rendition) (bool, error) {
		_, _, viable, err := p.nativeRendition(candidate)
		return viable, err
	})
	if err != nil {
		return err
	}
	if selected == nil {
		return fallback("no equivalent media rendition")
	}
	converted, kind, _, err := p.nativeRendition(selected)
	if err != nil {
		return err
	}
	data, available, err := p.mediaData(ctx, *selected.Clip.File)
	if err != nil {
		return err
	}
	if !available {
		return p.appearanceAnnotation(ctx, page, appearance)
	}
	format := pdfMediaFormat(*selected.Clip.File)
	if format == "" {
		mediaType, _, _ := mime.ParseMediaType(selected.Clip.ContentType)
		format = strings.TrimPrefix(strings.SplitN(mediaType, "/", 2)[1], "x-")
	}
	id, err := p.editor.AddMedia(kind, format, data)
	if err != nil {
		return err
	}
	if kind == "Audio" {
		converted.Sound.ResourceID = id
	} else {
		converted.Movie.ResourceID = id
	}
	if len(selected.Play) != 0 || len(selected.Screen) != 0 || len(selected.Clip.BestEffort) != 0 {
		p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF media rendition converted; best-effort presentation preferences may differ in OFD"})
	}
	return p.appearanceAnnotation(ctx, page, appearance, converted)
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
	_, viable, err = p.renditionParameters(rendition.Screen, map[pdfgo.Name]func(pdfgo.Object) bool{
		"W": func(value pdfgo.Object) bool { return value == pdfgo.Integer(3) },
	})
	if err != nil || !viable {
		return result, "", false, err
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
