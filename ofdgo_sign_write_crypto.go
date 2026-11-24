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
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/emmansun/gmsm/pkcs8"
	"github.com/emmansun/gmsm/sm2"
	"github.com/emmansun/gmsm/sm3"
	"github.com/emmansun/gmsm/smx509"
	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
)

// signatureWriteKey 已验证签署身份
type signatureWriteKey struct {
	cert   *smx509.Certificate
	method string
	digest string
}

// ParseSignatureIdentity 解析签署私钥和证书，并检查公私钥对应关系
// 支持普通PKCS8、SEC1、PKCS1以及加密PKCS8，不支持传统加密PEM
// 不加载系统信任或暗中信任该证书，返回的SM2/RSA私钥同时实现crypto.Decrypter
// 入参: certificate 证书DER或PEM, privateKey 私钥DER或PEM, password 可选的单一私钥口令
// 返回: crypto.Signer 签署器, []byte 证书DER, error 错误信息
func ParseSignatureIdentity(certificate, privateKey []byte, password ...[]byte) (crypto.Signer, []byte, error) {
	if len(password) > 1 {
		return nil, nil, fmt.Errorf("expected at most one private key password")
	}
	cert, err := parseSignatureCertificate(certificate)
	if err != nil {
		return nil, nil, err
	}
	der := privateKey
	encrypted := false
	if block, rest := pem.Decode(privateKey); block != nil {
		defer clear(block.Bytes)
		if len(bytes.TrimSpace(rest)) != 0 || len(block.Headers) != 0 {
			return nil, nil, fmt.Errorf("legacy encrypted PEM or multiple private keys are unsupported")
		}
		encrypted = block.Type == "ENCRYPTED PRIVATE KEY"
		if !encrypted && block.Type != "PRIVATE KEY" && block.Type != "RSA PRIVATE KEY" && block.Type != "EC PRIVATE KEY" {
			return nil, nil, fmt.Errorf("unsupported private key PEM type")
		}
		der = block.Bytes
	}
	var raw asn1.RawValue
	if rest, err := asn1.Unmarshal(der, &raw); err != nil || len(rest) != 0 || raw.Tag != asn1.TagSequence || raw.Class != asn1.ClassUniversal {
		return nil, nil, fmt.Errorf("invalid private key encoding")
	}
	var pass []byte
	if len(password) == 1 {
		pass = bytes.Clone(password[0])
		defer clear(pass)
	}
	if encrypted && len(pass) == 0 {
		return nil, nil, fmt.Errorf("private key password required")
	}
	key, err := pkcs8.ParsePKCS8PrivateKey(der, pass)
	if err != nil && !encrypted {
		key, err = smx509.ParsePKCS8PrivateKey(der)
		if err != nil {
			key, err = smx509.ParseTypedECPrivateKey(der)
		}
		if err != nil {
			key, err = x509.ParsePKCS1PrivateKey(der)
		}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("invalid private key or incorrect password")
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, nil, fmt.Errorf("unsupported signature private key")
	}
	if _, _, err := signatureWriteAlgorithm(signer, cert); err != nil {
		return nil, nil, err
	}
	return signer, bytes.Clone(cert.Raw), nil
}

