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
)

const (
	signContentData       = "1.2.156.10197.6.1.4.2.1"
	signContentSignedData = "1.2.156.10197.6.1.4.2.2"
	signAttrMessageDigest = "1.2.840.113549.1.9.4"
)

// digitalVerifyResult 数字签名验证结果
type digitalVerifyResult struct {
	DataHashOK  bool
	SignedOK    bool
	CertOK      bool
	SignerCerts [][]byte
	Certs       [][]byte
	CertInfo    SignatureCertInfo
}

// gbtSignedData GB/T 35275 SignedData结构
type gbtSignedData struct {
	ContentDigest []byte
	Certs         []gbtCertificate
	Signers       []gbtSignerInfo
}

// gbtCertificate SignedData证书索引信息
type gbtCertificate struct {
	Raw    []byte
	Issuer []byte
	Serial *big.Int
}

// gbtSignerInfo SignedData签名者信息
type gbtSignerInfo struct {
	Issuer       []byte
	Serial       *big.Int
	DigestAlg    string
	SignatureAlg string
	Signature    []byte
	AuthAttrs    []byte
	AttrDigest   []byte
}

// verifyDigitalSignature 验证OFD数字签名
// 入参: method 签名算法, digestMethod 摘要算法, signedValue 签名值, signedData 被签名原文, options 验证选项
// 返回: *digitalVerifyResult 验证结果, error 错误信息
func verifyDigitalSignature(method, digestMethod string, signedValue, signedData []byte, options *signatureVerifyOptions) (*digitalVerifyResult, error) {
	if isGBT35275SignedValue(signedValue) {
		return verifyGBT35275SignedData(signedValue, signedData, options)
	}
	if isSM2SignatureMethod(method) {
		return verifyRawDigitalSignature(signedValue, signedData, options)
	}
	if isRSASignatureMethod(method) || isECDSASignatureMethod(method) {
		return verifyRawPublicKeySignature(method, digestMethod, signedValue, signedData, options)
	}
	return nil, fmt.Errorf("unsupported signature method: %s", method)
}

// verifyRawDigitalSignature 验证裸SM2数字签名
// 入参: signedValue 签名值, signedData 被签名原文, options 验证选项
// 返回: *digitalVerifyResult 验证结果, error 错误信息
func verifyRawDigitalSignature(signedValue, signedData []byte, options *signatureVerifyOptions) (*digitalVerifyResult, error) {
	if len(options.SignCerts) == 0 {
		return nil, fmt.Errorf("signature certificate not found")
	}
	result := &digitalVerifyResult{DataHashOK: true}
	for _, cert := range options.SignCerts {
		pub, err := parseSM2PublicKeyFromCert(cert)
		if err != nil {
			continue
		}
		result.CertOK = true
		result.SignerCerts = [][]byte{cert}
		result.CertInfo = signatureCertInfo(cert)
		if sm2VerifySignature(pub, nil, signedData, signedValue) {
			result.SignedOK = true
			return result, nil
		}
	}
	return result, nil
}

// verifyRawPublicKeySignature 验证裸RSA或ECDSA数字签名
// 入参: method 签名算法, digestMethod 摘要算法, signedValue 签名值, signedData 被签名原文, options 验证选项
// 返回: *digitalVerifyResult 验证结果, error 错误信息
func verifyRawPublicKeySignature(method, digestMethod string, signedValue, signedData []byte, options *signatureVerifyOptions) (*digitalVerifyResult, error) {
	if len(options.SignCerts) == 0 {
		return nil, fmt.Errorf("signature certificate not found")
	}
	if _, err := signatureMethodHash(method, digestMethod); err != nil {
		return nil, err
	}
	result := &digitalVerifyResult{DataHashOK: true}
	for _, cert := range options.SignCerts {
		ok, err := verifyPublicKeySignature(method, digestMethod, cert, signedData, signedValue)
		if err != nil {
			continue
		}
		result.CertOK = true
		result.SignerCerts = [][]byte{cert}
		result.CertInfo = signatureCertInfo(cert)
		if ok {
			result.SignedOK = true
			return result, nil
		}
	}
	return result, nil
}

