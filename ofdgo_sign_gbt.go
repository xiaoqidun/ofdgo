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
	"slices"
	"time"
)

// GB/T35275与CMS签名内容类型及属性对象标识
const (
	signContentData       = "1.2.156.10197.6.1.4.2.1"
	signContentSignedData = "1.2.156.10197.6.1.4.2.2"
	signContentCMSData    = "1.2.840.113549.1.7.1"
	signAttrMessageDigest = "1.2.840.113549.1.9.4"
	signAttrSigningTime   = "1.2.840.113549.1.9.5"
)

// digitalVerifyResult 数字签名验证结果
type digitalVerifyResult struct {
	SignatureMethod string
	Timestamps      []signatureTimestampEvidence
	SignatureTime   time.Time
	SignerTimes     []signerCertTime
	DataHashChecked bool
	DataHashOK      bool
	SignedChecked   bool
	SignedOK        bool
	CertChecked     bool
	CertOK          bool
	SignerCerts     [][]byte
	Certs           [][]byte
	CertInfo        SignatureCertInfo
}

// gbtSignedData GB/T35275 SignedData结构
type gbtSignedData struct {
	Version     int
	DigestAlgs  []string
	ContentType string
	HasContent  bool
	Content     []byte
	Certs       []gbtCertificate
	Signers     []gbtSignerInfo
}

// gbtCertificate SignedData证书索引信息
type gbtCertificate struct {
	Raw          []byte
	Issuer       []byte
	Serial       *big.Int
	SubjectKeyID []byte
}

// gbtSignerInfo SignedData签名者信息
type gbtSignerInfo struct {
	Version         int
	UnauthAttrs     asn1.RawValue
	Issuer          []byte
	Serial          *big.Int
	SubjectKeyID    []byte
	DigestAlg       string
	SignatureAlg    string
	SignatureAlgRaw asn1.RawValue
	Signature       []byte
	AuthAttrs       []byte
	AuthAttrsSorted []byte
	AttrDigest      []byte
	SigningTime     time.Time
}

// verifyDigitalSignature 验证OFD数字签名
// 入参: method 签名算法, digestMethod 摘要算法, signedValue 签名值, signedData 被签名原文, options 验证选项
// 返回: *digitalVerifyResult 验证结果, error 错误信息
func verifyDigitalSignature(method, digestMethod string, signedValue, signedData []byte, options *signatureVerifyOptions) (*digitalVerifyResult, error) {
	if value, ok := normalizeGBT35275SignedValue(signedValue); ok {
		sd, err := parseGBT35275Normalized(value)
		if err != nil {
			return nil, err
		}
		for _, signer := range sd.Signers {
			if method != "" && !signatureMethodMatchesSigner(method, signer.SignatureAlg, signer.DigestAlg) {
				return nil, fmt.Errorf("signature method does not match SignedData")
			}
		}
		return verifyGBT35275SignedData(sd, signedData, options)
	}
	if isSM2SignatureMethod(method) {
		return verifyRawDigitalSignature(signedValue, signedData, options)
	}
	if isRSASignatureMethod(method) || isECDSASignatureMethod(method) {
		return verifyRawPublicKeySignature(method, digestMethod, signedValue, signedData, options)
	}
	return nil, fmt.Errorf("unsupported signature method: %s", method)
}

// signatureMethodMatchesSigner 校验Signature.xml签名算法与SignerInfo算法一致，仅在SignatureMethod显式携带哈希时比对哈希算法
// 入参: method Signature.xml签名算法, signerAlg SignerInfo签名算法, signerDigest SignerInfo摘要算法
// 返回: bool 是否一致
func signatureMethodMatchesSigner(method, signerAlg, signerDigest string) bool {
	if isSM2SignatureMethod(method) && isSM2SignatureMethod(signerAlg) {
		return true
	}
	if !(isRSASignatureMethod(method) && isRSASignatureMethod(signerAlg) || isECDSASignatureMethod(method) && isECDSASignatureMethod(signerAlg)) {
		return false
	}
	y, err2 := signatureMethodHash(signerAlg, signerDigest)
	if err2 != nil {
		return false
	}
	if x, err := signatureMethodHash(method, ""); err == nil {
		return x == y
	}
	return true
}