// signatureWriteIdentity 检查签署身份、证书信任与印章授权
// 制章证书按制章时间验证，不以签署时间代替制章时间
// 入参: options 签署选项
// 返回: *signatureWriteKey 已验证身份, error 错误信息
func signatureWriteIdentity(options SignatureWriteOptions) (*signatureWriteKey, error) {
	cert, err := parseSignatureCertificate(options.Certificate)
	if err != nil {
		return nil, err
	}
	method, digest, err := signatureWriteAlgorithm(options.Signer, cert)
	if err != nil {
		return nil, err
	}
	if err := signatureWriteTrust(cert, options.Time, options); err != nil {
		return nil, fmt.Errorf("signer certificate: %w", err)
	}
	if len(options.Seal) > 0 {
		if method != signMethodSM2SM3 {
			return nil, fmt.Errorf("SES v4 writing requires SM2/SM3")
		}
		var raw asn1.RawValue
		rest, err := asn1.Unmarshal(options.Seal, &raw)
		if err != nil || len(rest) != 0 || raw.Tag != asn1.TagSequence || raw.Class != asn1.ClassUniversal {
			return nil, fmt.Errorf("invalid SES seal DER; image files are not electronic seals")
		}
		if err := signatureWriteSealStructure(raw); err != nil {
			return nil, err
		}
		seal, err := parseSESSeal(raw)
		if err != nil {
			return nil, err
		}
		if seal.Info.Version != 4 || seal.SignAlg != signMethodSM2SM3 {
			return nil, fmt.Errorf("only SES v4 SM2/SM3 seals are supported")
		}
		if !sesCertInList(cert.Raw, seal.CertList) {
			return nil, fmt.Errorf("signer certificate is not authorized by the seal")
		}
		if seal.Info.CreateTime.IsZero() || seal.Info.ValidStart.IsZero() || seal.Info.ValidEnd.IsZero() || options.Time.Before(seal.Info.CreateTime) || options.Time.Before(seal.Info.ValidStart) || options.Time.After(seal.Info.ValidEnd) || seal.Info.ValidEnd.Before(seal.Info.ValidStart) {
			return nil, fmt.Errorf("seal is not valid at signature time")
		}
		maker, err := parseSignatureCertificate(seal.Cert)
		if err != nil {
			return nil, err
		}
		if err := signatureWriteTrust(maker, seal.Info.CreateTime, options); err != nil {
			return nil, fmt.Errorf("seal maker certificate: %w", err)
		}
		pub, ok := maker.PublicKey.(*ecdsa.PublicKey)
		if !ok || !sm2.IsSM2PublicKey(pub) || !sm2.VerifyASN1WithSM2(pub, nil, seal.SignData, seal.Signature) {
			return nil, fmt.Errorf("invalid seal maker signature")
		}
		mediaType, media := probeSealMedia(seal.PicData)
		if len(media) == 0 || mediaType != baseimage.NormalizeFormat(seal.PicType) {
			return nil, fmt.Errorf("invalid seal picture")
		}
	}
	return &signatureWriteKey{cert: cert, method: method, digest: digest}, nil
}

