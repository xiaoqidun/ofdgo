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
	"encoding/asn1"
	"encoding/binary"
	"encoding/xml"
	"image"
	"strings"
)

type SignType string

const (
	SignTypeSeal = "Seal"
	SignTypeSign = "Sign"
)

// Signatures 签名列表
type Signatures struct {
	XMLName   xml.Name    `xml:"Signatures"`
	MaxSignId string      `xml:"MaxSignId"`
	List      []Signature `xml:"Signature"`
}

// Signature 签名列表引用
type Signature struct {
	ID      string   `xml:"ID,attr"`
	BaseLoc string   `xml:"BaseLoc,attr"`
	Type    SignType `xml:"Type,attr"`
}

// SignatureFile 签名文件内容描述
type SignatureFile struct {
	XMLName     xml.Name `xml:"Signature"`
	SignedValue string   `xml:"SignedValue"`
	SignedInfo  struct {
		Seal struct {
			BaseLoc string `xml:"BaseLoc"`
		} `xml:"Seal"`
		StampAnnot []struct {
			ID       string `xml:"ID,attr"`
			PageRef  string `xml:"PageRef,attr"`
			Boundary string `xml:"Boundary,attr"`
		} `xml:"StampAnnot"`
	} `xml:"SignedInfo"`
}

// parseSignatures 解析签名文件
// 入参: doc 文档结构
// 返回: error 错误信息
func (r *Reader) parseSignatures(doc *Document) error {
	if doc.Signatures == "" {
		return nil
	}
	sigListPath := r.ResPath(doc.Signatures)
	f, err := r.openFile(sigListPath)
	if err != nil {
		return err
	}
	defer f.Close()
	var signatures Signatures
	if err := xml.NewDecoder(f).Decode(&signatures); err != nil {
		return err
	}
	for _, sigRef := range signatures.List {
		func(sigRef Signature) {
			sigPath := resolveResourcePath(sigListPath, "", sigRef.BaseLoc)
			sf, err := r.openFile(sigPath)
			if err != nil {
				return
			}
			defer sf.Close()
			var sigFile SignatureFile
			if err := xml.NewDecoder(sf).Decode(&sigFile); err != nil {
				return
			}
			var sealType string
			var sealData []byte
			if sigFile.SignedInfo.Seal.BaseLoc != "" {
				sealPath := resolveResourcePath(sigPath, "", sigFile.SignedInfo.Seal.BaseLoc)
				if data, err := r.ResData(sealPath); err == nil {
					sealType, sealData = extractSeal(data)
				}
			}
			if len(sealData) == 0 && sigFile.SignedValue != "" {
				signedValuePath := resolveResourcePath(sigPath, "", sigFile.SignedValue)
				if data, err := r.ResData(signedValuePath); err == nil {
					sealType, sealData = extractSeal(data)
				}
			}
			if len(sealData) == 0 {
				return
			}
			for _, annot := range sigFile.SignedInfo.StampAnnot {
				pageID := annot.PageRef
				bbox, _ := ParseBox(annot.Boundary)
				r.addStamp(pageID, bbox, sealType, sealData)
			}
		}(sigRef)
	}
	return nil
}

// extractSeal 尝试提取印章数据
// 入参: data 签名值数据
// 返回: string 印章类型, []byte 印章数据
func extractSeal(data []byte) (string, []byte) {
	if sealType, sealData := probeSealMedia(data); len(sealData) > 0 {
		return sealType, sealData
	}
	var raw asn1.RawValue
	_, err := asn1.Unmarshal(data, &raw)
	if err != nil {
		return "", nil
	}
	var foundType string
	var foundData []byte
	var search func(node asn1.RawValue) bool
	search = func(node asn1.RawValue) bool {
		if node.Tag == asn1.TagOctetString {
			if mediaType, mediaData := probeSealMedia(node.Bytes); len(mediaData) > 0 {
				foundType, foundData = mediaType, mediaData
				return true
			}
		}
		if node.IsCompound {
			if elements, ok := asn1Children(node.Bytes); ok {
				if mediaType, mediaData, ok := sealMediaFromElements(elements); ok {
					foundType, foundData = mediaType, mediaData
					return true
				}
				for _, child := range elements {
					if search(child) {
						return true
					}
				}
			}
		}
		return false
	}
	if search(raw) {
		return foundType, foundData
	}
	return "", nil
}

