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
	"fmt"
	"math/big"
	"strings"
)

const (
	signDigestSM3      = "1.2.156.10197.1.401"
	signDigestSM3NoKey = "1.2.156.10197.1.401.1"
	signDigestSM3Key   = "1.2.156.10197.1.401.2"
	signMethodSM2SM3   = "1.2.156.10197.1.501"
	signMethodSM2SM3B  = "1.2.156.10197.501"
	signMethodSM2Sign  = "1.2.156.10197.1.301.1"
	signCurveSM2P256   = "1.2.156.10197.1.301"
	signECPublicKey    = "1.2.840.10045.2.1"
	signASN1Sequence   = 16
	signPublicKeySize  = 65
)

// sesSignature SES签章值
type sesSignature struct {
	ToSign    []byte
	Cert      []byte
	SignAlg   string
	Signature []byte
	DataHash  []byte
	Seal      *sesSeal
}

// sesSeal SES电子印章
type sesSeal struct {
	Raw       []byte
	SignData  []byte
	Cert      []byte
	SignAlg   string
	Signature []byte
	PicType   string
	PicData   []byte
	CertList  sesCertList
}

// sesCertList SES印章证书列表
type sesCertList struct {
	Certs   [][]byte
	Digests []sesCertDigest
}

// sesCertDigest SES印章证书摘要
type sesCertDigest struct {
	Method string
	Value  []byte
}

// sesVerifyResult SES签章验证结果
type sesVerifyResult struct {
	DataHashOK  bool
	SignedOK    bool
	SealOK      bool
	CertOK      bool
	SignCert    SignatureCertInfo
	SealCert    SignatureCertInfo
	SignCertRaw []byte
	SealCertRaw []byte
	SealRaw     []byte
	Certs       [][]byte
	SealType    string
}

// parseSESSignature 解析SES签章值
// 入参: data 签章值数据
// 返回: *sesSignature SES签章值, error 错误信息
func parseSESSignature(data []byte) (*sesSignature, error) {
	var root asn1.RawValue
	rest, err := asn1.Unmarshal(data, &root)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 || root.Tag != signASN1Sequence || !root.IsCompound {
		return nil, fmt.Errorf("invalid ses signature")
	}
	items, ok := asn1Children(root.Bytes)
	if !ok {
		return nil, fmt.Errorf("invalid ses signature items")
	}
	if len(items) == 2 {
		return parseSESSignatureV1(items)
	}
	if len(items) < 4 || len(items) > 5 {
		return nil, fmt.Errorf("invalid ses signature items")
	}
	cert, err := asn1OctetString(items[1])
	if err != nil {
		return nil, err
	}
	alg, err := asn1OIDString(items[2])
	if err != nil {
		return nil, err
	}
	signature, err := asn1BitStringBytes(items[3])
	if err != nil {
		return nil, err
	}
	tbsItems, ok := asn1Children(items[0].Bytes)
	if !ok || len(tbsItems) < 5 {
		return nil, fmt.Errorf("invalid ses toSign")
	}
	seal, err := parseSESSeal(tbsItems[1])
	if err != nil {
		return nil, err
	}
	dataHash, err := asn1BitOrOctetBytes(tbsItems[3])
	if err != nil {
		return nil, err
	}
	return &sesSignature{
		ToSign:    append([]byte(nil), items[0].FullBytes...),
		Cert:      cert,
		SignAlg:   alg,
		Signature: signature,
		DataHash:  dataHash,
		Seal:      seal,
	}, nil
}

// parseSESSignatureV1 解析SES V1签章值
// 入参: items ASN.1子元素
// 返回: *sesSignature SES签章值, error 错误信息
func parseSESSignatureV1(items []asn1.RawValue) (*sesSignature, error) {
	signature, err := asn1BitStringBytes(items[1])
	if err != nil {
		return nil, err
	}
	tbsItems, ok := asn1Children(items[0].Bytes)
	if !ok || len(tbsItems) != 7 {
		return nil, fmt.Errorf("invalid ses v1 toSign")
	}
	seal, err := parseSESSeal(tbsItems[1])
	if err != nil {
		return nil, err
	}
	dataHash, err := asn1BitOrOctetBytes(tbsItems[3])
	if err != nil {
		return nil, err
	}
	cert, err := asn1OctetString(tbsItems[5])
	if err != nil {
		return nil, err
	}
	alg, err := asn1OIDString(tbsItems[6])
	if err != nil {
		return nil, err
	}
	return &sesSignature{
		ToSign:    append([]byte(nil), items[0].FullBytes...),
		Cert:      cert,
		SignAlg:   alg,
		Signature: signature,
		DataHash:  dataHash,
		Seal:      seal,
	}, nil
}