// signatureWriteSealStructure 限定GB/T38540-2020 SES v4印章结构，拒绝旧版伪装、弱算法及非法字段
// 入参: raw 印章DER节点
// 返回: error 错误信息
func signatureWriteSealStructure(raw asn1.RawValue) error {
	items, ok := asn1Children(raw.Bytes)
	if !ok || len(items) != 4 || items[0].Tag != asn1.TagSequence || !items[0].IsCompound || items[1].Tag != asn1.TagOctetString || items[2].Tag != asn1.TagOID || items[3].Tag != asn1.TagBitString {
		return fmt.Errorf("unsupported SES v4 seal structure")
	}
	for _, item := range items {
		if item.Class != asn1.ClassUniversal {
			return fmt.Errorf("invalid SES v4 seal tag")
		}
	}
	info, ok := asn1Children(items[0].Bytes)
	if !ok || len(info) < 4 || len(info) > 5 {
		return fmt.Errorf("invalid SES v4 seal info")
	}
	for _, item := range info {
		if item.Class != asn1.ClassUniversal {
			return fmt.Errorf("invalid SES v4 seal info tag")
		}
	}
	if info[0].Tag != asn1.TagSequence || !info[0].IsCompound || info[2].Tag != asn1.TagSequence || !info[2].IsCompound || info[3].Tag != asn1.TagSequence || !info[3].IsCompound {
		return fmt.Errorf("invalid SES v4 seal info structure")
	}
	header, ok := asn1Children(info[0].Bytes)
	if !ok || len(header) != 3 {
		return fmt.Errorf("invalid SES v4 seal header")
	}
	for _, item := range header {
		if item.Class != asn1.ClassUniversal {
			return fmt.Errorf("invalid SES v4 seal header tag")
		}
	}
	headerID, err := signatureWriteASN1String(header[0], asn1.TagIA5String)
	if err != nil || headerID != "ES" {
		return fmt.Errorf("invalid SES v4 seal header ID")
	}
	if header[1].Tag != asn1.TagInteger {
		return fmt.Errorf("invalid SES v4 seal version tag")
	}
	version, err := asn1Integer(header[1])
	if err != nil || version != 4 {
		return fmt.Errorf("only SES v4 seals are supported")
	}
	vendorID, err := signatureWriteASN1String(header[2], asn1.TagIA5String)
	if err != nil || strings.TrimSpace(vendorID) == "" {
		return fmt.Errorf("invalid SES v4 seal vendor ID")
	}
	esID, err := signatureWriteASN1String(info[1], asn1.TagIA5String)
	if err != nil || strings.TrimSpace(esID) == "" {
		return fmt.Errorf("invalid SES v4 seal ID")
	}
	property, ok := asn1Children(info[2].Bytes)
	if !ok || len(property) != 7 {
		return fmt.Errorf("invalid SES v4 seal property")
	}
	for _, item := range property {
		if item.Class != asn1.ClassUniversal {
			return fmt.Errorf("invalid SES v4 seal property tag")
		}
	}
	if property[0].Tag != asn1.TagInteger {
		return fmt.Errorf("invalid SES v4 seal type tag")
	}
	if _, err := asn1Integer(property[0]); err != nil {
		return fmt.Errorf("invalid SES v4 seal type")
	}
	sealName, err := signatureWriteASN1String(property[1], asn1.TagUTF8String)
	if err != nil || strings.TrimSpace(sealName) == "" {
		return fmt.Errorf("invalid SES v4 seal name")
	}
	if property[2].Tag != asn1.TagInteger || property[3].Tag != asn1.TagSequence || !property[3].IsCompound {
		return fmt.Errorf("invalid SES v4 cert list structure")
	}
	certListType, err := asn1Integer(property[2])
	if err != nil || certListType != 1 && certListType != 2 {
		return fmt.Errorf("unsupported SES v4 cert list type")
	}
	certItems, ok := asn1Children(property[3].Bytes)
	if !ok || len(certItems) == 0 {
		return fmt.Errorf("invalid SES v4 cert list")
	}
	for _, item := range certItems {
		if item.Class != asn1.ClassUniversal {
			return fmt.Errorf("invalid SES v4 cert list item tag")
		}
		if certListType == 1 {
			if item.Tag != asn1.TagOctetString {
				return fmt.Errorf("invalid SES v4 cert item tag")
			}
			certBytes, err := asn1OctetString(item)
			if err != nil || len(certBytes) == 0 {
				return fmt.Errorf("invalid SES v4 cert item")
			}
			continue
		}
		if item.Tag != asn1.TagSequence || !item.IsCompound {
			return fmt.Errorf("invalid SES v4 cert digest item")
		}
		fields, ok := asn1Children(item.Bytes)
		if !ok || len(fields) != 2 || fields[0].Class != asn1.ClassUniversal || fields[1].Class != asn1.ClassUniversal || fields[1].Tag != asn1.TagOctetString {
			return fmt.Errorf("invalid SES v4 cert digest fields")
		}
		if fields[0].Tag != asn1.TagPrintableString && fields[0].Tag != asn1.TagIA5String && fields[0].Tag != asn1.TagUTF8String {
			return fmt.Errorf("invalid SES v4 cert digest method tag")
		}
		method := strings.TrimSpace(asn1String(fields[0]))
		if !signatureWriteStrongDigest(method) {
			return fmt.Errorf("weak or unsupported SES v4 cert digest algorithm: %s", method)
		}
		digestBytes, err := asn1OctetString(fields[1])
		if err != nil || len(digestBytes) == 0 {
			return fmt.Errorf("invalid SES v4 cert digest value")
		}
	}
	for _, timeNode := range property[4:7] {
		if timeNode.Tag != asn1.TagGeneralizedTime {
			return fmt.Errorf("SES v4 seal times must use GeneralizedTime")
		}
		if t, err := asn1Time(timeNode); err != nil || t.IsZero() {
			return fmt.Errorf("invalid SES v4 seal time")
		}
	}
	picture, ok := asn1Children(info[3].Bytes)
	if !ok || len(picture) != 4 {
		return fmt.Errorf("invalid SES v4 picture structure")
	}
	for _, item := range picture {
		if item.Class != asn1.ClassUniversal {
			return fmt.Errorf("invalid SES v4 picture tag")
		}
	}
	picType, err := signatureWriteASN1String(picture[0], asn1.TagIA5String)
	if err != nil || strings.TrimSpace(picType) == "" || picture[1].Tag != asn1.TagOctetString || picture[2].Tag != asn1.TagInteger || picture[3].Tag != asn1.TagInteger {
		return fmt.Errorf("invalid SES v4 picture fields")
	}
	w, err := asn1Integer(picture[2])
	if err != nil || w <= 0 {
		return fmt.Errorf("invalid SES v4 picture width")
	}
	h, err := asn1Integer(picture[3])
	if err != nil || h <= 0 {
		return fmt.Errorf("invalid SES v4 picture height")
	}
	if len(info) == 5 {
		if info[4].Tag != asn1.TagSequence || !info[4].IsCompound {
			return fmt.Errorf("invalid SES v4 extDatas structure")
		}
		exts, ok := asn1Children(info[4].Bytes)
		if !ok {
			return fmt.Errorf("invalid SES v4 extDatas")
		}
		for _, ext := range exts {
			if ext.Class != asn1.ClassUniversal || ext.Tag != asn1.TagSequence || !ext.IsCompound {
				return fmt.Errorf("invalid SES v4 extData item")
			}
			fields, ok := asn1Children(ext.Bytes)
			if !ok || len(fields) < 2 || len(fields) > 3 {
				return fmt.Errorf("invalid SES v4 extData fields")
			}
			for _, field := range fields {
				if field.Class != asn1.ClassUniversal {
					return fmt.Errorf("invalid SES v4 extData field tag")
				}
			}
			if fields[0].Tag != asn1.TagOID {
				return fmt.Errorf("invalid SES v4 extData OID")
			}
			if _, err := asn1OIDString(fields[0]); err != nil {
				return fmt.Errorf("invalid SES v4 extData OID")
			}
			valIdx := 1
			if len(fields) == 3 {
				if fields[1].Tag != asn1.TagBoolean {
					return fmt.Errorf("invalid SES v4 extData critical flag")
				}
				var critical bool
				if rest, err := asn1.Unmarshal(fields[1].FullBytes, &critical); err != nil || len(rest) != 0 {
					return fmt.Errorf("invalid SES v4 extData critical flag")
				}
				valIdx = 2
			}
			if fields[valIdx].Tag != asn1.TagOctetString {
				return fmt.Errorf("invalid SES v4 extData value")
			}
			if _, err := asn1OctetString(fields[valIdx]); err != nil {
				return fmt.Errorf("invalid SES v4 extData value")
			}
		}
	}
	return nil
}

