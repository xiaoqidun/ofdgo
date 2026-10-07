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
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path"
	"strings"

	"github.com/xiaoqidun/pdfgo"
)

// soundResource 注册采样音频或调用方提供的外部音频，不自动访问文件
// 入参: ctx 取消上下文, sound 音频对象
// 返回: string 资源标识，不可用时为空, error 读取或封装错误
func (p *pdfImporter) soundResource(ctx context.Context, sound pdfgo.Sound) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var data []byte
	var err error
	format := "WAV"
	if sound.File != nil {
		var available bool
		data, available, err = p.mediaData(ctx, *sound.File)
		if err != nil || !available {
			return "", err
		}
		format = pdfMediaFormat(*sound.File)
	} else {
		data, err = pdfSoundWAV(sound)
	}
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return p.editor.AddMedia("Audio", format, data)
}

// soundLinkAction 保留音频点击动作，混音、交互限制及音量损失按策略报告
// 入参: object 音频动作, strict 是否禁止播放参数损失
// 返回: *Action 音频动作, error 结构、参数或资源错误
func (p *pdfImporter) soundLinkAction(object pdfgo.Object, strict bool) (*Action, error) {
	source, err := p.reader.ReadSoundAction(p.ctx, object)
	if err != nil {
		return nil, err
	}
	if source.Volume < 0 {
		return nil, &pdfgo.UnsupportedError{Feature: "negative sound action volume conversion"}
	}
	if strict {
		return nil, &pdfgo.UnsupportedError{Feature: "sound action playback policy conversion"}
	}
	volume := int(math.Round(source.Volume * 100))
	if source.Mix {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF sound action mixing policy not transferred to OFD"})
	} else {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF sound action exclusive playback policy not transferred to OFD"})
	}
	if source.Synchronous {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF sound action interaction restriction not transferred to OFD; action waiting retained"})
	}
	if float64(volume)/100 != source.Volume {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF sound action volume rounded to OFD integer volume"})
	}
	id, err := p.soundResource(p.ctx, source.Sound)
	if err != nil || id == "" {
		return nil, err
	}
	return &Action{Event: "CLICK", Sound: &Sound{ResourceID: id, Volume: &volume, Repeat: source.Repeat, Synchronous: source.Synchronous}}, nil
}

// movieAnnotation 转换内嵌视频及其点击播放动作，保留独立海报外观
// 入参: ctx 取消上下文, page PDF页面, annotation 视频注解
// 返回: error 媒体资源或播放参数无法表示时返回错误
func (p *pdfImporter) movieAnnotation(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation) error {
	movie, err := p.reader.ReadMovie(annotation.Dictionary["Movie"])
	if err != nil {
		return err
	}
	activation, err := p.reader.Resolve(annotation.Dictionary["A"])
	if err != nil {
		return err
	}
	if activation == pdfgo.Boolean(false) {
		return p.appearanceAnnotation(ctx, page, annotation)
	}
	if activation != nil && activation != pdfgo.Boolean(true) {
		parameters, err := p.reader.ReadMovieActivation(ctx, activation)
		if err != nil {
			return err
		}
		if parameters.Start != nil && parameters.Start.Value != 0 || parameters.Duration != nil || parameters.Rate != 1 || parameters.Volume != 1 || parameters.ShowControls || parameters.Synchronous || parameters.Mode != "Once" || parameters.FloatingScale != nil {
			return &pdfgo.UnsupportedError{Feature: "movie activation playback parameters conversion"}
		}
	}
	if movie.Rotate != 0 {
		return &pdfgo.UnsupportedError{Feature: "rotated movie playback"}
	}
	data, available, err := p.mediaData(ctx, movie.File)
	if err != nil {
		return err
	}
	if !available {
		return p.appearanceAnnotation(ctx, page, annotation)
	}
	id, err := p.editor.AddMedia("Video", pdfMediaFormat(movie.File), data)
	if err != nil {
		return err
	}
	return p.appearanceAnnotation(ctx, page, annotation, Action{Event: "CLICK", Movie: &Movie{ResourceID: id, Operator: "Play"}})
}