// verifyRawDigitalSignature 验证裸SM2数字签名
// 入参: signedValue 签名值, signedData 被签名原文, options 验证选项
// 返回: *digitalVerifyResult 验证结果, error 错误信息
func verifyRawDigitalSignature(signedValue, signedData []byte, options *signatureVerifyOptions) (*digitalVerifyResult, error) {
	certs := compactSignatureCerts(options.SignCerts)
	if len(certs) == 0 {
		return nil, fmt.Errorf("signature certificate not found")
	}
	var candidates [][]byte
	for _, cert := range certs {
		pub, err := parseSM2PublicKeyFromCert(cert)
		if err != nil {
			continue
		}
		candidates = append(candidates, cert)
		if verifySM2Signature(pub, nil, signedData, signedValue) {
			return &digitalVerifyResult{
				DataHashChecked: true,
				DataHashOK:      true,
				SignedChecked:   true,
				SignedOK:        true,
				CertChecked:     true,
				CertOK:          true,
				SignerCerts:     [][]byte{cert},
				CertInfo:        signatureCertInfo(cert),
			}, nil
		}
	}
	result := &digitalVerifyResult{CertChecked: true}
	if len(candidates) == 0 {
		return result, nil
	}
	result.DataHashChecked = true
	result.DataHashOK = true
	result.SignedChecked = true
	result.SignedOK = false
	if len(candidates) == 1 {
		result.CertOK = true
		result.SignerCerts = [][]byte{candidates[0]}
		result.CertInfo = signatureCertInfo(candidates[0])
	}
	return result, nil
}

// verifyRawPublicKeySignature 验证裸RSA或ECDSA数字签名
// 入参: method 签名算法, digestMethod 摘要算法, signedValue 签名值, signedData 被签名原文, options 验证选项
// 返回: *digitalVerifyResult 验证结果, error 错误信息
func verifyRawPublicKeySignature(method, digestMethod string, signedValue, signedData []byte, options *signatureVerifyOptions) (*digitalVerifyResult, error) {
	certs := compactSignatureCerts(options.SignCerts)
	if len(certs) == 0 {
		return nil, fmt.Errorf("signature certificate not found")
	}
	if _, err := signatureMethodHash(method, digestMethod); err != nil {
		return nil, err
	}
	var candidates [][]byte
	for _, cert := range certs {
		if !certMatchesPublicKeyMethod(cert, method) {
			continue
		}
		ok, err := verifyPublicKeySignature(method, digestMethod, cert, signedData, signedValue)
		if err != nil {
			continue
		}
		candidates = append(candidates, cert)
		if ok {
			return &digitalVerifyResult{
				DataHashChecked: true,
				DataHashOK:      true,
				SignedChecked:   true,
				SignedOK:        true,
				CertChecked:     true,
				CertOK:          true,
				SignerCerts:     [][]byte{cert},
				CertInfo:        signatureCertInfo(cert),
			}, nil
		}
	}
	result := &digitalVerifyResult{CertChecked: true}
	if len(candidates) == 0 {
		return result, nil
	}
	result.DataHashChecked = true
	result.DataHashOK = true
	result.SignedChecked = true
	result.SignedOK = false
	if len(candidates) == 1 {
		result.CertOK = true
		result.SignerCerts = [][]byte{candidates[0]}
		result.CertInfo = signatureCertInfo(candidates[0])
	}
	return result, nil
}