// signatureWriteASN1String 校验通用标签并解析ASN.1字符串
// 入参: raw ASN.1原始节点, tag 期望通用标签
// 返回: string 字符串内容, error 错误信息
func signatureWriteASN1String(raw asn1.RawValue, tag int) (string, error) {
	if raw.Class != asn1.ClassUniversal || raw.Tag != tag {
		return "", fmt.Errorf("invalid ASN.1 string tag")
	}
	var out string
	rest, err := asn1.Unmarshal(raw.FullBytes, &out)
	if err != nil || len(rest) != 0 {
		return "", fmt.Errorf("invalid ASN.1 string")
	}
	return out, nil
}

// signatureWriteStrongDigest 判断摘要算法是否为SM3或SHA-256及以上安全算法
// 入参: method 摘要算法名称或OID
// 返回: bool 是否安全
func signatureWriteStrongDigest(method string) bool {
	if isSM3DigestMethod(method) {
		return true
	}
	h, ok := signatureDigestHash(method)
	return ok && (h == crypto.SHA256 || h == crypto.SHA384 || h == crypto.SHA512 || h == crypto.SHA512_256)
}

// signatureWriteAlgorithm 校验证书、公钥匹配和支持的安全算法
// 入参: signer 签署器, cert 证书
// 返回: string 签名算法OID, string 摘要OID, error 错误信息
func signatureWriteAlgorithm(signer crypto.Signer, cert *smx509.Certificate) (string, string, error) {
	if signer == nil || reflect.ValueOf(signer).Kind() == reflect.Ptr && reflect.ValueOf(signer).IsNil() {
		return "", "", fmt.Errorf("nil signature signer")
	}
	pub := signer.Public()
	match, ok := cert.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !match.Equal(pub) {
		return "", "", fmt.Errorf("signature certificate and private key do not match")
	}
	switch key := pub.(type) {
	case *rsa.PublicKey:
		if key.N == nil || key.N.BitLen() < 2048 || key.E < 65537 {
			return "", "", fmt.Errorf("RSA signature key must be at least 2048 bits with exponent at least 65537")
		}
		return "1.2.840.113549.1.1.11", "2.16.840.1.101.3.4.2.1", nil
	case *ecdsa.PublicKey:
		if sm2.IsSM2PublicKey(key) {
			return signMethodSM2SM3, signDigestSM3, nil
		}
		switch key.Curve {
		case elliptic.P256():
			return "1.2.840.10045.4.3.2", "2.16.840.1.101.3.4.2.1", nil
		case elliptic.P384():
			return "1.2.840.10045.4.3.3", "2.16.840.1.101.3.4.2.2", nil
		case elliptic.P521():
			return "1.2.840.10045.4.3.4", "2.16.840.1.101.3.4.2.3", nil
		default:
			return "", "", fmt.Errorf("unsupported ECDSA signature curve")
		}
	default:
		return "", "", fmt.Errorf("unsupported signature public key")
	}
}