// interactiveAnnotation 保留3D或富媒体的静态外观和原始资源，不执行脚本
// 入参: ctx 取消上下文, page PDF页面, annotation 交互注解, strict 严格检查开关
// 返回: error 外观、资源或不可等价转换错误
func (p *pdfImporter) interactiveAnnotation(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, strict bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strict {
		return &pdfgo.UnsupportedError{Feature: "interactive " + string(annotation.Subtype) + " conversion"}
	}
	var actions []Action
	if annotation.Subtype == "3D" {
		return p.threeDAnnotation(ctx, page, annotation)
	} else {
		media, err := p.reader.ReadRichMedia(ctx, annotation)
		if err != nil {
			return p.interactiveAppearanceFallback(ctx, page, annotation, "metadata", err)
		}
		config := media.Configurations[media.ActiveConfiguration]
		var playing *pdfgo.FileSpecification
		if (config.Subtype == "Sound" || config.Subtype == "Video") && len(config.Instances) == 1 && config.Instances[0].Subtype == config.Subtype {
			condition, err := p.reader.Resolve(media.Activation["Condition"])
			if err != nil {
				return err
			}
			style, err := p.reader.Resolve(media.Presentation["Style"])
			if err != nil {
				return err
			}
			if (condition == nil || condition == pdfgo.Name("XA")) && (style == nil || style == pdfgo.Name("Embedded")) && media.Activation["Scripts"] == nil {
				file := config.Instances[0].Asset
				data, available, err := p.mediaData(ctx, file)
				if err != nil {
					return err
				}
				if available {
					kind := "Video"
					if config.Subtype == "Sound" {
						kind = "Audio"
					}
					id, err := p.editor.AddMedia(kind, pdfMediaFormat(file), data)
					if err != nil {
						return err
					}
					if kind == "Audio" {
						actions = append(actions, Action{Event: "CLICK", Sound: &Sound{ResourceID: id}})
					} else {
						actions = append(actions, Action{Event: "CLICK", Movie: &Movie{ResourceID: id, Operator: "Play"}})
					}
				}
				playing = &file
			}
		}
		files := append([]pdfgo.MediaAsset(nil), media.Assets...)
		seen := make(map[*pdfgo.Stream]bool)
		external := make(map[string]bool)
		if playing != nil {
			if playing.Embedded != nil {
				seen[playing.Embedded] = true
			} else {
				external[string(playing.FileSystem)+"\x00"+playing.Name] = true
			}
		}
		for _, config := range media.Configurations {
			for _, instance := range config.Instances {
				files = append(files, pdfgo.MediaAsset{File: instance.Asset})
			}
		}
		for index, asset := range files {
			key := string(asset.File.FileSystem) + "\x00" + asset.File.Name
			if asset.File.Embedded != nil && seen[asset.File.Embedded] || asset.File.Embedded == nil && external[key] {
				continue
			}
			data, available, err := p.mediaData(ctx, asset.File)
			if err != nil {
				return err
			}
			if available {
				name := path.Base(strings.ReplaceAll(asset.File.Name, "\\", "/"))
				if name == "" || name == "." || name == "/" {
					name = fmt.Sprintf("Media_%d_%d", p.page+1, index+1)
				}
				_, err = p.editor.AddAttachment(name, data)
				if err != nil {
					return err
				}
			}
			if asset.File.Embedded != nil {
				seen[asset.File.Embedded] = true
			} else {
				external[key] = true
			}
		}
	}
	if err := p.appearanceAnnotation(ctx, page, annotation, actions...); err != nil {
		return err
	}
	p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF " + string(annotation.Subtype) + " appearance and available primary assets retained; advanced interaction not transferred to OFD"})
	return nil
}

// interactiveAppearanceFallback 保留交互注解的有效正常外观，取消时不继续转换
// 入参: ctx 取消上下文, page PDF页面, annotation 交互注解, kind 失败资源类型, cause 原始错误
// 返回: error 取消或外观转换错误
func (p *pdfImporter) interactiveAppearanceFallback(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, kind string, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	if err := p.appearanceAnnotation(ctx, page, annotation); err != nil {
		return err
	}
	p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: fmt.Sprintf("PDF %s %s unavailable; normal appearance retained: %v", annotation.Subtype, kind, cause)})
	return nil
}

// mediaData 读取媒体或附件，宽松模式缺少外部读取器时报告资源不可用
// 入参: ctx 取消上下文, file PDF文件说明
// 返回: []byte 原始文件, bool 是否可用, error 解码、外部读取或严格模式错误
func (p *pdfImporter) mediaData(ctx context.Context, file pdfgo.FileSpecification) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if file.Embedded == nil && p.resolveFile == nil && p.warning != nil {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF external file unavailable: " + file.Name + "; appearance retained"})
		return nil, false, nil
	}
	data, err := p.reader.ReadFileData(ctx, file, p.resolveFile)
	return data, err == nil, err
}