// verifySESSignature 验证SES签章值
// 入参: data 签章值数据, signedData 被签名数据原文, options 验证选项
// 返回: *sesVerifyResult 验证结果, error 错误信息
func verifySESSignature(data, signedData []byte, options *signatureVerifyOptions) (*sesVerifyResult, error) {
	sig, err := parseSESSignature(data)
	if err != nil {
		return nil, err
	}
	if !isSM2SignatureMethod(sig.SignAlg) || !isSM2SignatureMethod(sig.Seal.SignAlg) {
		return nil, fmt.Errorf("unsupported signature method")
	}
	result := &sesVerifyResult{}
	result.SignCert = signatureCertInfo(sig.Cert)
	result.SealCert = signatureCertInfo(sig.Seal.Cert)
	result.SignCertRaw = sig.Cert
	result.SealCertRaw = sig.Seal.Cert
	result.SealRaw = sig.Seal.Raw
	result.Certs = append(result.Certs, sig.Seal.CertList.Certs...)
	result.Certs = append(result.Certs, options.SignCerts...)
	result.SealType = sig.Seal.PicType
	result.DataHashOK = bytes.Equal(sig.DataHash, signSM3(signedData))
	signPub, err := parseSM2PublicKeyFromCert(sig.Cert)
	if err != nil {
		return result, err
	}
	sealPub, err := parseSM2PublicKeyFromCert(sig.Seal.Cert)
	if err != nil {
		return result, err
	}
	result.SignedOK = sm2VerifySignature(signPub, nil, sig.ToSign, sig.Signature)
	result.SealOK = sm2VerifySignature(sealPub, nil, sig.Seal.SignData, sig.Seal.Signature)
	result.CertOK = sesCertInList(sig.Cert, sig.Seal.CertList)
	return result, nil
}

// parseSESSeal 解析SES电子印章
// 入参: raw ASN.1原始值
// 返回: *sesSeal SES电子印章, error 错误信息
func parseSESSeal(raw asn1.RawValue) (*sesSeal, error) {
	items, ok := asn1Children(raw.Bytes)
	if !ok {
		return nil, fmt.Errorf("invalid ses seal")
	}
	if len(items) == 2 {
		return parseSESSealV1(raw, items)
	}
	if len(items) < 4 || len(items) > 5 {
		return nil, fmt.Errorf("invalid ses seal")
	}
	cert, err := asn1OctetString(items[1])
	if err != nil {
		return nil, err
	}
	alg, err := asn1OIDString(items[2])
	if err != nil {
		return nil, err
	}
	signature, err := asn1BitStringBytes(items[3])
	if err != nil {
		return nil, err
	}
	infoItems, ok := asn1Children(items[0].Bytes)
	if !ok || len(infoItems) < 4 || len(infoItems) > 5 {
		return nil, fmt.Errorf("invalid ses seal info")
	}
	version, err := parseSESHeaderVersion(infoItems[0])
	if err != nil {
		return nil, err
	}
	certList, err := parseSESCertList(infoItems[2], version)
	if err != nil {
		return nil, err
	}
	picType, picData, err := parseSESPicture(infoItems[3])
	if err != nil {
		return nil, err
	}
	return &sesSeal{
		Raw:       append([]byte(nil), raw.FullBytes...),
		SignData:  append([]byte(nil), items[0].FullBytes...),
		Cert:      cert,
		SignAlg:   alg,
		Signature: signature,
		PicType:   picType,
		PicData:   picData,
		CertList:  certList,
	}, nil
}

