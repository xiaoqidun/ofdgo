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

import "encoding/xml"

// textObjectTokens 记录字形变换所属TextCode，供解码后转换为对象内索引
type textObjectTokens struct {
	decoder     *xml.Decoder
	start       *xml.StartElement
	depth, code int
	transforms  []int
}

// Token 转发XML令牌并记录直接子节点顺序
// 返回: xml.Token XML令牌, error 解码错误
func (s *textObjectTokens) Token() (xml.Token, error) {
	if s.start != nil {
		start := *s.start
		s.start = nil
		return start, nil
	}
	token, err := s.decoder.Token()
	switch value := token.(type) {
	case xml.StartElement:
		if s.depth == 0 && value.Name.Local == "CGTransform" {
			s.transforms = append(s.transforms, s.code)
		}
		s.depth++
	case xml.EndElement:
		s.depth--
		if s.depth == 0 && value.Name.Local == "TextCode" {
			s.code++
		}
	}
	return token, err
}

// UnmarshalXML 将各TextCode前的局部字形位置统一为对象内索引
// 入参: decoder XML解码器, start 对象起始节点
// 返回: error 解码错误
func (obj *TextObject) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	type plain TextObject
	stream := &textObjectTokens{decoder: decoder, start: &start}
	var value plain
	if err := xml.NewTokenDecoder(stream).Decode(&value); err != nil {
		return err
	}
	offsets := make([]int, len(value.TextCode))
	for i := 1; i < len(offsets); i++ {
		offsets[i] = offsets[i-1] + len(textCodeRunes(value.TextCode[i-1].Value))
	}
	for i, code := range stream.transforms {
		if code < len(offsets) {
			value.CGTransform[i].CodePosition += offsets[code]
		}
	}
	*obj = TextObject(value)
	return nil
}
