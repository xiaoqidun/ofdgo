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
	"encoding/xml"
	"fmt"
)

// MarshalXML 按标准属性写入目标，书签和动作采用相同格式
// 入参: encoder XML编码器, start 起始节点
// 返回: error 参数或编码错误
func (dest Dest) MarshalXML(encoder *xml.Encoder, start xml.StartElement) error {
	attrs, err := dest.attributes()
	if err != nil {
		return err
	}
	start.Attr = attrs
	if err := encoder.EncodeToken(start); err != nil {
		return err
	}
	return encoder.EncodeToken(start.End())
}

// effective 应用省略属性的默认值，不改变原始声明
// 返回: Dest 生效的目标参数
func (dest Dest) effective() Dest {
	if dest.OmitLeft {
		dest.Left = 0
	}
	if dest.OmitTop {
		dest.Top = 0
	}
	if dest.OmitZoom {
		dest.Zoom = 0
	}
	return dest
}

// attributes 校验并生成目标属性，缩放为0表示沿用当前比例
// 返回: ofdAttrs 标准属性, error 模式或参数错误
func (dest Dest) attributes() (ofdAttrs, error) {
	dest = dest.effective()
	attrs := ofdAttrs{{Name: xml.Name{Local: "Type"}, Value: dest.Type}, {Name: xml.Name{Local: "PageID"}, Value: dest.PageID}}
	if dest.PageID == "" {
		return nil, fmt.Errorf("missing destination page")
	}
	add := func(name string, value float64, omit bool) error {
		if !finite(value) {
			return fmt.Errorf("invalid destination %s", name)
		}
		if !omit {
			attrs.add(name, ofdNumber(value))
		}
		return nil
	}
	switch dest.Type {
	case "XYZ":
		if dest.Zoom != 0 && (dest.Zoom < 0.1 || dest.Zoom > 64) {
			return nil, fmt.Errorf("destination zoom must be zero or between 0.1 and 64")
		}
		if err := add("Zoom", dest.Zoom, dest.OmitZoom); err != nil {
			return nil, err
		}
		fallthrough
	case "FitR":
		if err := add("Left", dest.Left, dest.OmitLeft); err != nil {
			return nil, err
		}
		if err := add("Top", dest.Top, dest.OmitTop); err != nil {
			return nil, err
		}
		if dest.Type == "FitR" {
			if dest.Right <= dest.Left || dest.Bottom <= dest.Top {
				return nil, fmt.Errorf("empty destination rectangle")
			}
			if err := add("Right", dest.Right, false); err != nil {
				return nil, err
			}
			if err := add("Bottom", dest.Bottom, false); err != nil {
				return nil, err
			}
		}
	case "FitH":
		if err := add("Top", dest.Top, dest.OmitTop); err != nil {
			return nil, err
		}
	case "FitV":
		if err := add("Left", dest.Left, dest.OmitLeft); err != nil {
			return nil, err
		}
	case "Fit":
	default:
		return nil, fmt.Errorf("invalid destination type %q", dest.Type)
	}
	return attrs, nil
}
