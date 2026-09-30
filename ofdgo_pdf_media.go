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
	"fmt"
	"math"
	"net/url"
	"path"
	"strings"

	"github.com/xiaoqidun/pdfgo"
)

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
		dict, ok := activation.(pdfgo.Dictionary)
		if !ok {
			return fmt.Errorf("invalid movie activation")
		}
		for key, value := range dict {
			value, err = p.reader.Resolve(value)
			if err != nil {
				return err
			}
			if value == nil {
				continue
			}
			allowed := false
			switch key {
			case "Start":
				allowed = value == pdfgo.Integer(0)
			case "Rate", "Volume":
				allowed = value == pdfgo.Integer(1) || value == pdfgo.Real(1)
			case "ShowControls", "Synchronous":
				allowed = value == pdfgo.Boolean(false)
			case "Mode":
				allowed = value == pdfgo.Name("Once")
			}
			if !allowed {
				return &pdfgo.UnsupportedError{Feature: fmt.Sprintf("movie activation field %q", key)}
			}
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
	if strict {
		return &pdfgo.UnsupportedError{Feature: "interactive " + string(annotation.Subtype) + " conversion"}
	}
	var actions []Action
	if annotation.Subtype == "3D" {
		model, err := p.reader.ReadThreeD(annotation)
		if err != nil {
			p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF 3D metadata unavailable; static appearance retained: " + err.Error()})
			return p.appearanceAnnotation(ctx, page, annotation)
		}
		data, err := model.Stream.Decode()
		if err != nil {
			return err
		}
		id, err := p.editor.AddAttachment(fmt.Sprintf("Model_%d.%s", p.page+1, strings.ToLower(string(model.Format))), data)
		if err != nil {
			return err
		}
		actions = append(actions, Action{Event: "CLICK", GotoA: &GotoA{AttachID: id}})
	} else {
		media, err := p.reader.ReadRichMedia(ctx, annotation)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF rich media metadata unavailable; static appearance retained: " + err.Error()})
			return p.appearanceAnnotation(ctx, page, annotation)
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