// verifyGBT35275SignedData 验证GB/T35275 SignedData签名值，逐一校验全部SignerInfo签名与证书
// 入参: sd 已解析SignedData结构, signedData 被签名原文, options 验证选项
// 返回: *digitalVerifyResult 验证结果, error 错误信息
func verifyGBT35275SignedData(sd *gbtSignedData, signedData []byte, options *signatureVerifyOptions) (*digitalVerifyResult, error) {
	if options.Policy != nil && len(options.Policy.SignedDataVersions) != 0 {
		allowed := false
		for _, v := range options.Policy.SignedDataVersions {
			allowed = allowed || v == sd.Version
		}
		if !allowed {
			return nil, rejectSignaturePolicy("SignedData version rejected: %d", sd.Version)
		}
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
	result := &digitalVerifyResult{SignatureMethod: sd.Signers[0].SignatureAlg, Certs: sd.rawCerts()}
	for index, signer := range sd.Signers {
		digestListed := false
		for _, alg := range sd.DigestAlgs {
			if signatureDigestEquivalent(alg, signer.DigestAlg) {
				digestListed = true
				break
			}
		}
		if !digestListed {
			return result, fmt.Errorf("signer digest algorithm not in SignedData digestAlgorithms")
		}
		if err := signatureSignerDigest(signer.SignatureAlg, signer.DigestAlg); err != nil {
			return result, err
		}
		if err := signatureAlgorithmPolicy(options.Policy, signer.SignatureAlg, signer.DigestAlg); err != nil {
			return result, err
		}
		if !signer.SigningTime.IsZero() && result.SignatureTime.IsZero() {
			result.SignatureTime = signer.SigningTime
		}
		digest, err := signatureDigest(signer.DigestAlg, signedData)
		if err != nil {
			return result, err
		}
		doubleDigest, _ := signatureDigest(signer.DigestAlg, digest)
		fillCert := func() {
			if c, findErr := sd.findCert(signer); findErr == nil && c != nil {
				if _, parseErr := parseSignatureCertificate(c.Raw); parseErr == nil {
					if index == 0 && len(result.CertInfo.Raw) == 0 {
						result.CertInfo = signatureCertInfo(c.Raw)
					}
					result.SignerCerts = append(result.SignerCerts, c.Raw)
				}
			}
		}
		var plains [][]byte
		if sd.HasContent {
			contentIsPlain := bytes.Equal(sd.Content, signedData)
			contentIsDigest := bytes.Equal(sd.Content, digest)
			if !contentIsPlain && !contentIsDigest {
				fillCert()
				result.DataHashChecked = true
				result.DataHashOK = false
				return result, nil
			}
			if len(signer.AuthAttrs) != 0 {
				hashMatch := bytes.Equal(signer.AttrDigest, digest)
				if !contentIsPlain && contentIsDigest {
					hashMatch = hashMatch || bytes.Equal(signer.AttrDigest, doubleDigest)
				}
				if !hashMatch {
					fillCert()
					result.DataHashChecked = true
					result.DataHashOK = false
					return result, nil
				}
				plains = [][]byte{signer.AuthAttrs}
				if len(signer.AuthAttrsSorted) != 0 && !bytes.Equal(signer.AuthAttrsSorted, signer.AuthAttrs) {
					plains = append(plains, signer.AuthAttrsSorted)
				}
			} else {
				plains = [][]byte{sd.Content}
			}
		} else {
			if len(signer.AuthAttrs) != 0 {
				if !bytes.Equal(signer.AttrDigest, digest) && !bytes.Equal(signer.AttrDigest, doubleDigest) {
					fillCert()
					result.DataHashChecked = true
					result.DataHashOK = false
					return result, nil
				}
				plains = [][]byte{signer.AuthAttrs}
				if len(signer.AuthAttrsSorted) != 0 && !bytes.Equal(signer.AuthAttrsSorted, signer.AuthAttrs) {
					plains = append(plains, signer.AuthAttrsSorted)
				}
			} else {
				plains = [][]byte{signedData, digest}
			}
		}
		result.DataHashChecked = true
		result.DataHashOK = true
		cert, err := sd.findCert(signer)
		if err != nil {
			result.CertChecked = true
			result.CertOK = false
			return result, err
		}
		if cert == nil {
			result.CertChecked = true
			result.CertOK = false
			return result, nil
		}
		if _, err := parseSignatureCertificate(cert.Raw); err != nil {
			result.CertChecked = true
			result.CertOK = false
			return result, err
		}
		result.SignerCerts = append(result.SignerCerts, cert.Raw)
		if !signer.SigningTime.IsZero() {
			result.SignerTimes = append(result.SignerTimes, signerCertTime{cert: cert.Raw, at: signer.SigningTime})
		}
		tokens, err := signatureTimestampAttributes(signer.UnauthAttrs)
		if err != nil {
			return result, err
		}
		for _, token := range tokens {
			result.Timestamps = append(result.Timestamps, signatureTimestampEvidence{Token: token, Data: signer.Signature})
		}
		certInfo := signatureCertInfo(cert.Raw)
		if index == 0 {
			result.CertInfo = certInfo
		}
		verified := false
		if isSM2SignatureMethod(signer.SignatureAlg) {
			pub, err := parseSM2PublicKeyFromCert(cert.Raw)
			if err != nil {
				result.CertInfo = certInfo
				result.CertChecked = true
				result.CertOK = false
				return result, err
			}
			for _, plain := range plains {
				if verifySM2Signature(pub, nil, plain, signer.Signature) {
					verified = true
					break
				}
			}
		} else if isRSASignatureMethod(signer.SignatureAlg) || isECDSASignatureMethod(signer.SignatureAlg) {
			for _, plain := range plains {
				var ok bool
				var verifyErr error
				if signatureMethodText(signer.SignatureAlg) == "1.2.840.113549.1.1.10" {
					ok, verifyErr = verifyRSAPSSSignature(signer.SignatureAlgRaw, signer.DigestAlg, cert.Raw, plain, signer.Signature)
				} else {
					ok, verifyErr = verifyPublicKeySignature(signer.SignatureAlg, signer.DigestAlg, cert.Raw, plain, signer.Signature)
				}
				if verifyErr != nil {
					result.CertInfo = certInfo
					result.CertChecked = true
					result.CertOK = false
					return result, verifyErr
				}
				if ok {
					verified = true
					break
				}
			}
		} else {
			return result, fmt.Errorf("unsupported signature method: %s", signer.SignatureAlg)
		}
		if !verified {
			result.CertInfo = certInfo
			result.SignedChecked = true
			result.SignedOK = false
			result.CertChecked = true
			result.CertOK = true
			return result, nil
		}
	}
	result.CertChecked = true
	result.CertOK = true
	result.SignedChecked = true
	result.SignedOK = true
	return result, nil
}

// normalizeGBT35275SignedValue 规范化GB/T35275 SignedData编码
// 入参: data 签名值数据
// 返回: []byte 定长编码数据, bool 是否为SignedData
func normalizeGBT35275SignedValue(data []byte) ([]byte, bool) {
	if !hasBERIndefiniteOrConstructedOctet(data) {
		if contentType, ok := gbtContentType(data); ok {
			return data, contentType == signContentSignedData || contentType == signContentCMS
		}
	}
	der, err := berToDefinite(data)
	if err != nil {
		return nil, false
	}
	contentType, ok := gbtContentType(der)
	if !ok || contentType != signContentSignedData && contentType != signContentCMS {
		return nil, false
	}
	return der, true
}

// hasBERIndefiniteOrConstructedOctet 检测BER数据中是否存在不定长、非最短长度或构造型OCTET STRING
// 入参: data 待检测ASN.1数据
// 返回: bool 是否需要执行BER规范化
func hasBERIndefiniteOrConstructedOctet(data []byte) bool {
	return scanBERNeedsNormalize(data, 0)
}

// scanBERNeedsNormalize 递归扫描BER节点序列检测是否需要定长或OCTET STRING规范化
// 入参: data 待扫描字节, depth 当前嵌套深度
// 返回: bool 是否需要规范化
func scanBERNeedsNormalize(data []byte, depth int) bool {
	if depth > 128 {
		return true
	}
	pos := 0
	for pos < len(data) {
		if len(data)-pos < 2 {
			return false
		}
		tagStart := pos
		firstTag := data[pos]
		pos++
		if firstTag&0x1f == 0x1f {
			for pos < len(data) && data[pos]&0x80 != 0 {
				pos++
			}
			pos++
		}
		if pos >= len(data) {
			return false
		}
		if pos-tagStart == 1 && firstTag == 0x24 {
			return true
		}
		firstLength := data[pos]
		pos++
		if firstLength == 0x80 {
			return true
		}
		length := uint64(firstLength)
		if firstLength&0x80 != 0 {
			n := int(firstLength & 0x7f)
			if n == 0 || n > 8 || len(data)-pos < n {
				return false
			}
			if data[pos] == 0 {
				return true
			}
			length = 0
			for _, b := range data[pos : pos+n] {
				length = length<<8 | uint64(b)
			}
			pos += n
			if length < 128 {
				return true
			}
		}
		if length > uint64(len(data)-pos) {
			return false
		}
		end := pos + int(length)
		if firstTag&0x20 != 0 && scanBERNeedsNormalize(data[pos:end], depth+1) {
			return true
		}
		pos = end
	}
	return false
}

// gbtContentType 读取GB/T35275内容类型
// 入参: data DER编码数据
// 返回: string 内容类型, bool 是否完成解析
func gbtContentType(data []byte) (string, bool) {
	var root asn1.RawValue
	rest, err := asn1.Unmarshal(data, &root)
	if err != nil || len(rest) != 0 || root.Tag != signASN1Sequence {
		return "", false
	}
	items, ok := asn1Children(root.Bytes)
	if !ok || len(items) == 0 {
		return "", false
	}
	if items[0].Tag != asn1.TagOID {
		return "", true
	}
	oid, err := asn1OIDString(items[0])
	return oid, err == nil
}

// berToDefinite 将BER不定长编码转换为定长编码
// 入参: data BER编码数据
// 返回: []byte 定长编码数据, error 错误信息
func berToDefinite(data []byte) ([]byte, error) {
	out, n, err := berValueToDefinite(data, 0)
	if err != nil {
		return nil, err
	}
	if n != len(data) {
		return nil, fmt.Errorf("invalid BER trailing data")
	}
	return out, nil
}

// berValueToDefinite 转换单个BER编码值并递归合并构造型OCTET STRING
// 入参: data BER编码数据, depth 当前嵌套深度
// 返回: []byte 定长编码数据, int 已读取长度, error 错误信息
func berValueToDefinite(data []byte, depth int) ([]byte, int, error) {
	if depth > 128 {
		return nil, 0, fmt.Errorf("BER nesting exceeds parsing budget")
	}
	if len(data) < 2 {
		return nil, 0, fmt.Errorf("invalid BER value")
	}
	pos := 1
	if data[0]&0x1f == 0x1f {
		for pos < len(data) && data[pos]&0x80 != 0 {
			pos++
		}
		pos++
	}
	if pos >= len(data) {
		return nil, 0, fmt.Errorf("invalid BER tag")
	}
	tag := data[:pos]
	constructedOctet := len(tag) == 1 && tag[0] == 0x24
	firstLength := data[pos]
	pos++
	if firstLength == 0x80 {
		if tag[0]&0x20 == 0 {
			return nil, 0, fmt.Errorf("invalid BER indefinite primitive")
		}
		var content []byte
		for {
			if len(data)-pos < 2 {
				return nil, 0, fmt.Errorf("invalid BER unterminated value")
			}
			if data[pos] == 0 && data[pos+1] == 0 {
				pos += 2
				break
			}
			child, n, err := berValueToDefinite(data[pos:], depth+1)
			if err != nil {
				return nil, 0, err
			}
			if constructedOctet {
				part, err := berPrimitiveOctetBytes(child)
				if err != nil {
					return nil, 0, err
				}
				content = append(content, part...)
			} else {
				content = append(content, child...)
			}
			pos += n
		}
		if constructedOctet {
			return wrapBERValue([]byte{0x04}, content), pos, nil
		}
		return wrapBERValue(tag, content), pos, nil
	}
	length := uint64(firstLength)
	if firstLength&0x80 != 0 {
		n := int(firstLength & 0x7f)
		if n == 0 || n > 8 || len(data)-pos < n {
			return nil, 0, fmt.Errorf("invalid BER length")
		}
		length = 0
		for _, b := range data[pos : pos+n] {
			length = length<<8 | uint64(b)
		}
		pos += n
	}
	if length > uint64(len(data)-pos) {
		return nil, 0, fmt.Errorf("invalid BER truncated value")
	}
	end := pos + int(length)
	content := data[pos:end]
	if tag[0]&0x20 != 0 {
		content = nil
		for pos < end {
			child, n, err := berValueToDefinite(data[pos:end], depth+1)
			if err != nil {
				return nil, 0, err
			}
			if constructedOctet {
				part, err := berPrimitiveOctetBytes(child)
				if err != nil {
					return nil, 0, err
				}
				content = append(content, part...)
			} else {
				content = append(content, child...)
			}
			pos += n
		}
		if constructedOctet {
			return wrapBERValue([]byte{0x04}, content), end, nil
		}
	}
	return wrapBERValue(tag, content), end, nil
}

// berPrimitiveOctetBytes 提取规范化后的原语OCTET STRING载荷
// 入参: der 规范化后的DER节点
// 返回: []byte 载荷字节, error 非原语OCTET STRING错误
func berPrimitiveOctetBytes(der []byte) ([]byte, error) {
	var raw asn1.RawValue
	rest, err := asn1.Unmarshal(der, &raw)
	if err != nil || len(rest) != 0 || raw.Class != asn1.ClassUniversal || raw.Tag != asn1.TagOctetString || raw.IsCompound {
		return nil, fmt.Errorf("invalid BER constructed octet string")
	}
	return raw.Bytes, nil
}

// wrapBERValue 包装定长BER编码值
// 入参: tag 标签, content 内容
// 返回: []byte 定长编码数据
func wrapBERValue(tag, content []byte) []byte {
	out := make([]byte, 0, len(tag)+len(content)+9)
	out = append(out, tag...)
	out = append(out, asn1LengthBytes(len(content))...)
	return append(out, content...)
}

// parseGBT35275Normalized 解析已定长规范化的GB/T35275 SignedData
// 入参: data 已规范化DER编码数据
// 返回: *gbtSignedData SignedData结构, error 错误信息
func parseGBT35275Normalized(data []byte) (*gbtSignedData, error) {
	contentType, content, ok, err := parseGBTContentInfoBytes(data)
	if err != nil {
		return nil, err
	}
	if !ok || contentType != signContentSignedData && contentType != signContentCMS {
		return nil, fmt.Errorf("invalid signed data content type")
	}
	if content.Class != asn1.ClassUniversal || content.Tag != asn1.TagSequence || !content.IsCompound {
		return nil, fmt.Errorf("invalid signed data")
	}
	items, ok := asn1Children(content.Bytes)
	if !ok || len(items) < 4 {
		return nil, fmt.Errorf("invalid signed data")
	}
	version, err := asn1Integer(items[0])
	if err != nil || version != 1 && version != 3 {
		return nil, fmt.Errorf("unsupported GB/T signed data version: %d", version)
	}
	if items[1].Class != asn1.ClassUniversal || items[1].Tag != asn1.TagSet || !items[1].IsCompound {
		return nil, fmt.Errorf("invalid signed data digest algorithms")
	}
	algItems, ok := asn1Children(items[1].Bytes)
	if !ok {
		return nil, fmt.Errorf("invalid signed data digest algorithms")
	}
	digestAlgs := make([]string, 0, len(algItems))
	for _, algItem := range algItems {
		alg, err := parseGBTAlgorithm(algItem)
		if err != nil {
			return nil, err
		}
		digestAlgs = append(digestAlgs, alg)
	}
	contentOID, innerContent, hasContent, err := parseGBTContentInfo(items[2])
	if err != nil {
		return nil, err
	}
	if !isGBTDataContentType(contentOID) {
		return nil, fmt.Errorf("invalid signed data inner content type")
	}
	sd := &gbtSignedData{
		Version:     version,
		DigestAlgs:  digestAlgs,
		ContentType: contentOID,
	}
	if hasContent {
		sd.Content, err = asn1OctetString(innerContent)
		if err != nil {
			return nil, err
		}
		sd.HasContent = true
	}
	idx := 3
	if idx < len(items) && items[idx].Class == asn1.ClassContextSpecific && items[idx].Tag == 0 {
		if !items[idx].IsCompound {
			return nil, fmt.Errorf("invalid signed data certificates")
		}
		certs, err := parseGBTCertificates(items[idx])
		if err != nil {
			return nil, err
		}
		sd.Certs = certs
		idx++
	}
	if idx < len(items) && items[idx].Class == asn1.ClassContextSpecific && items[idx].Tag == 1 {
		if !items[idx].IsCompound {
			return nil, fmt.Errorf("invalid signed data crls")
		}
		idx++
	}
	if idx != len(items)-1 || items[idx].Class != asn1.ClassUniversal || items[idx].Tag != asn1.TagSet || !items[idx].IsCompound {
		return nil, fmt.Errorf("invalid signer infos")
	}
	signers, err := parseGBTSignerInfosForContent(items[idx], sd.ContentType)
	if err != nil {
		return nil, err
	}
	sd.Signers = signers
	return sd, nil
}

// isGBTDataContentType 判断内容类型OID是否为GB/T35275或标准CMS数据内容类型
// 入参: oid 对象标识符
// 返回: bool 是否为合法数据内容类型
func isGBTDataContentType(oid string) bool {
	return oid == signContentData || oid == signContentCMSData
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
	if !ok || len(items) == 0 || len(items) > 2 || raw.Class != asn1.ClassUniversal || raw.Tag != asn1.TagSequence || !raw.IsCompound {
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
		if item.Class == asn1.ClassContextSpecific {
			continue
		}
		if item.Class != asn1.ClassUniversal || item.Tag != asn1.TagSequence || !item.IsCompound {
			return nil, fmt.Errorf("invalid signed data certificate")
		}
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
		Raw:          cert.Raw,
		Issuer:       cert.RawIssuer,
		Serial:       cert.SerialNumber,
		SubjectKeyID: bytes.Clone(cert.SubjectKeyId),
	}, nil
}

// parseGBTSignerInfos 解析签名者信息集合
// 入参: raw ASN.1原始值
// 返回: []gbtSignerInfo 签名者列表, error 错误信息
func parseGBTSignerInfos(raw asn1.RawValue) ([]gbtSignerInfo, error) {
	return parseGBTSignerInfosForContent(raw, "")
}

// parseGBTSignerInfosForContent 按内层内容类型解析签名者信息集合
// 入参: raw ASN.1原始值, innerContentType 内层内容类型OID，空值不限定Data OID
// 返回: []gbtSignerInfo 签名者列表, error 错误信息
func parseGBTSignerInfosForContent(raw asn1.RawValue, innerContentType string) ([]gbtSignerInfo, error) {
	items, ok := asn1Children(raw.Bytes)
	if !ok {
		return nil, fmt.Errorf("invalid signer infos")
	}
	signers := make([]gbtSignerInfo, 0, len(items))
	for _, item := range items {
		signer, err := parseGBTSignerInfoForContent(item, innerContentType)
		if err != nil {
			return nil, err
		}
		signers = append(signers, signer)
	}
	return signers, nil
}

// parseGBTSignerInfoForContent 按内层内容类型解析签名者信息
// 入参: raw ASN.1原始值, innerContentType 内层内容类型OID，空值不限定Data OID
// 返回: gbtSignerInfo 签名者信息, error 错误信息
func parseGBTSignerInfoForContent(raw asn1.RawValue, innerContentType string) (gbtSignerInfo, error) {
	if raw.Class != asn1.ClassUniversal || raw.Tag != asn1.TagSequence || !raw.IsCompound {
		return gbtSignerInfo{}, fmt.Errorf("invalid signer info")
	}
	items, ok := asn1Children(raw.Bytes)
	if !ok || len(items) < 5 {
		return gbtSignerInfo{}, fmt.Errorf("invalid signer info")
	}
	version, err := asn1Integer(items[0])
	if err != nil || version != 1 && version != 3 {
		return gbtSignerInfo{}, fmt.Errorf("unsupported signer info version")
	}
	var issuer []byte
	var serial *big.Int
	var subjectKeyID []byte
	if version == 1 {
		issuer, serial, err = parseGBTIssuerAndSerial(items[1])
		if err != nil {
			return gbtSignerInfo{}, err
		}
	} else {
		if items[1].Class != asn1.ClassContextSpecific || items[1].Tag != 0 || items[1].IsCompound || len(items[1].Bytes) == 0 {
			return gbtSignerInfo{}, fmt.Errorf("invalid subject key identifier")
		}
		subjectKeyID = bytes.Clone(items[1].Bytes)
	}
	digestAlg, err := parseGBTAlgorithm(items[2])
	if err != nil {
		return gbtSignerInfo{}, err
	}
	idx := 3
	var authAttrs []byte
	var authAttrsSorted []byte
	var attrDigest []byte
	var signingTime time.Time
	if items[idx].Class == asn1.ClassContextSpecific && items[idx].Tag == 0 {
		authAttrs = asn1SetBytes(items[idx].Bytes)
		attrDigest, authAttrsSorted, signingTime, err = parseGBTAuthAttrs(items[idx], innerContentType)
		if err != nil {
			return gbtSignerInfo{}, err
		}
		idx++
	}
	if len(items) <= idx+1 {
		return gbtSignerInfo{}, fmt.Errorf("invalid signer info")
	}
	signatureAlgRaw := items[idx]
	signatureAlg, err := parseGBTAlgorithm(signatureAlgRaw)
	if err != nil {
		return gbtSignerInfo{}, err
	}
	signature, err := asn1OctetString(items[idx+1])
	if err != nil {
		return gbtSignerInfo{}, err
	}
	var unauth asn1.RawValue
	if len(items) > idx+2 {
		unauth = items[idx+2]
		if len(items) != idx+3 || unauth.Class != asn1.ClassContextSpecific || unauth.Tag != 1 || !unauth.IsCompound {
			return gbtSignerInfo{}, fmt.Errorf("invalid unauthenticated attributes")
		}
	}
	return gbtSignerInfo{
		Version:         version,
		UnauthAttrs:     unauth,
		Issuer:          issuer,
		Serial:          serial,
		SubjectKeyID:    subjectKeyID,
		DigestAlg:       digestAlg,
		SignatureAlg:    signatureAlg,
		SignatureAlgRaw: signatureAlgRaw,
		Signature:       signature,
		AuthAttrs:       authAttrs,
		AuthAttrsSorted: authAttrsSorted,
		AttrDigest:      attrDigest,
		SigningTime:     signingTime,
	}, nil
}

// parseGBTIssuerAndSerial 解析证书颁发者和序列号
// 入参: raw ASN.1原始值
// 返回: []byte 颁发者DN, *big.Int 序列号, error 错误信息
func parseGBTIssuerAndSerial(raw asn1.RawValue) ([]byte, *big.Int, error) {
	if raw.Class != asn1.ClassUniversal || raw.Tag != asn1.TagSequence || !raw.IsCompound {
		return nil, nil, fmt.Errorf("invalid issuer and serial")
	}
	items, ok := asn1Children(raw.Bytes)
	if !ok || len(items) != 2 || items[0].Class != asn1.ClassUniversal || items[0].Tag != asn1.TagSequence || !items[0].IsCompound {
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
	if raw.Class == asn1.ClassUniversal && raw.Tag == asn1.TagOID && !raw.IsCompound {
		return asn1OIDString(raw)
	}
	if raw.Class != asn1.ClassUniversal || raw.Tag != asn1.TagSequence || !raw.IsCompound {
		return "", fmt.Errorf("invalid algorithm identifier")
	}
	items, ok := asn1Children(raw.Bytes)
	if !ok || len(items) == 0 || len(items) > 2 {
		return "", fmt.Errorf("invalid algorithm identifier")
	}
	return asn1OIDString(items[0])
}

// parseGBTAuthAttrs 解析并校验认证属性集合，返回摘要、DER排序编码和签名时间
// 入参: raw ASN.1原始值, innerContentType 内层内容类型OID，空值仅校验contentType为合法OID
// 返回: []byte 摘要值, []byte DER升序排序后的SET编码, time.Time 签名时间, error 错误信息
func parseGBTAuthAttrs(raw asn1.RawValue, innerContentType string) ([]byte, []byte, time.Time, error) {
	if !raw.IsCompound {
		return nil, nil, time.Time{}, fmt.Errorf("invalid authenticated attributes")
	}
	attrs, ok := asn1Children(raw.Bytes)
	if !ok || len(attrs) == 0 {
		return nil, nil, time.Time{}, fmt.Errorf("invalid authenticated attributes")
	}
	seen := make(map[string]bool, len(attrs))
	rawAttrs := make([][]byte, 0, len(attrs))
	var digest []byte
	var signingTime time.Time
	hasContentType := false
	hasDigest := false
	for _, attr := range attrs {
		if attr.Class != asn1.ClassUniversal || attr.Tag != asn1.TagSequence || !attr.IsCompound {
			return nil, nil, time.Time{}, fmt.Errorf("invalid authenticated attribute")
		}
		items, ok := asn1Children(attr.Bytes)
		if !ok || len(items) != 2 || items[1].Class != asn1.ClassUniversal || items[1].Tag != asn1.TagSet || !items[1].IsCompound {
			return nil, nil, time.Time{}, fmt.Errorf("invalid authenticated attribute")
		}
		oid, err := asn1OIDString(items[0])
		if err != nil {
			return nil, nil, time.Time{}, err
		}
		if seen[oid] {
			return nil, nil, time.Time{}, fmt.Errorf("duplicate authenticated attribute: %s", oid)
		}
		seen[oid] = true
		values, ok := asn1Children(items[1].Bytes)
		if !ok || len(values) == 0 {
			return nil, nil, time.Time{}, fmt.Errorf("invalid authenticated attribute value")
		}
		rawAttrs = append(rawAttrs, attr.FullBytes)
		switch oid {
		case signAttrContentType:
			if len(values) != 1 {
				return nil, nil, time.Time{}, fmt.Errorf("invalid authenticated attribute value")
			}
			ct, err := asn1OIDString(values[0])
			if err != nil {
				return nil, nil, time.Time{}, err
			}
			if innerContentType != "" {
				if isGBTDataContentType(innerContentType) {
					if !isGBTDataContentType(ct) {
						return nil, nil, time.Time{}, fmt.Errorf("content type attribute mismatch")
					}
				} else if ct != innerContentType {
					return nil, nil, time.Time{}, fmt.Errorf("content type attribute mismatch")
				}
			}
			hasContentType = true
		case signAttrMessageDigest:
			if len(values) != 1 {
				return nil, nil, time.Time{}, fmt.Errorf("invalid authenticated attribute value")
			}
			digest, err = asn1OctetString(values[0])
			if err != nil {
				return nil, nil, time.Time{}, err
			}
			hasDigest = true
		case signAttrSigningTime:
			if len(values) != 1 {
				return nil, nil, time.Time{}, fmt.Errorf("invalid authenticated attribute value")
			}
			signingTime, err = asn1Time(values[0])
			if err != nil {
				return nil, nil, time.Time{}, err
			}
		}
	}
	if !hasContentType {
		return nil, nil, time.Time{}, fmt.Errorf("content type attribute not found")
	}
	if !hasDigest {
		return nil, nil, time.Time{}, fmt.Errorf("message digest attribute not found")
	}
	slices.SortFunc(rawAttrs, bytes.Compare)
	var sortedContent []byte
	for _, item := range rawAttrs {
		sortedContent = append(sortedContent, item...)
	}
	return digest, asn1SetBytes(sortedContent), signingTime, nil
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

// findCert 查找唯一匹配的签名者证书
// 入参: signer 签名者信息
// 返回: *gbtCertificate 证书信息, error 多张不同证书歧义错误
func (sd *gbtSignedData) findCert(signer gbtSignerInfo) (*gbtCertificate, error) {
	var matched *gbtCertificate
	for i := range sd.Certs {
		cert := &sd.Certs[i]
		hit := false
		if signer.Version == 3 {
			hit = len(signer.SubjectKeyID) != 0 && bytes.Equal(cert.SubjectKeyID, signer.SubjectKeyID)
		} else {
			hit = bytes.Equal(cert.Issuer, signer.Issuer) && cert.Serial != nil && signer.Serial != nil && cert.Serial.Cmp(signer.Serial) == 0
		}
		if !hit {
			continue
		}
		if matched != nil && !bytes.Equal(matched.Raw, cert.Raw) {
			return nil, fmt.Errorf("ambiguous signer certificate")
		}
		matched = cert
	}
	return matched, nil
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