// asn1Children 解析ASN.1复合节点子元素
// 入参: data 复合节点内容
// 返回: []asn1.RawValue 子元素列表, bool 是否成功
func asn1Children(data []byte) ([]asn1.RawValue, bool) {
	var elements []asn1.RawValue
	for len(data) > 0 {
		var child asn1.RawValue
		rest, err := asn1.Unmarshal(data, &child)
		if err != nil || len(rest) == len(data) {
			return nil, false
		}
		elements = append(elements, child)
		data = rest
	}
	return elements, true
}

// sealMediaFromElements 从印章ASN.1结构提取媒体数据
// 入参: elements ASN.1子元素
// 返回: string 媒体类型, []byte 媒体数据, bool 是否成功
func sealMediaFromElements(elements []asn1.RawValue) (string, []byte, bool) {
	if len(elements) < 2 || elements[1].Tag != asn1.TagOctetString {
		return "", nil, false
	}
	mediaType := normalizeSealType(sealString(elements[0]))
	if isSealMediaType(mediaType) {
		if mediaType == "ofd" {
			if data := trimOFDPackage(elements[1].Bytes); len(data) > 0 {
				return mediaType, data, true
			}
		}
		return mediaType, elements[1].Bytes, true
	}
	if probeType, probeData := probeSealMedia(elements[1].Bytes); len(probeData) > 0 {
		return probeType, probeData, true
	}
	return "", nil, false
}

// sealString 解析印章图片类型
// 入参: raw ASN.1原始值
// 返回: string 图片类型
func sealString(raw asn1.RawValue) string {
	var s string
	if _, err := asn1.Unmarshal(raw.FullBytes, &s); err != nil {
		s = string(raw.Bytes)
	}
	s = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(s, "\x00", "")))
	if s == "jpg" {
		s = "jpeg"
	}
	return s
}

// normalizeSealType 标准化印章媒体类型
// 入参: s 原始媒体类型
// 返回: string 标准媒体类型
func normalizeSealType(s string) string {
	switch s {
	case "jpg":
		return "jpeg"
	case "jb2", "gbig2":
		return "jbig2"
	default:
		return s
	}
}

// isSealMediaType 判断是否为可渲染印章媒体类型
// 入参: s 媒体类型
// 返回: bool 是否可渲染
func isSealMediaType(s string) bool {
	switch s {
	case "png", "jpeg", "jbig2", "ofd":
		return true
	default:
		return false
	}
}

// probeSealMedia 探测印章媒体数据
// 入参: data 原始数据
// 返回: string 媒体类型, []byte 媒体数据
func probeSealMedia(data []byte) (string, []byte) {
	if sealData := trimOFDPackage(data); len(sealData) > 0 {
		return "ofd", sealData
	}
	return probeImageMedia(data)
}

// trimOFDPackage 裁剪OFD包数据
// 入参: data 原始数据
// 返回: []byte OFD包数据
func trimOFDPackage(data []byte) []byte {
	if idx := bytes.Index(data, []byte("PK\x03\x04")); idx >= 0 {
		if end := zipDataEnd(data[idx:]); end > 0 {
			return data[idx : idx+end]
		}
	}
	return nil
}

// zipDataEnd 获取ZIP数据结束位置
// 入参: data ZIP数据
// 返回: int 结束位置
func zipDataEnd(data []byte) int {
	if len(data) < 4 || !bytes.Equal(data[:4], []byte("PK\x03\x04")) {
		return 0
	}
	sig := []byte("PK\x05\x06")
	for i := len(data) - 22; i >= 0; i-- {
		if i+22 > len(data) || !bytes.Equal(data[i:i+4], sig) {
			continue
		}
		commentLen := int(binary.LittleEndian.Uint16(data[i+20 : i+22]))
		end := i + 22 + commentLen
		if end <= len(data) {
			return end
		}
	}
	return 0
}

// probeImageMedia 探测图片媒体数据
// 入参: data 原始数据
// 返回: string 媒体类型, []byte 图片数据
func probeImageMedia(data []byte) (string, []byte) {
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", nil
	}
	return normalizeSealType(format), data
}

// Stamp 印章信息结构
type Stamp struct {
	Box  Box
	Type string
	Data []byte
}

// addStamp 添加印章到页面
// 入参: pageID 页面ID, box 印章区域, sType 印章类型, data 印章数据
func (r *Reader) addStamp(pageID string, box Box, sType string, data []byte) {
	if r.Stamps == nil {
		r.Stamps = make(map[string][]Stamp)
	}
	r.Stamps[pageID] = append(r.Stamps[pageID], Stamp{
		Box:  box,
		Type: sType,
		Data: data,
	})
}