// signatureWriteExpandCerts 展开DER或单/多证书PEM列表并校验每张证书合法性
// 入参: bundles 证书输入列表
// 返回: [][]byte 校验后的DER证书列表, error 错误信息
func signatureWriteExpandCerts(bundles [][]byte) ([][]byte, error) {
	var certs [][]byte
	for _, raw := range bundles {
		if len(bytes.TrimSpace(raw)) == 0 {
			return nil, fmt.Errorf("empty certificate entry")
		}
		if block, rest := pem.Decode(raw); block != nil {
			for block != nil {
				if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
					return nil, fmt.Errorf("invalid certificate PEM")
				}
				parsed, err := parseSignatureCertificate(block.Bytes)
				if err != nil {
					return nil, err
				}
				certs = append(certs, bytes.Clone(parsed.Raw))
				block, rest = pem.Decode(rest)
			}
			if len(bytes.TrimSpace(rest)) != 0 {
				return nil, fmt.Errorf("invalid certificate PEM")
			}
			continue
		}
		parsed, err := parseSignatureCertificate(raw)
		if err != nil {
			return nil, err
		}
		certs = append(certs, bytes.Clone(parsed.Raw))
	}
	return certs, nil
}

// signatureWriteTrust 校验证书用途、有效期和调用方显式指定的信任链
// 入参: cert 证书, at 验证时间, options 签署选项
// 返回: error 错误信息
func signatureWriteTrust(cert *smx509.Certificate, at time.Time, options SignatureWriteOptions) error {
	if cert.KeyUsage != 0 && cert.KeyUsage&(smx509.KeyUsageDigitalSignature|smx509.KeyUsageContentCommitment) == 0 {
		return fmt.Errorf("certificate does not authorize digital signatures")
	}
	if len(options.TrustRoots) == 0 {
		return fmt.Errorf("explicit signature trust roots are required")
	}
	roots, err := signatureWriteExpandCerts(options.TrustRoots)
	if err != nil {
		return fmt.Errorf("invalid trust root certificate: %w", err)
	}
	if len(roots) == 0 {
		return fmt.Errorf("explicit signature trust roots are required")
	}
	intermediates, err := signatureWriteExpandCerts(options.Intermediates)
	if err != nil {
		return fmt.Errorf("invalid intermediate certificate: %w", err)
	}
	return verifySignatureCertificateChain(cert, intermediates, roots, at, smx509.ExtKeyUsageAny)
}

// signatureWriteCMSAttribute 编码CMS/GB/T35275单个认证属性
// 入参: oid 属性对象标识, value 已编码的属性值DER
// 返回: []byte 属性DER编码, error 编码错误
func signatureWriteCMSAttribute(oid asn1.ObjectIdentifier, value []byte) ([]byte, error) {
	return asn1.Marshal(struct {
		Type   asn1.ObjectIdentifier
		Values asn1.RawValue
	}{
		Type:   oid,
		Values: asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSet, IsCompound: true, Bytes: value},
	})
}