// parseSESSealV1 解析SES V1电子印章
// 入参: raw ASN.1原始值, items ASN.1子元素
// 返回: *sesSeal SES电子印章, error 错误信息
func parseSESSealV1(raw asn1.RawValue, items []asn1.RawValue) (*sesSeal, error) {
	infoItems, ok := asn1Children(items[0].Bytes)
	if !ok || len(infoItems) < 4 || len(infoItems) > 5 {
		return nil, fmt.Errorf("invalid ses v1 seal info")
	}
	signItems, ok := asn1Children(items[1].Bytes)
	if !ok || len(signItems) != 3 {
		return nil, fmt.Errorf("invalid ses v1 sign info")
	}
	cert, err := asn1OctetString(signItems[0])
	if err != nil {
		return nil, err
	}
	alg, err := asn1OIDString(signItems[1])
	if err != nil {
		return nil, err
	}
	signature, err := asn1BitStringBytes(signItems[2])
	if err != nil {
		return nil, err
	}
	certList, err := parseSESCertListV1(infoItems[2])
	if err != nil {
		return nil, err
	}
	picType, picData, err := parseSESPicture(infoItems[3])
	if err != nil {
		return nil, err
	}
	signData := asn1SequenceBytes(items[0].FullBytes, signItems[0].FullBytes, signItems[1].FullBytes)
	return &sesSeal{
		Raw:       append([]byte(nil), raw.FullBytes...),
		SignData:  signData,
		Cert:      cert,
		SignAlg:   alg,
		Signature: signature,
		PicType:   picType,
		PicData:   picData,
		CertList:  certList,
	}, nil
}

// parseSESHeaderVersion 解析印章头版本
// 入参: raw 印章头信息
// 返回: int 版本号, error 错误信息
func parseSESHeaderVersion(raw asn1.RawValue) (int, error) {
	items, ok := asn1Children(raw.Bytes)
	if !ok || len(items) < 2 {
		return 0, fmt.Errorf("invalid ses header")
	}
	return asn1Integer(items[1])
}

// parseSESCertList 解析印章证书列表
// 入参: raw 印章属性信息, version 印章版本
// 返回: sesCertList 证书列表, error 错误信息
func parseSESCertList(raw asn1.RawValue, version int) (sesCertList, error) {
	items, ok := asn1Children(raw.Bytes)
	if !ok || len(items) < 3 {
		return sesCertList{}, fmt.Errorf("invalid ses property")
	}
	if version < 4 {
		return parseSESCertInfoList(items[2])
	}
	if len(items) < 4 {
		return sesCertList{}, fmt.Errorf("invalid ses cert list")
	}
	listType, err := asn1Integer(items[2])
	if err != nil {
		return sesCertList{}, err
	}
	switch listType {
	case 1:
		return parseSESCertInfoList(items[3])
	case 2:
		return parseSESCertDigestList(items[3])
	default:
		return sesCertList{}, fmt.Errorf("unsupported ses cert list type")
	}
}

// parseSESCertListV1 解析SES V1证书列表
// 入参: raw 证书列表ASN.1值
// 返回: sesCertList 证书列表, error 错误信息
func parseSESCertListV1(raw asn1.RawValue) (sesCertList, error) {
	certs, ok := asn1Children(raw.Bytes)
	if !ok || len(certs) == 0 {
		return sesCertList{}, fmt.Errorf("invalid ses cert list")
	}
	list := sesCertList{Certs: make([][]byte, 0, len(certs))}
	for _, item := range certs {
		list.Certs = append(list.Certs, append([]byte(nil), item.FullBytes...))
	}
	return list, nil
}

// parseSESCertInfoList 解析SES证书信息列表
// 入参: raw 证书信息列表ASN.1值
// 返回: sesCertList 证书列表, error 错误信息
func parseSESCertInfoList(raw asn1.RawValue) (sesCertList, error) {
	certs, ok := asn1Children(raw.Bytes)
	if !ok || len(certs) == 0 {
		return sesCertList{}, fmt.Errorf("invalid ses cert list")
	}
	list := sesCertList{Certs: make([][]byte, 0, len(certs))}
	for _, item := range certs {
		cert, err := asn1OctetString(item)
		if err != nil {
			return sesCertList{}, err
		}
		list.Certs = append(list.Certs, cert)
	}
	return list, nil
}

// parseSESCertDigestList 解析SES证书摘要列表
// 入参: raw 证书摘要列表ASN.1值
// 返回: sesCertList 证书列表, error 错误信息
func parseSESCertDigestList(raw asn1.RawValue) (sesCertList, error) {
	items, ok := asn1Children(raw.Bytes)
	if !ok || len(items) == 0 {
		return sesCertList{}, fmt.Errorf("invalid ses cert digest list")
	}
	list := sesCertList{Digests: make([]sesCertDigest, 0, len(items))}
	for _, item := range items {
		fields, ok := asn1Children(item.Bytes)
		if !ok || len(fields) < 2 {
			return sesCertList{}, fmt.Errorf("invalid ses cert digest")
		}
		method, err := parseGBTAlgorithm(fields[0])
		if err != nil {
			return sesCertList{}, err
		}
		digest, err := asn1OctetString(fields[1])
		if err != nil {
			return sesCertList{}, err
		}
		list.Digests = append(list.Digests, sesCertDigest{Method: method, Value: digest})
	}
	return list, nil
}