// verifyGBT35275SignedData 验证GB/T 35275 SignedData签名值
// 入参: signedValue 签名值, signedData 被签名原文, options 验证选项
// 返回: *digitalVerifyResult 验证结果, error 错误信息
func verifyGBT35275SignedData(signedValue, signedData []byte, options *signatureVerifyOptions) (*digitalVerifyResult, error) {
	sd, err := parseGBT35275SignedData(signedValue)
	if err != nil {
		return nil, err
	}
	for _, cert := range options.SignCerts {
		c, err := parseGBTCertificate(cert)
		if err == nil {
			sd.Certs = append(sd.Certs, c)
		}
	}
	if len(sd.Signers) == 0 {
		return nil, fmt.Errorf("invalid signed data signer info")
	}
	result := &digitalVerifyResult{Certs: sd.rawCerts()}
	for _, signer := range sd.Signers {
		digest, err := signatureDigest(signer.DigestAlg, signedData)
		if err != nil {
			return nil, err
		}
		if len(sd.ContentDigest) != 0 && !bytes.Equal(sd.ContentDigest, digest) {
			return result, nil
		}
		result.DataHashOK = true
		plain := sd.ContentDigest
		if len(signer.AuthAttrs) != 0 {
			if !bytes.Equal(signer.AttrDigest, digest) {
				result.DataHashOK = false
				return result, nil
			}
			plain = signer.AuthAttrs
		}
		if len(plain) == 0 {
			return nil, fmt.Errorf("invalid signed data content")
		}
		cert := sd.findCert(signer.Issuer, signer.Serial)
		if cert == nil {
			result.CertOK = false
			return result, nil
		}
		result.SignerCerts = append(result.SignerCerts, cert.Raw)
		result.CertInfo = signatureCertInfo(cert.Raw)
		if isSM2SignatureMethod(signer.SignatureAlg) {
			pub, err := parseSM2PublicKeyFromCert(cert.Raw)
			if err != nil {
				result.CertOK = false
				return result, err
			}
			if !sm2VerifySignature(pub, nil, plain, signer.Signature) {
				result.CertOK = true
				return result, nil
			}
			continue
		}
		if !isRSASignatureMethod(signer.SignatureAlg) && !isECDSASignatureMethod(signer.SignatureAlg) {
			return nil, fmt.Errorf("unsupported signature method: %s", signer.SignatureAlg)
		}
		ok, err := verifyPublicKeySignature(signer.SignatureAlg, signer.DigestAlg, cert.Raw, plain, signer.Signature)
		if err != nil {
			result.CertOK = false
			return result, err
		}
		if !ok {
			result.CertOK = true
			return result, nil
		}
	}
	result.CertOK = true
	result.SignedOK = true
	return result, nil
}

// isGBT35275SignedValue 判断签名值是否为GB/T 35275 SignedData
// 入参: data 签名值数据
// 返回: bool 是否为SignedData
func isGBT35275SignedValue(data []byte) bool {
	var root asn1.RawValue
	rest, err := asn1.Unmarshal(data, &root)
	if err != nil || len(rest) != 0 || root.Tag != signASN1Sequence {
		return false
	}
	items, ok := asn1Children(root.Bytes)
	if !ok || len(items) < 2 {
		return false
	}
	oid, err := asn1OIDString(items[0])
	return err == nil && oid == signContentSignedData
}

// parseGBT35275SignedData 解析GB/T 35275 SignedData
// 入参: data 签名值数据
// 返回: *gbtSignedData SignedData结构, error 错误信息
func parseGBT35275SignedData(data []byte) (*gbtSignedData, error) {
	contentType, content, ok, err := parseGBTContentInfoBytes(data)
	if err != nil {
		return nil, err
	}
	if !ok || contentType != signContentSignedData {
		return nil, fmt.Errorf("invalid signed data content type")
	}
	items, ok := asn1Children(content.Bytes)
	if !ok || len(items) < 4 {
		return nil, fmt.Errorf("invalid signed data")
	}
	sd := &gbtSignedData{}
	contentOID, content, hasContent, err := parseGBTContentInfo(items[2])
	if err != nil {
		return nil, err
	}
	if hasContent {
		if contentOID != signContentData {
			return nil, fmt.Errorf("invalid signed data inner content type")
		}
		if content.Tag == asn1.TagOctetString {
			sd.ContentDigest, err = asn1OctetString(content)
			if err != nil {
				return nil, err
			}
		}
	}
	for i := 3; i < len(items); i++ {
		item := items[i]
		if item.Class == asn1.ClassContextSpecific && item.Tag == 0 {
			certs, err := parseGBTCertificates(item)
			if err != nil {
				return nil, err
			}
			sd.Certs = certs
			continue
		}
		if item.Class == asn1.ClassContextSpecific && item.Tag == 1 {
			continue
		}
		signers, err := parseGBTSignerInfos(item)
		if err != nil {
			return nil, err
		}
		sd.Signers = signers
	}
	return sd, nil
}