// signatureWriteSignedData 编码带认证属性和DER排序证书链的标准detached SignedData消息
// 入参: key 已验证签署身份, data 最终Signature.xml原文, options 签署选项
// 返回: []byte SignedData编码, error 错误信息
func signatureWriteSignedData(key *signatureWriteKey, data []byte, options SignatureWriteOptions) ([]byte, error) {
	type contentInfo struct {
		Type    asn1.ObjectIdentifier
		Content asn1.RawValue
	}
	type encapContentInfo struct {
		Type asn1.ObjectIdentifier
	}
	type signerInfo struct {
		Version         int
		IssuerAndSerial struct {
			Issuer asn1.RawValue
			Serial *big.Int
		}
		Digest    pkix.AlgorithmIdentifier
		AuthAttrs asn1.RawValue
		Algorithm pkix.AlgorithmIdentifier
		Signature []byte
	}
	var outerType, innerType asn1.ObjectIdentifier
	var digestAlg, sigAlg pkix.AlgorithmIdentifier
	switch key.method {
	case signMethodSM2SM3:
		outerType = asn1.ObjectIdentifier{1, 2, 156, 10197, 6, 1, 4, 2, 2}
		innerType = asn1.ObjectIdentifier{1, 2, 156, 10197, 6, 1, 4, 2, 1}
		digestAlg = pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{1, 2, 156, 10197, 1, 401}}
		sigAlg = pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{1, 2, 156, 10197, 1, 301, 1}}
	case "1.2.840.113549.1.1.11":
		outerType = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
		innerType = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
		digestAlg = pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}}
		sigAlg = pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 11}, Parameters: asn1.NullRawValue}
	case "1.2.840.10045.4.3.2":
		outerType = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
		innerType = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
		digestAlg = pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}}
		sigAlg = pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}}
	case "1.2.840.10045.4.3.3":
		outerType = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
		innerType = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
		digestAlg = pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 2}}
		sigAlg = pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 3}}
	case "1.2.840.10045.4.3.4":
		outerType = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
		innerType = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
		digestAlg = pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 3}}
		sigAlg = pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 4}}
	default:
		return nil, fmt.Errorf("unsupported signature method: %s", key.method)
	}
	digest, err := signatureDigest(key.digest, data)
	if err != nil {
		return nil, err
	}
	ctVal, err := asn1.Marshal(innerType)
	if err != nil {
		return nil, err
	}
	ctAttr, err := signatureWriteCMSAttribute(asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}, ctVal)
	if err != nil {
		return nil, err
	}
	utc := options.Time.UTC().Truncate(time.Second)
	var timeVal []byte
	if year := utc.Year(); year >= 1950 && year <= 2049 {
		timeVal = asn1Wrap(asn1.TagUTCTime, []byte(utc.Format("060102150405Z")))
	} else {
		timeVal = asn1Wrap(asn1.TagGeneralizedTime, []byte(utc.Format("20060102150405Z")))
	}
	timeAttr, err := signatureWriteCMSAttribute(asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}, timeVal)
	if err != nil {
		return nil, err
	}
	mdVal, err := asn1.Marshal(digest)
	if err != nil {
		return nil, err
	}
	mdAttr, err := signatureWriteCMSAttribute(asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}, mdVal)
	if err != nil {
		return nil, err
	}
	attrs := [][]byte{ctAttr, timeAttr, mdAttr}
	slices.SortFunc(attrs, bytes.Compare)
	var attrsBytes []byte
	for _, attr := range attrs {
		attrsBytes = append(attrsBytes, attr...)
	}
	signature, err := key.signMessage(asn1SetBytes(attrsBytes), options.Signer)
	if err != nil {
		return nil, err
	}
	expanded, err := signatureWriteExpandCerts(options.Intermediates)
	if err != nil {
		return nil, fmt.Errorf("invalid intermediate certificate: %w", err)
	}
	certs := [][]byte{bytes.Clone(key.cert.Raw)}
	seen := map[string]bool{string(key.cert.Raw): true}
	for _, raw := range expanded {
		if !seen[string(raw)] {
			seen[string(raw)] = true
			certs = append(certs, raw)
		}
	}
	slices.SortFunc(certs, bytes.Compare)
	var certsBytes []byte
	for _, raw := range certs {
		certsBytes = append(certsBytes, raw...)
	}
	signer := signerInfo{
		Version:   1,
		Digest:    digestAlg,
		AuthAttrs: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: attrsBytes},
		Algorithm: sigAlg,
		Signature: signature,
	}
	signer.IssuerAndSerial.Issuer = asn1.RawValue{FullBytes: key.cert.RawIssuer}
	signer.IssuerAndSerial.Serial = key.cert.SerialNumber
	sd, err := asn1.Marshal(struct {
		Version      int
		Digests      []pkix.AlgorithmIdentifier `asn1:"set"`
		Content      encapContentInfo
		Certificates asn1.RawValue
		Signers      []signerInfo `asn1:"set"`
	}{
		Version:      1,
		Digests:      []pkix.AlgorithmIdentifier{digestAlg},
		Content:      encapContentInfo{Type: innerType},
		Certificates: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: certsBytes},
		Signers:      []signerInfo{signer},
	})
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(contentInfo{Type: outerType, Content: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: sd}})
}