// parseSESPicture 解析印章图片
// 入参: raw 印章图片信息
// 返回: string 图片类型, []byte 图片数据, error 错误信息
func parseSESPicture(raw asn1.RawValue) (string, []byte, error) {
	items, ok := asn1Children(raw.Bytes)
	if !ok || len(items) < 4 {
		return "", nil, fmt.Errorf("invalid ses picture")
	}
	picType := normalizeSealType(strings.ToLower(strings.TrimSpace(asn1String(items[0]))))
	picData, err := asn1OctetString(items[1])
	if err != nil {
		return "", nil, err
	}
	if picType == "ofd" {
		if data := trimOFDPackage(picData); len(data) > 0 {
			picData = data
		}
	}
	return picType, picData, nil
}

// parseSM2PublicKeyFromCert 从证书解析SM2公钥
// 入参: data DER编码证书
// 返回: sm2PublicKey SM2公钥, error 错误信息
func parseSM2PublicKeyFromCert(data []byte) (sm2PublicKey, error) {
	cert, err := parseSignatureCertificate(data)
	if err != nil {
		return sm2PublicKey{}, err
	}
	return parseSM2PublicKeyInfo(cert.PublicKey)
}

// parseSM2PublicKeyInfo 解析SM2公钥信息
// 入参: raw SubjectPublicKeyInfo原始值
// 返回: sm2PublicKey SM2公钥, error 错误信息
func parseSM2PublicKeyInfo(raw asn1.RawValue) (sm2PublicKey, error) {
	var spki struct {
		Algorithm        asn1.RawValue
		SubjectPublicKey asn1.BitString
	}
	rest, err := asn1.Unmarshal(raw.FullBytes, &spki)
	if err != nil {
		return sm2PublicKey{}, err
	}
	if len(rest) != 0 {
		return sm2PublicKey{}, fmt.Errorf("invalid subject public key info")
	}
	algItems, ok := asn1Children(spki.Algorithm.Bytes)
	if !ok || len(algItems) < 2 {
		return sm2PublicKey{}, fmt.Errorf("invalid public key algorithm")
	}
	alg, err := asn1OIDString(algItems[0])
	if err != nil {
		return sm2PublicKey{}, err
	}
	curve, err := asn1OIDString(algItems[1])
	if err != nil {
		return sm2PublicKey{}, err
	}
	if alg != signECPublicKey || curve != signCurveSM2P256 {
		return sm2PublicKey{}, fmt.Errorf("unsupported public key algorithm")
	}
	key := spki.SubjectPublicKey.Bytes
	if len(key) != signPublicKeySize || key[0] != 4 {
		return sm2PublicKey{}, fmt.Errorf("invalid sm2 public key")
	}
	return sm2PublicKey{
		X: new(big.Int).SetBytes(key[1:33]),
		Y: new(big.Int).SetBytes(key[33:65]),
	}, nil
}

// signSM3 计算SM3摘要
// 入参: data 原文数据
// 返回: []byte 摘要值
func signSM3(data []byte) []byte {
	h := newSM3()
	h.Write(data)
	return h.Sum(nil)
}

// isSM2SignatureMethod 判断是否为SM2签名算法
// 入参: method 算法标识
// 返回: bool 是否为SM2签名算法
func isSM2SignatureMethod(method string) bool {
	switch signatureMethodText(method) {
	case signMethodSM2SM3, signMethodSM2SM3B, signMethodSM2Sign, "SM2", "SM2SM3", "SM3SM2", "SM2WITHSM3", "SM3WITHSM2":
		return true
	default:
		return false
	}
}

// isSM3DigestMethod 判断是否为SM3摘要算法
// 入参: method 算法标识
// 返回: bool 是否为SM3摘要算法
func isSM3DigestMethod(method string) bool {
	switch signatureMethodText(method) {
	case signDigestSM3, signDigestSM3NoKey, signDigestSM3Key, "SM3":
		return true
	default:
		return false
	}
}