// parseGBTContentInfoBytes 解析ContentInfo字节
// 入参: data DER编码数据
// 返回: string 内容类型, asn1.RawValue 内容, bool 是否存在内容, error 错误信息
func parseGBTContentInfoBytes(data []byte) (string, asn1.RawValue, bool, error) {
	var raw asn1.RawValue
	rest, err := asn1.Unmarshal(data, &raw)
	if err != nil || len(rest) != 0 {
		return "", asn1.RawValue{}, false, fmt.Errorf("invalid content info")
	}
	return parseGBTContentInfo(raw)
}

// parseGBTContentInfo 解析ContentInfo结构
// 入参: raw ASN.1原始值
// 返回: string 内容类型, asn1.RawValue 内容, bool 是否存在内容, error 错误信息
func parseGBTContentInfo(raw asn1.RawValue) (string, asn1.RawValue, bool, error) {
	items, ok := asn1Children(raw.Bytes)
	if !ok || len(items) == 0 {
		return "", asn1.RawValue{}, false, fmt.Errorf("invalid content info")
	}
	oid, err := asn1OIDString(items[0])
	if err != nil {
		return "", asn1.RawValue{}, false, err
	}
	if len(items) == 1 {
		return oid, asn1.RawValue{}, false, nil
	}
	content, err := asn1Explicit(items[1])
	if err != nil {
		return "", asn1.RawValue{}, false, err
	}
	return oid, content, true, nil
}

// parseGBTCertificates 解析SignedData证书集合
// 入参: raw ASN.1原始值
// 返回: []gbtCertificate 证书列表, error 错误信息
func parseGBTCertificates(raw asn1.RawValue) ([]gbtCertificate, error) {
	items, ok := asn1Children(raw.Bytes)
	if !ok {
		return nil, fmt.Errorf("invalid signed data certificates")
	}
	certs := make([]gbtCertificate, 0, len(items))
	for _, item := range items {
		cert, err := parseGBTCertificate(item.FullBytes)
		if err != nil {
			return nil, err
		}
		certs = append(certs, cert)
	}
	return certs, nil
}

// parseGBTCertificate 解析X.509证书索引字段
// 入参: data DER编码证书
// 返回: gbtCertificate 证书索引信息, error 错误信息
func parseGBTCertificate(data []byte) (gbtCertificate, error) {
	cert, err := parseSignatureCertificate(data)
	if err != nil {
		return gbtCertificate{}, err
	}
	return gbtCertificate{
		Raw:    cert.Raw,
		Issuer: cert.Issuer,
		Serial: cert.Serial,
	}, nil
}

// parseGBTSignerInfos 解析签名者信息集合
// 入参: raw ASN.1原始值
// 返回: []gbtSignerInfo 签名者列表, error 错误信息
func parseGBTSignerInfos(raw asn1.RawValue) ([]gbtSignerInfo, error) {
	items, ok := asn1Children(raw.Bytes)
	if !ok {
		return nil, fmt.Errorf("invalid signer infos")
	}
	signers := make([]gbtSignerInfo, 0, len(items))
	for _, item := range items {
		signer, err := parseGBTSignerInfo(item)
		if err != nil {
			return nil, err
		}
		signers = append(signers, signer)
	}
	return signers, nil
}

// parseGBTSignerInfo 解析签名者信息
// 入参: raw ASN.1原始值
// 返回: gbtSignerInfo 签名者信息, error 错误信息
func parseGBTSignerInfo(raw asn1.RawValue) (gbtSignerInfo, error) {
	items, ok := asn1Children(raw.Bytes)
	if !ok || len(items) < 5 {
		return gbtSignerInfo{}, fmt.Errorf("invalid signer info")
	}
	issuer, serial, err := parseGBTIssuerAndSerial(items[1])
	if err != nil {
		return gbtSignerInfo{}, err
	}
	digestAlg, err := parseGBTAlgorithm(items[2])
	if err != nil {
		return gbtSignerInfo{}, err
	}
	idx := 3
	var authAttrs []byte
	var attrDigest []byte
	if items[idx].Class == asn1.ClassContextSpecific && items[idx].Tag == 0 {
		authAttrs = asn1SetBytes(items[idx].Bytes)
		attrDigest, err = parseGBTMessageDigestAttr(items[idx])
		if err != nil {
			return gbtSignerInfo{}, err
		}
		idx++
	}
	if len(items) <= idx+1 {
		return gbtSignerInfo{}, fmt.Errorf("invalid signer info")
	}
	signatureAlg, err := parseGBTAlgorithm(items[idx])
	if err != nil {
		return gbtSignerInfo{}, err
	}
	signature, err := asn1OctetString(items[idx+1])
	if err != nil {
		return gbtSignerInfo{}, err
	}
	return gbtSignerInfo{
		Issuer:       issuer,
		Serial:       serial,
		DigestAlg:    digestAlg,
		SignatureAlg: signatureAlg,
		Signature:    signature,
		AuthAttrs:    authAttrs,
		AttrDigest:   attrDigest,
	}, nil
}