// signatureWritePropertyInfo 保证签章属性路径满足ASN.1 IA5String编码要求
// 入参: property 原始包内路径
// 返回: string 纯ASCII路径
func signatureWritePropertyInfo(property string) string {
	ascii := true
	for i := 0; i < len(property); i++ {
		if property[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return property
	}
	parts := strings.Split(property, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// sign 编码数字签名或SES v4签章
// 入参: data 最终Signature.xml原文, property 包内绝对路径, options 签署选项
// 返回: []byte 签名值, error 错误信息
func (key *signatureWriteKey) sign(data []byte, property string, options SignatureWriteOptions) ([]byte, error) {
	if len(options.Seal) == 0 {
		return signatureWriteSignedData(key, data, options)
	}
	digest := sm3.Sum(data)
	tbs, err := asn1.Marshal(struct {
		Version  int
		Seal     asn1.RawValue
		Time     time.Time `asn1:"generalized"`
		Hash     asn1.BitString
		Property string `asn1:"ia5"`
	}{4, asn1.RawValue{FullBytes: options.Seal}, options.Time, asn1.BitString{Bytes: digest[:], BitLength: 256}, signatureWritePropertyInfo(property)})
	if err != nil {
		return nil, err
	}
	signature, err := key.signMessage(tbs, options.Signer)
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(struct {
		TBS         asn1.RawValue
		Certificate []byte
		Algorithm   asn1.ObjectIdentifier
		Signature   asn1.BitString
	}{asn1.RawValue{FullBytes: tbs}, key.cert.Raw, asn1.ObjectIdentifier{1, 2, 156, 10197, 1, 501}, asn1.BitString{Bytes: signature, BitLength: len(signature) * 8}})
}

// validateSM2DERSignature 校验SM2签名值为合法规范的ASN.1 DER编码且分量位于曲线阶内
// 入参: sig 签名值字节
// 返回: error 校验错误
func validateSM2DERSignature(sig []byte) error {
	var rs struct {
		R, S *big.Int
	}
	rest, err := asn1.Unmarshal(sig, &rs)
	if err != nil || len(rest) != 0 || rs.R == nil || rs.S == nil || rs.R.Sign() <= 0 || rs.S.Sign() <= 0 {
		return fmt.Errorf("invalid SM2 ASN.1 DER signature")
	}
	order := sm2.P256().Params().N
	if rs.R.Cmp(order) >= 0 || rs.S.Cmp(order) >= 0 {
		return fmt.Errorf("SM2 signature scalar out of range")
	}
	canonical, err := asn1.Marshal(rs)
	if err != nil || !bytes.Equal(canonical, sig) {
		return fmt.Errorf("non-canonical SM2 ASN.1 DER signature")
	}
	return nil
}

// signMessage 使用签署器对数据签名
// 入参: data 待签名原文, signer 签署器
// 返回: []byte 签名值, error 签名错误
func (key *signatureWriteKey) signMessage(data []byte, signer crypto.Signer) ([]byte, error) {
	if key.method == signMethodSM2SM3 {
		sig, err := signer.Sign(rand.Reader, data, sm2.NewSM2SignerOption(true, nil))
		if err != nil {
			return nil, err
		}
		if err := validateSM2DERSignature(sig); err != nil {
			return nil, err
		}
		return sig, nil
	}
	hashAlg, ok := signatureDigestHash(key.digest)
	if !ok {
		return nil, fmt.Errorf("unsupported digest method: %s", key.digest)
	}
	digest := signatureHashBytes(hashAlg, data)
	if len(digest) == 0 {
		return nil, fmt.Errorf("unsupported digest method: %s", key.digest)
	}
	return signer.Sign(rand.Reader, digest, hashAlg)
}
