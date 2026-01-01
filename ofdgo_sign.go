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
	"encoding/asn1"
	"encoding/xml"
	"path"
	"strings"
)

const (
	// SignTypeSeal 签章类型: seal
	SignTypeSeal = "seal"
	// SignTypeSign 签章类型: sign
	SignTypeSign = "sign"
)

// OFDSignatures 签名列表列表
type OFDSignatures struct {
	XMLName   xml.Name       `xml:"Signatures"`
	MaxSignId string         `xml:"MaxSignId"`
	List      []OFDSignature `xml:"Signature"`
}

// OFDSignature 签名列表引用
type OFDSignature struct {
	ID      string `xml:"ID,attr"`
	BaseLoc string `xml:"BaseLoc,attr"`
}

// SignatureFile 签名文件内容描述
type SignatureFile struct {
	XMLName     xml.Name `xml:"Signature"`
	SignedValue string   `xml:"SignedValue"`
	SignedInfo  struct {
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
	f, err := r.openFile(doc.Signatures)
	if err != nil {
		return err
	}
	defer f.Close()
	var signatures OFDSignatures
	if err := xml.NewDecoder(f).Decode(&signatures); err != nil {
		return err
	}
	for _, sigRef := range signatures.List {
		sigPath := path.Join(path.Dir(doc.Signatures), sigRef.BaseLoc)
		sf, err := r.openFile(sigPath)
		if err != nil {
			continue
		}
		var sigFile SignatureFile
		if err := xml.NewDecoder(sf).Decode(&sigFile); err != nil {
			sf.Close()
			continue
		}
		sf.Close()
		signedValuePath := path.Join(path.Dir(sigPath), sigFile.SignedValue)
		svData, err := r.ResData(signedValuePath)
		if err != nil {
			continue
		}
		sealType, sealData := extractSeal(svData)
		for _, annot := range sigFile.SignedInfo.StampAnnot {
			pageID := annot.PageRef
			bbox, _ := ParseBox(annot.Boundary)
			r.addStamp(pageID, bbox, sealType, sealData)
		}
	}
	return nil
}

// extractSeal 尝试提取印章数据
// 入参: data 签名值数据
// 返回: string 印章类型, []byte 印章数据
func extractSeal(data []byte) (string, []byte) {
	var raw asn1.RawValue
	_, err := asn1.Unmarshal(data, &raw)
	if err != nil {
		return "", nil
	}
	var foundType string
	var foundData []byte
	var search func(node asn1.RawValue) bool
	search = func(node asn1.RawValue) bool {
		if node.IsCompound {
			var elements []asn1.RawValue
			rest, err := asn1.Unmarshal(node.Bytes, &elements)
			if err == nil && len(rest) == 0 {
				if len(elements) == 4 {
					e0, e1, e2, e3 := elements[0], elements[1], elements[2], elements[3]
					if e1.Tag == 4 && e2.Tag == 2 && e3.Tag == 2 {
						foundData = e1.Bytes
						var s string
						if _, err := asn1.Unmarshal(e0.FullBytes, &s); err == nil {
							foundType = s
						} else {
							foundType = string(e0.Bytes)
						}
						foundType = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(foundType, "\x00", "")))
						if foundType == "es" {
							foundType = "png"
						}
						return true
					}
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