// parseGBTIssuerAndSerial 解析证书颁发者和序列号
// 入参: raw ASN.1原始值
// 返回: []byte 颁发者DN, *big.Int 序列号, error 错误信息
func parseGBTIssuerAndSerial(raw asn1.RawValue) ([]byte, *big.Int, error) {
	items, ok := asn1Children(raw.Bytes)
	if !ok || len(items) < 2 {
		return nil, nil, fmt.Errorf("invalid issuer and serial")
	}
	serial, err := asn1IntegerBig(items[1])
	if err != nil {
		return nil, nil, err
	}
	return append([]byte(nil), items[0].FullBytes...), serial, nil
}

// parseGBTAlgorithm 解析算法标识
// 入参: raw ASN.1原始值
// 返回: string 算法OID, error 错误信息
func parseGBTAlgorithm(raw asn1.RawValue) (string, error) {
	if raw.Tag == asn1.TagOID {
		return asn1OIDString(raw)
	}
	items, ok := asn1Children(raw.Bytes)
	if !ok || len(items) == 0 {
		return "", fmt.Errorf("invalid algorithm identifier")
	}
	return asn1OIDString(items[0])
}

// parseGBTMessageDigestAttr 解析认证属性中的message-digest
// 入参: raw ASN.1原始值
// 返回: []byte 摘要值, error 错误信息
func parseGBTMessageDigestAttr(raw asn1.RawValue) ([]byte, error) {
	attrs, ok := asn1Children(raw.Bytes)
	if !ok {
		return nil, fmt.Errorf("invalid authenticated attributes")
	}
	for _, attr := range attrs {
		items, ok := asn1Children(attr.Bytes)
		if !ok || len(items) < 2 {
			continue
		}
		oid, err := asn1OIDString(items[0])
		if err != nil || oid != signAttrMessageDigest {
			continue
		}
		values, ok := asn1Children(items[1].Bytes)
		if !ok || len(values) == 0 {
			return nil, fmt.Errorf("invalid message digest attribute")
		}
		return asn1OctetString(values[0])
	}
	return nil, fmt.Errorf("message digest attribute not found")
}

// findCert 查找签名者证书
// 入参: issuer 颁发者DN, serial 证书序列号
// 返回: *gbtCertificate 证书信息
func (sd *gbtSignedData) findCert(issuer []byte, serial *big.Int) *gbtCertificate {
	for i := range sd.Certs {
		cert := &sd.Certs[i]
		if bytes.Equal(cert.Issuer, issuer) && cert.Serial.Cmp(serial) == 0 {
			return cert
		}
	}
	return nil
}

// rawCerts 获取SignedData证书原文
// 返回: [][]byte 证书列表
func (sd *gbtSignedData) rawCerts() [][]byte {
	certs := make([][]byte, 0, len(sd.Certs))
	for _, cert := range sd.Certs {
		certs = append(certs, cert.Raw)
	}
	return certs
}

// asn1Explicit 解析显式标签内容
// 入参: raw ASN.1原始值
// 返回: asn1.RawValue 标签内容, error 错误信息
func asn1Explicit(raw asn1.RawValue) (asn1.RawValue, error) {
	if raw.Class != asn1.ClassContextSpecific || raw.Tag != 0 || !raw.IsCompound {
		return asn1.RawValue{}, fmt.Errorf("invalid explicit content")
	}
	var out asn1.RawValue
	rest, err := asn1.Unmarshal(raw.Bytes, &out)
	if err != nil || len(rest) != 0 {
		return asn1.RawValue{}, fmt.Errorf("invalid explicit content")
	}
	return out, nil
}

// asn1IntegerBig 解析ASN.1整数
// 入参: raw ASN.1原始值
// 返回: *big.Int 整数值, error 错误信息
func asn1IntegerBig(raw asn1.RawValue) (*big.Int, error) {
	var out *big.Int
	rest, err := asn1.Unmarshal(raw.FullBytes, &out)
	if err != nil || len(rest) != 0 || out == nil {
		return nil, fmt.Errorf("invalid integer")
	}
	return out, nil
}