// pdfMediaFormat 获取媒体文件扩展名，URL参数和片段不参与格式判断
// 入参: file PDF文件说明
// 返回: string 媒体格式，未声明扩展名时为空
func pdfMediaFormat(file pdfgo.FileSpecification) string {
	name := strings.ReplaceAll(file.Name, "\\", "/")
	if file.FileSystem == "URL" {
		if location, err := url.Parse(name); err == nil {
			name = location.Path
		}
	}
	return strings.TrimPrefix(path.Ext(name), ".")
}

// pdfSoundWAV 封装PDF采样音频，保持采样率、声道及数值，超过RIFF容量时使用RF64
// 入参: sound 音频流及参数
// 返回: []byte WAVE文件, error 不可表示的参数或损坏的采样数据
func pdfSoundWAV(sound pdfgo.Sound) ([]byte, error) {
	if sound.File != nil || sound.Compression != "" {
		return nil, &pdfgo.UnsupportedError{Feature: "external or compressed sound samples"}
	}
	if sound.Rate != math.Trunc(sound.Rate) || sound.Rate > math.MaxUint32 || sound.Channels > math.MaxUint16 || sound.Bits != 8 && sound.Bits != 16 && sound.Bits != 24 && sound.Bits != 32 {
		return nil, &pdfgo.UnsupportedError{Feature: "sound parameters in WAVE"}
	}
	format := uint16(1)
	if sound.Encoding == "muLaw" {
		format = 7
	} else if sound.Encoding == "ALaw" {
		format = 6
	}
	if format != 1 && sound.Bits != 8 {
		return nil, fmt.Errorf("invalid companded sound depth")
	}
	width := sound.Bits / 8
	align := uint64(sound.Channels) * uint64(width)
	rate := uint64(sound.Rate)
	if align > math.MaxUint16 || rate*align > math.MaxUint32 {
		return nil, &pdfgo.UnsupportedError{Feature: "sound block alignment in WAVE"}
	}
	data, err := sound.Stream.Decode()
	if err != nil {
		return nil, err
	}
	if uint64(len(data))%align != 0 {
		return nil, fmt.Errorf("incomplete sound sample frame")
	}
	data = bytes.Clone(data)
	if format == 1 {
		for offset := 0; offset < len(data); offset += width {
			if sound.Encoding == "Raw" && width > 1 || sound.Encoding == "Signed" && width == 1 {
				data[offset] ^= 0x80
			}
			for left, right := offset, offset+width-1; left < right; left, right = left+1, right-1 {
				data[left], data[right] = data[right], data[left]
			}
		}
	}
	formatSize := uint32(16)
	extra := uint64(0)
	if format == 1 && (sound.Bits > 16 || sound.Channels > 2) {
		format = 0xfffe
		formatSize = 40
		extra = 36
	} else if format != 1 {
		formatSize = 18
		extra = 14
	}
	size := uint64(36) + extra + uint64(len(data)) + uint64(len(data)%2)
	large := size > math.MaxUint32
	var out bytes.Buffer
	put16 := func(v uint16) { _ = binary.Write(&out, binary.LittleEndian, v) }
	put32 := func(v uint32) { _ = binary.Write(&out, binary.LittleEndian, v) }
	put64 := func(v uint64) { _ = binary.Write(&out, binary.LittleEndian, v) }
	if large {
		out.WriteString("RF64")
		put32(math.MaxUint32)
		out.WriteString("WAVEds64")
		put32(28)
		put64(size + 36)
		put64(uint64(len(data)))
		put64(uint64(len(data)) / align)
		put32(0)
	} else {
		out.WriteString("RIFF")
		put32(uint32(size))
		out.WriteString("WAVE")
	}
	out.WriteString("fmt ")
	put32(formatSize)
	put16(format)
	put16(uint16(sound.Channels))
	put32(uint32(rate))
	put32(uint32(rate * align))
	put16(uint16(align))
	put16(uint16(sound.Bits))
	if format != 1 {
		if format == 0xfffe {
			put16(22)
			put16(uint16(sound.Bits))
			put32(0)
			out.Write([]byte{1, 0, 0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xaa, 0, 0x38, 0x9b, 0x71})
		} else {
			put16(0)
		}
		out.WriteString("fact")
		put32(4)
		if large {
			put32(math.MaxUint32)
		} else {
			put32(uint32(uint64(len(data)) / align))
		}
	}
	out.WriteString("data")
	if large {
		put32(math.MaxUint32)
	} else {
		put32(uint32(len(data)))
	}
	out.Write(data)
	if len(data)%2 != 0 {
		out.WriteByte(0)
	}
	return out.Bytes(), nil
}