// sesCertInList 判断证书是否在印章证书列表中
// 入参: cert DER编码证书, list 证书列表
// 返回: bool 是否存在
func sesCertInList(cert []byte, list sesCertList) bool {
	for _, item := range list.Certs {
		if bytes.Equal(cert, item) {
			return true
		}
	}
	for _, item := range list.Digests {
		digest, err := signatureDigest(item.Method, cert)
		if err != nil {
			continue
		}
		if bytes.Equal(digest, item.Value) {
			return true
		}
	}
	return false
}

// asn1OctetString 解析ASN.1八位字符串
// 入参: raw ASN.1原始值
// 返回: []byte 字节数据, error 错误信息
func asn1OctetString(raw asn1.RawValue) ([]byte, error) {
	var out []byte
	rest, err := asn1.Unmarshal(raw.FullBytes, &out)
	if err != nil || len(rest) != 0 {
		return nil, fmt.Errorf("invalid octet string")
	}
	return append([]byte(nil), out...), nil
}

// asn1BitStringBytes 解析ASN.1位字符串
// 入参: raw ASN.1原始值
// 返回: []byte 字节数据, error 错误信息
func asn1BitStringBytes(raw asn1.RawValue) ([]byte, error) {
	var bits asn1.BitString
	rest, err := asn1.Unmarshal(raw.FullBytes, &bits)
	if err != nil || len(rest) != 0 || bits.BitLength%8 != 0 {
		return nil, fmt.Errorf("invalid bit string")
	}
	return append([]byte(nil), bits.Bytes...), nil
}

// asn1BitOrOctetBytes 解析ASN.1位字符串或八位字符串
// 入参: raw ASN.1原始值
// 返回: []byte 字节数据, error 错误信息
func asn1BitOrOctetBytes(raw asn1.RawValue) ([]byte, error) {
	if raw.Tag == asn1.TagOctetString {
		return asn1OctetString(raw)
	}
	return asn1BitStringBytes(raw)
}

// asn1OIDString 解析ASN.1对象标识符
// 入参: raw ASN.1原始值
// 返回: string OID字符串, error 错误信息
func asn1OIDString(raw asn1.RawValue) (string, error) {
	var oid asn1.ObjectIdentifier
	rest, err := asn1.Unmarshal(raw.FullBytes, &oid)
	if err != nil || len(rest) != 0 {
		return "", fmt.Errorf("invalid oid")
	}
	return oid.String(), nil
}

// asn1Integer 解析ASN.1整数
// 入参: raw ASN.1原始值
// 返回: int 整数值, error 错误信息
func asn1Integer(raw asn1.RawValue) (int, error) {
	var out int
	rest, err := asn1.Unmarshal(raw.FullBytes, &out)
	if err != nil || len(rest) != 0 {
		return 0, fmt.Errorf("invalid integer")
	}
	return out, nil
}

// asn1String 解析ASN.1字符串
// 入参: raw ASN.1原始值
// 返回: string 字符串
func asn1String(raw asn1.RawValue) string {
	var s string
	if _, err := asn1.Unmarshal(raw.FullBytes, &s); err != nil {
		return strings.TrimSpace(string(raw.Bytes))
	}
	return s
}

// asn1SequenceBytes 编码ASN.1序列
// 入参: parts 序列元素DER数据
// 返回: []byte ASN.1序列DER数据
func asn1SequenceBytes(parts ...[]byte) []byte {
	var content []byte
	for _, part := range parts {
		content = append(content, part...)
	}
	return asn1Wrap(0x30, content)
}

// asn1SetBytes 编码ASN.1集合
// 入参: content 集合内容DER数据
// 返回: []byte ASN.1集合DER数据
func asn1SetBytes(content []byte) []byte {
	return asn1Wrap(0x31, content)
}

// asn1Wrap 包装ASN.1标签
// 入参: tag ASN.1标签, content 内容DER数据
// 返回: []byte ASN.1 DER数据
func asn1Wrap(tag byte, content []byte) []byte {
	out := []byte{tag}
	out = append(out, asn1LengthBytes(len(content))...)
	out = append(out, content...)
	return out
}

// asn1LengthBytes 编码ASN.1长度
// 入参: n 内容长度
// 返回: []byte ASN.1长度编码
func asn1LengthBytes(n int) []byte {
	if n < 128 {
		return []byte{byte(n)}
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte(n)
		n >>= 8
	}
	out := []byte{0x80 | byte(len(buf)-i)}
	return append(out, buf[i:]...)
}
