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
	"crypto"
	"crypto/ecdsa"
	"crypto/md5"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/asn1"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"hash"
	"io"
	"math/big"
	"path"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emmansun/gmsm/sm2"
	"github.com/emmansun/gmsm/sm3"
	"github.com/emmansun/gmsm/smx509"
)

// signatureMethodReplacer 移除签名算法名称中的分隔符
var signatureMethodReplacer = strings.NewReplacer("-", "", "_", "", " ", "")

// signatureCSTZone 表示无时区签名时间默认使用的CST东八区
var signatureCSTZone = time.FixedZone("CST", 8*3600)

// signerCertTime 记录单个SignerInfo证书及其独立声明的签名时间
type signerCertTime struct {
	cert []byte
	at   time.Time
}

// SignatureVerifyReport 签名验证报告
// Valid表示签名完整性、签章外观位置、签名时间语义及调用方指定的全部策略均通过
// Checked表示对应检查已得出结论，未检查时不能将OK的零值视为失败
// SealCertTimeOK按印章制章时间检查制章证书的有效期状态，存在可信制章时间戳时优先按其检查
type SignatureVerifyReport struct {
	signerCerts          [][]byte
	signerTimes          []signerCertTime
	DocIndex             int
	VersionID            string
	DocRoot              string
	ID                   string
	BaseLoc              string
	Type                 SignType
	Valid                bool
	Error                string
	Signer               string
	SignatureDateTime    string
	SignatureTime        time.Time
	Provider             SignatureProvider
	SealInfo             SignatureSealInfo
	SealType             string
	SignCert             SignatureCertInfo
	SealCert             SignatureCertInfo
	SignatureMethod      string
	DigestMethod         string
	References           []SignatureReferenceVerify
	Stamps               []SignatureStamp
	StampPositions       []SignatureStampPosition
	StampPositionError   string
	DigestOK             bool
	DataHashChecked      bool
	DataHashOK           bool
	SignedValueChecked   bool
	SignedValueOK        bool
	CertChecked          bool
	CertOK               bool
	CertTimeChecked      bool
	CertTimeOK           bool
	CertTrustChecked     bool
	CertTrustOK          bool
	CertTrustError       string
	SignatureTimeChecked bool
	SignatureTimeOK      bool
	SealChecked          bool
	SealOK               bool
	SealMatchChecked     bool
	SealMatchOK          bool
	SealTimeChecked      bool
	SealTimeOK           bool
	SealCertTimeChecked  bool
	SealCertTimeOK       bool
	CoverageChecked      bool
	CoverageOK           bool
	CoverageError        string
	CoveredFiles         []string
	UncoveredFiles       []string
	PolicyChecked        bool
	PolicyOK             bool
	PolicyError          string
	TimestampChecked     bool
	TimestampOK          bool
	Timestamps           []SignatureTimestampReport
	RevocationChecked    bool
	RevocationOK         bool
	Revocations          []SignatureRevocationReport
}

// SignatureCertInfo 签名证书信息
type SignatureCertInfo struct {
	Raw          []byte
	Subject      string
	CommonName   string
	Organization string
	Issuer       string
	SerialNumber string
	NotBefore    time.Time
	NotAfter     time.Time
}

// SignatureSealInfo 电子印章信息
type SignatureSealInfo struct {
	Version    int
	ID         string
	VendorID   string
	Type       int
	Name       string
	CreateTime time.Time
	ValidStart time.Time
	ValidEnd   time.Time
}

// SignatureReferenceVerify 签名保护文件验证结果
type SignatureReferenceVerify struct {
	FileRef    string
	Path       string
	CheckValue []byte
	Actual     []byte
	Checked    bool
	OK         bool
	Error      string
}

// SignatureVerifyOption 签名验证选项函数
type SignatureVerifyOption func(*signatureVerifyOptions)

// signatureVerifyOptions 签名验证选项
type signatureVerifyOptions struct {
	SignCerts    [][]byte
	TrustCerts   [][]byte
	VerifyTime   *time.Time
	Policy       *SignaturePolicy
	Timestamp    *SignatureTimestampOptions
	Revocation   *SignatureRevocationOptions
	DocIndex     int
	VersionID    string
	DocRoot      string
	digestCache  map[[2]string][]byte
	sigFileCache map[string]*SignatureFile
}

// WithSignatureCert 添加数字签名验证证书
// 入参: cert DER或PEM编码证书
// 返回: SignatureVerifyOption 签名验证选项
func WithSignatureCert(cert []byte) SignatureVerifyOption {
	return func(o *signatureVerifyOptions) {
		o.SignCerts = appendSignatureCerts(o.SignCerts, cert)
	}
}

// WithSignatureCerts 添加多张数字签名验证证书
// 入参: certs DER或PEM编码证书列表
// 返回: SignatureVerifyOption 签名验证选项
func WithSignatureCerts(certs ...[]byte) SignatureVerifyOption {
	return func(o *signatureVerifyOptions) {
		o.SignCerts = appendSignatureCerts(o.SignCerts, certs...)
	}
}

// WithSignatureTrustCert 添加签名信任证书
// 指定信任后同时验证证书链有效期，默认使用可信时间戳时间或当前时间
// 入参: cert DER或PEM编码证书
// 返回: SignatureVerifyOption 签名验证选项
func WithSignatureTrustCert(cert []byte) SignatureVerifyOption {
	return func(o *signatureVerifyOptions) {
		o.TrustCerts = appendSignatureCerts(o.TrustCerts, cert)
	}
}

// WithSignatureTrustCerts 添加多张签名信任证书
// 指定信任后同时验证证书链有效期，默认使用可信时间戳时间或当前时间
// 入参: certs DER或PEM编码证书列表
// 返回: SignatureVerifyOption 签名验证选项
func WithSignatureTrustCerts(certs ...[]byte) SignatureVerifyOption {
	return func(o *signatureVerifyOptions) {
		o.TrustCerts = appendSignatureCerts(o.TrustCerts, certs...)
	}
}

// WithSignatureVerifyTime 设置签名证书验证时间
// 入参: t 验证时间
// 返回: SignatureVerifyOption 签名验证选项
func WithSignatureVerifyTime(t time.Time) SignatureVerifyOption {
	return func(o *signatureVerifyOptions) {
		o.VerifyTime = &t
	}
}

// VerifySignatures 验证明文OFD输入源中的签名，不接管输入源生命周期
// 加密文档请先使用WithCredentials通过NewReaderContext打开后再调用Reader验签方法
// 入参: input 输入源, opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 错误信息
func VerifySignatures(input *Input, opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	return VerifySignaturesContext(context.Background(), input, opts...)
}

// VerifySignaturesContext 验证明文OFD输入源中的签名并响应取消，不接管输入源生命周期
// 加密文档请先使用WithCredentials通过NewReaderContext打开后再调用Reader验签方法
// 入参: ctx 取消上下文，nil使用后台上下文, input 输入源, opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 错误信息
func VerifySignaturesContext(ctx context.Context, input *Input, opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reader, err := NewReaderContext(ctx, input)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return reader.VerifySignaturesContext(ctx, opts...)
}

// withSignatureDigestCache 注入单次批量验签共享的引用摘要缓存
// 入参: cache 摘要缓存映射
// 返回: SignatureVerifyOption 内部验签选项
func withSignatureDigestCache(cache map[[2]string][]byte) SignatureVerifyOption {
	return func(o *signatureVerifyOptions) {
		if o.digestCache == nil {
			o.digestCache = cache
		}
	}
}

// appendSignatureCerts 追加签名证书
// 入参: dst 目标证书列表, certs DER或PEM编码证书列表
// 返回: [][]byte 证书列表
func appendSignatureCerts(dst [][]byte, certs ...[]byte) [][]byte {
	for _, cert := range certs {
		dst = append(dst, parseSignatureCerts(cert)...)
	}
	return dst
}

// canonicalSignatureDigestKey 归一化摘要算法缓存键，统一算法名称与OID别名
// 入参: method 摘要算法标识
// 返回: string 规范化算法键, bool 是否支持
func canonicalSignatureDigestKey(method string) (string, bool) {
	if strings.TrimSpace(method) == "" {
		return "MD5", true
	}
	if isSM3DigestMethod(method) {
		return "SM3", true
	}
	h, ok := signatureDigestHash(method)
	if !ok {
		return "", false
	}
	return h.String(), true
}

// parseSignatureFile 解析签名文件，按OFD命名空间过滤并校验关键结构节点唯一性
// 入参: data 签名文件XML数据
// 返回: *SignatureFile 签名结构, error 解析错误
func parseSignatureFile(data []byte) (*SignatureFile, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var root xml.StartElement
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if start, ok := tok.(xml.StartElement); ok {
			root = start
			break
		}
	}
	if root.Name.Local != "Signature" || classifyOFDNamespace(root.Name.Space) == ofdXMLUnknown {
		return nil, fmt.Errorf("unexpected signature root element: %s", root.Name.Local)
	}
	sigFile := SignatureFile{XMLName: root.Name}
	signedInfoCount := 0
	signedValueCount := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch elem := tok.(type) {
		case xml.StartElement:
			if !ofdXMLChild(root.Name, elem.Name) {
				if err := dec.Skip(); err != nil {
					return nil, err
				}
				continue
			}
			elem = ofdXMLStart(elem)
			switch elem.Name.Local {
			case "SignedInfo":
				signedInfoCount++
				if signedInfoCount > 1 {
					return nil, fmt.Errorf("duplicate SignedInfo element")
				}
				if err := decodeSignatureSignedInfo(dec, elem.Name, &sigFile.SignedInfo); err != nil {
					return nil, err
				}
			case "SignedValue":
				signedValueCount++
				if signedValueCount > 1 {
					return nil, fmt.Errorf("duplicate SignedValue element")
				}
				if err := dec.DecodeElement(&sigFile.SignedValue, &elem); err != nil {
					return nil, err
				}
			default:
				if err := dec.Skip(); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			for {
				tail, err := dec.Token()
				if err == io.EOF {
					return &sigFile, nil
				}
				if err != nil {
					return nil, err
				}
				switch v := tail.(type) {
				case xml.CharData:
					if len(bytes.TrimSpace(v)) != 0 {
						return nil, fmt.Errorf("unexpected trailing data in signature file")
					}
				case xml.Comment, xml.ProcInst:
				default:
					return nil, fmt.Errorf("unexpected trailing token in signature file")
				}
			}
		}
	}
}

// decodeSignatureSignedInfo 解析SignedInfo，按OFD命名空间过滤并校验References与Seal节点唯一性
// 入参: dec XML解码器, parent 父节点名称, info 签名信息接收结构
// 返回: error 解析错误
func decodeSignatureSignedInfo(dec *xml.Decoder, parent xml.Name, info *SignedInfo) error {
	refsCount := 0
	sealCount := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch elem := tok.(type) {
		case xml.StartElement:
			if !ofdXMLChild(parent, elem.Name) {
				if err := dec.Skip(); err != nil {
					return err
				}
				continue
			}
			elem = ofdXMLStart(elem)
			switch elem.Name.Local {
			case "Provider":
				if err := dec.DecodeElement(&info.Provider, &elem); err != nil {
					return err
				}
			case "SignatureMethod":
				if err := dec.DecodeElement(&info.SignatureMethod, &elem); err != nil {
					return err
				}
			case "SignatureDateTime":
				if err := dec.DecodeElement(&info.SignatureDateTime, &elem); err != nil {
					return err
				}
			case "References":
				refsCount++
				if refsCount > 1 {
					return fmt.Errorf("duplicate References element")
				}
				if err := dec.DecodeElement(&info.References, &elem); err != nil {
					return err
				}
			case "Seal":
				sealCount++
				if sealCount > 1 {
					return fmt.Errorf("duplicate Seal element")
				}
				if err := dec.DecodeElement(&info.Seal, &elem); err != nil {
					return err
				}
			case "StampAnnot":
				var stamp SignatureStamp
				if err := dec.DecodeElement(&stamp, &elem); err != nil {
					return err
				}
				info.StampAnnot = append(info.StampAnnot, stamp)
			default:
				if err := dec.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			return nil
		}
	}
}

// signatureDigest 计算签名摘要
// 入参: method 摘要算法, data 原文数据
// 返回: []byte 摘要值, error 错误信息
func signatureDigest(method string, data []byte) ([]byte, error) {
	if strings.TrimSpace(method) == "" {
		sum := md5.Sum(data)
		return sum[:], nil
	}
	if isSM3DigestMethod(method) {
		return signSM3(data), nil
	}
	if h, ok := signatureDigestHash(method); ok {
		return signatureHashBytes(h, data), nil
	}
	return nil, fmt.Errorf("unsupported digest method: %s", method)
}

// signatureDigestHash 获取摘要算法
// 入参: method 摘要算法
// 返回: crypto.Hash 摘要算法, bool 是否支持
func signatureDigestHash(method string) (crypto.Hash, bool) {
	switch signatureMethodText(method) {
	case "1.2.840.113549.2.5", "MD5":
		return crypto.MD5, true
	case "1.3.14.3.2.26", "SHA1":
		return crypto.SHA1, true
	case "2.16.840.1.101.3.4.2.4", "SHA224":
		return crypto.SHA224, true
	case "2.16.840.1.101.3.4.2.1", "SHA256":
		return crypto.SHA256, true
	case "2.16.840.1.101.3.4.2.2", "SHA384":
		return crypto.SHA384, true
	case "2.16.840.1.101.3.4.2.3", "SHA512":
		return crypto.SHA512, true
	case "2.16.840.1.101.3.4.2.5", "SHA512224":
		return crypto.SHA512_224, true
	case "2.16.840.1.101.3.4.2.6", "SHA512256":
		return crypto.SHA512_256, true
	default:
		return 0, false
	}
}

// signatureMethodHash 获取签名算法对应摘要算法
// 入参: method 签名算法, digestMethod 摘要算法
// 返回: crypto.Hash 摘要算法, error 错误信息
func signatureMethodHash(method, digestMethod string) (crypto.Hash, error) {
	switch signatureMethodText(method) {
	case "1.2.840.113549.1.1.4", "MD5RSA", "MD5WITHRSA", "RSAMD5":
		return crypto.MD5, nil
	case "1.2.840.113549.1.1.5", "RSASHA1", "SHA1RSA", "SHA1WITHRSA":
		return crypto.SHA1, nil
	case "1.2.840.113549.1.1.14", "RSASHA224", "SHA224RSA", "SHA224WITHRSA":
		return crypto.SHA224, nil
	case "1.2.840.113549.1.1.11", "RSASHA256", "SHA256RSA", "SHA256WITHRSA":
		return crypto.SHA256, nil
	case "1.2.840.113549.1.1.12", "RSASHA384", "SHA384RSA", "SHA384WITHRSA":
		return crypto.SHA384, nil
	case "1.2.840.113549.1.1.13", "RSASHA512", "SHA512RSA", "SHA512WITHRSA":
		return crypto.SHA512, nil
	case "1.2.840.10045.4.1", "ECDSASHA1", "SHA1ECDSA", "SHA1WITHECDSA":
		return crypto.SHA1, nil
	case "1.2.840.10045.4.3.1", "ECDSASHA224", "SHA224ECDSA", "SHA224WITHECDSA":
		return crypto.SHA224, nil
	case "1.2.840.10045.4.3.2", "ECDSASHA256", "SHA256ECDSA", "SHA256WITHECDSA":
		return crypto.SHA256, nil
	case "1.2.840.10045.4.3.3", "ECDSASHA384", "SHA384ECDSA", "SHA384WITHECDSA":
		return crypto.SHA384, nil
	case "1.2.840.10045.4.3.4", "ECDSASHA512", "SHA512ECDSA", "SHA512WITHECDSA":
		return crypto.SHA512, nil
	}
	if h, ok := signatureDigestHash(digestMethod); ok {
		return h, nil
	}
	return 0, fmt.Errorf("unsupported signature method: %s", method)
}

// signatureHashBytes 计算摘要
// 入参: h 摘要算法, data 原文数据
// 返回: []byte 摘要值
func signatureHashBytes(h crypto.Hash, data []byte) []byte {
	switch h {
	case crypto.MD5:
		sum := md5.Sum(data)
		return sum[:]
	case crypto.SHA1:
		sum := sha1.Sum(data)
		return sum[:]
	case crypto.SHA224:
		sum := sha256.Sum224(data)
		return sum[:]
	case crypto.SHA256:
		sum := sha256.Sum256(data)
		return sum[:]
	case crypto.SHA384:
		sum := sha512.Sum384(data)
		return sum[:]
	case crypto.SHA512:
		sum := sha512.Sum512(data)
		return sum[:]
	case crypto.SHA512_224:
		sum := sha512.Sum512_224(data)
		return sum[:]
	case crypto.SHA512_256:
		sum := sha512.Sum512_256(data)
		return sum[:]
	default:
		return nil
	}
}

// isRSASignatureMethod 判断是否为RSA签名算法
// 入参: method 算法标识
// 返回: bool 是否为RSA签名算法
func isRSASignatureMethod(method string) bool {
	switch signatureMethodText(method) {
	case "1.2.840.113549.1.1.1", "1.2.840.113549.1.1.4", "1.2.840.113549.1.1.5", "1.2.840.113549.1.1.10", "1.2.840.113549.1.1.11", "1.2.840.113549.1.1.12", "1.2.840.113549.1.1.13", "1.2.840.113549.1.1.14", "RSA", "RSAMD5", "MD5RSA", "MD5WITHRSA", "RSASHA1", "RSASHA224", "RSASHA256", "RSASHA384", "RSASHA512", "SHA1RSA", "SHA224RSA", "SHA256RSA", "SHA384RSA", "SHA512RSA", "SHA1WITHRSA", "SHA224WITHRSA", "SHA256WITHRSA", "SHA384WITHRSA", "SHA512WITHRSA":
		return true
	default:
		return false
	}
}

// isECDSASignatureMethod 判断是否为ECDSA签名算法
// 入参: method 算法标识
// 返回: bool 是否为ECDSA签名算法
func isECDSASignatureMethod(method string) bool {
	switch signatureMethodText(method) {
	case "1.2.840.10045.2.1", "1.2.840.10045.4.1", "1.2.840.10045.4.3.1", "1.2.840.10045.4.3.2", "1.2.840.10045.4.3.3", "1.2.840.10045.4.3.4", "ECDSA", "ECDSASHA1", "ECDSASHA224", "ECDSASHA256", "ECDSASHA384", "ECDSASHA512", "SHA1ECDSA", "SHA224ECDSA", "SHA256ECDSA", "SHA384ECDSA", "SHA512ECDSA", "SHA1WITHECDSA", "SHA224WITHECDSA", "SHA256WITHECDSA", "SHA384WITHECDSA", "SHA512WITHECDSA":
		return true
	default:
		return false
	}
}

// signatureMethodText 规范化算法标识
// 入参: method 算法标识
// 返回: string 规范化算法标识
func signatureMethodText(method string) string {
	method = strings.TrimSpace(method)
	if len(method) >= len("urn:oid:") && strings.EqualFold(method[:len("urn:oid:")], "urn:oid:") {
		method = method[len("urn:oid:"):]
	}
	if idx := strings.LastIndexAny(method, "#/"); idx >= 0 && idx+1 < len(method) {
		method = method[idx+1:]
	}
	method = strings.ToUpper(method)
	return signatureMethodReplacer.Replace(method)
}

// certMatchesPublicKeyMethod 判断证书公钥类型是否与RSA或ECDSA签名算法匹配
// 入参: cert 证书字节, method 签名算法标识
// 返回: bool 公钥类型是否匹配
func certMatchesPublicKeyMethod(cert []byte, method string) bool {
	x509Cert, err := parseSignatureCertificate(cert)
	if err != nil {
		return false
	}
	switch pub := x509Cert.PublicKey.(type) {
	case *rsa.PublicKey:
		return isRSASignatureMethod(method)
	case *ecdsa.PublicKey:
		return !sm2.IsSM2PublicKey(pub) && isECDSASignatureMethod(method)
	default:
		return false
	}
}

// parseRSAPSSParams 解析RSASSA-PSS算法参数中的摘要算法与盐长
// 入参: algRaw 签名算法AlgorithmIdentifier原始ASN.1节点, digestMethod 签名者摘要算法
// 返回: crypto.Hash 摘要算法, int PSS盐长选项, error 参数错误
func parseRSAPSSParams(algRaw asn1.RawValue, digestMethod string) (crypto.Hash, int, error) {
	h := crypto.SHA1
	if digestMethod != "" {
		if d, ok := signatureDigestHash(digestMethod); ok {
			h = d
		}
	}
	if algRaw.Class != asn1.ClassUniversal || algRaw.Tag != asn1.TagSequence || !algRaw.IsCompound {
		return h, rsa.PSSSaltLengthAuto, nil
	}
	algItems, ok := asn1Children(algRaw.Bytes)
	if !ok || len(algItems) == 0 || len(algItems) > 2 {
		return 0, 0, fmt.Errorf("invalid RSASSA-PSS algorithm identifier")
	}
	if len(algItems) == 1 || algItems[1].Class == asn1.ClassUniversal && algItems[1].Tag == asn1.TagNull {
		return h, rsa.PSSSaltLengthAuto, nil
	}
	if algItems[1].Class != asn1.ClassUniversal || algItems[1].Tag != asn1.TagSequence || !algItems[1].IsCompound {
		return 0, 0, fmt.Errorf("invalid RSASSA-PSS parameters")
	}
	paramItems, ok := asn1Children(algItems[1].Bytes)
	if !ok {
		return 0, 0, fmt.Errorf("invalid RSASSA-PSS parameters")
	}
	pssHash := crypto.SHA1
	mgfHash := crypto.SHA1
	saltLen := rsa.PSSSaltLengthAuto
	previous := -1
	for _, item := range paramItems {
		if item.Class != asn1.ClassContextSpecific || !item.IsCompound || item.Tag < 0 || item.Tag > 3 || item.Tag <= previous {
			return 0, 0, fmt.Errorf("invalid RSASSA-PSS parameter tag")
		}
		previous = item.Tag
		var inner asn1.RawValue
		rest, err := asn1.Unmarshal(item.Bytes, &inner)
		if err != nil || len(rest) != 0 {
			return 0, 0, fmt.Errorf("invalid RSASSA-PSS parameter value")
		}
		switch item.Tag {
		case 0:
			oid, err := parseGBTAlgorithm(inner)
			if err != nil {
				return 0, 0, err
			}
			parsed, ok := signatureDigestHash(oid)
			if !ok {
				return 0, 0, fmt.Errorf("unsupported RSASSA-PSS hash algorithm: %s", oid)
			}
			pssHash = parsed
		case 1:
			mgfItems, ok := asn1Children(inner.Bytes)
			if !ok || inner.Class != asn1.ClassUniversal || inner.Tag != asn1.TagSequence || !inner.IsCompound || len(mgfItems) == 0 || len(mgfItems) > 2 {
				return 0, 0, fmt.Errorf("invalid RSASSA-PSS MGF algorithm")
			}
			mgfOID, err := asn1OIDString(mgfItems[0])
			if err != nil || mgfOID != "1.2.840.113549.1.1.8" {
				return 0, 0, fmt.Errorf("unsupported RSASSA-PSS MGF algorithm")
			}
			if len(mgfItems) == 2 {
				mhOID, err := parseGBTAlgorithm(mgfItems[1])
				if err != nil {
					return 0, 0, err
				}
				parsed, ok := signatureDigestHash(mhOID)
				if !ok {
					return 0, 0, fmt.Errorf("unsupported RSASSA-PSS MGF hash algorithm: %s", mhOID)
				}
				mgfHash = parsed
			}
		case 2:
			salt, err := asn1Integer(inner)
			if err != nil || salt < 0 {
				return 0, 0, fmt.Errorf("invalid RSASSA-PSS salt length")
			}
			if salt > 0 {
				saltLen = salt
			}
		case 3:
			trailer, err := asn1Integer(inner)
			if err != nil || trailer != 1 {
				return 0, 0, fmt.Errorf("unsupported RSASSA-PSS trailer field")
			}
		}
	}
	if mgfHash != pssHash {
		return 0, 0, fmt.Errorf("RSASSA-PSS MGF hash mismatch")
	}
	if digestMethod != "" {
		if d, ok := signatureDigestHash(digestMethod); ok && d != pssHash {
			return 0, 0, fmt.Errorf("RSASSA-PSS hash algorithm mismatch")
		}
	}
	return pssHash, saltLen, nil
}

// verifyRSAPSSSignature 按RSASSA-PSS参数验证RSA-PSS签名
// 入参: algRaw 签名算法AlgorithmIdentifier原始ASN.1节点, digestMethod 摘要算法, cert 证书, signedData 被签名数据, signedValue 签名值
// 返回: bool 是否验证通过, error 错误信息
func verifyRSAPSSSignature(algRaw asn1.RawValue, digestMethod string, cert, signedData, signedValue []byte) (bool, error) {
	h, saltLen, err := parseRSAPSSParams(algRaw, digestMethod)
	if err != nil {
		return false, err
	}
	digest := signatureHashBytes(h, signedData)
	if len(digest) == 0 {
		return false, fmt.Errorf("unsupported digest method")
	}
	x509Cert, err := parseSignatureCertificate(cert)
	if err != nil {
		return false, err
	}
	pub, ok := x509Cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return false, nil
	}
	return rsa.VerifyPSS(pub, h, digest, signedValue, &rsa.PSSOptions{SaltLength: saltLen, Hash: h}) == nil, nil
}

// verifyPublicKeySignature 验证国际公钥签名，拒绝SM2公钥跨算法混用ECDSA
// 入参: method 签名算法, digestMethod 摘要算法, cert 证书, signedData 被签名数据, signedValue 签名值
// 返回: bool 是否验证通过, error 错误信息
func verifyPublicKeySignature(method, digestMethod string, cert, signedData, signedValue []byte) (bool, error) {
	if !isRSASignatureMethod(method) && !isECDSASignatureMethod(method) {
		return false, fmt.Errorf("unsupported signature method: %s", method)
	}
	h, err := signatureMethodHash(method, digestMethod)
	if err != nil {
		return false, err
	}
	digest := signatureHashBytes(h, signedData)
	if len(digest) == 0 {
		return false, fmt.Errorf("unsupported digest method")
	}
	x509Cert, err := parseSignatureCertificate(cert)
	if err != nil {
		return false, err
	}
	switch pub := x509Cert.PublicKey.(type) {
	case *rsa.PublicKey:
		if !isRSASignatureMethod(method) {
			return false, nil
		}
		if signatureMethodText(method) == "1.2.840.113549.1.1.10" {
			return rsa.VerifyPSS(pub, h, digest, signedValue, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthAuto, Hash: h}) == nil, nil
		}
		return rsa.VerifyPKCS1v15(pub, h, digest, signedValue) == nil, nil
	case *ecdsa.PublicKey:
		if sm2.IsSM2PublicKey(pub) {
			return false, nil
		}
		if !isECDSASignatureMethod(method) {
			return false, nil
		}
		return verifyECDSASignature(pub, digest, signedValue), nil
	default:
		return false, fmt.Errorf("unsupported public key algorithm")
	}
}

// verifySM2Signature 验证DER或定长拼接编码的SM2签名值
// 入参: pub 公钥, userID 用户标识, msg 原文, sig 签名值
// 返回: bool 是否验证通过
func verifySM2Signature(pub *ecdsa.PublicKey, userID, msg, sig []byte) bool {
	if sm2.VerifyASN1WithSM2(pub, userID, msg, sig) {
		return true
	}
	if len(sig) != 64 {
		return false
	}
	digest, err := sm2.CalculateSM2Hash(pub, msg, userID)
	if err != nil {
		return false
	}
	return sm2.Verify(pub, digest, new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}

// verifyECDSASignature 验证ECDSA签名
// 入参: pub 公钥, digest 摘要, sig 签名值
// 返回: bool 是否验证通过
func verifyECDSASignature(pub *ecdsa.PublicKey, digest, sig []byte) bool {
	if ecdsa.VerifyASN1(pub, digest, sig) {
		return true
	}
	if len(sig) == 0 || len(sig)%2 != 0 {
		return false
	}
	n := len(sig) / 2
	r := new(big.Int).SetBytes(sig[:n])
	s := new(big.Int).SetBytes(sig[n:])
	return ecdsa.Verify(pub, digest, r, s)
}

// signatureRefPath 解析签名文件引用路径
// 入参: basePath 基准路径, refPath 引用路径
// 返回: string 包内文件路径
func signatureRefPath(basePath, refPath string) string {
	return resolveResourcePath(basePath, "", refPath)
}

// referencesOK 判断保护文件摘要是否全部通过
// 入参: refs 保护文件验证结果
// 返回: bool 是否全部通过
func referencesOK(refs []SignatureReferenceVerify) bool {
	if len(refs) == 0 {
		return false
	}
	for _, ref := range refs {
		if !ref.Checked || !ref.OK || ref.Error != "" {
			return false
		}
	}
	return true
}

// timeInRange 判断时间是否位于闭区间
// 入参: t 待判断时间, start 起始时间, end 结束时间
// 返回: bool 是否位于区间
func timeInRange(t, start, end time.Time) bool {
	return !start.After(end) && !t.Before(start) && !t.After(end)
}

// signatureCertsValidAt 判断证书是否在指定时间有效
// 入参: certs 证书列表, t 验证时间
// 返回: bool 是否有效
func signatureCertsValidAt(certs [][]byte, t time.Time) bool {
	if len(certs) == 0 {
		return false
	}
	for _, cert := range certs {
		info, err := parseSignatureCertificate(cert)
		if err != nil || t.Before(info.NotBefore) || t.After(info.NotAfter) {
			return false
		}
	}
	return true
}

// verifySignatureCertificateTrust 按指定时间验证签署证书到显式信任根的证书链
// 入参: cert 证书, pool 证书池, trusts 信任证书, at 验证时间
// 返回: error 验证错误
func verifySignatureCertificateTrust(cert []byte, pool, trusts [][]byte, at time.Time) error {
	c, err := parseSignatureCertificate(cert)
	if err != nil {
		return err
	}
	if c.KeyUsage != 0 && c.KeyUsage&(smx509.KeyUsageDigitalSignature|smx509.KeyUsageContentCommitment) == 0 {
		return fmt.Errorf("certificate does not authorize digital signatures")
	}
	return verifySignatureCertificateChain(c, pool, trusts, at, smx509.ExtKeyUsageAny)
}

// verifySignatureCertificateChain 使用统一X.509实现验证证书链
// 入参: cert 目标证书, pool 中间证书, trusts 信任根, at 验证时间, usage 扩展用途
// 返回: error 验证错误
func verifySignatureCertificateChain(cert *smx509.Certificate, pool, trusts [][]byte, at time.Time, usage smx509.ExtKeyUsage) error {
	roots, intermediates := smx509.NewCertPool(), smx509.NewCertPool()
	for _, group := range []struct {
		data [][]byte
		pool *smx509.CertPool
	}{{trusts, roots}, {pool, intermediates}} {
		for _, data := range group.data {
			ca, err := parseSignatureCertificate(data)
			if err != nil {
				return err
			}
			addSignatureCertificate(group.pool, ca)
		}
	}
	_, err := cert.Verify(smx509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: at, KeyUsages: []smx509.ExtKeyUsage{usage}})
	return err
}

// addSignatureCertificate 添加证书并按RFC5280排除自颁发中间证书的路径长度
// 入参: pool 证书池, cert 证书
func addSignatureCertificate(pool *smx509.CertPool, cert *smx509.Certificate) {
	if !cert.BasicConstraintsValid || cert.MaxPathLen < 0 {
		pool.AddCert(cert)
		return
	}
	copy := *cert
	copy.MaxPathLen, copy.MaxPathLenZero = -1, false
	pool.AddCertWithConstraint(&copy, func(chain []*smx509.Certificate) error {
		length := 0
		for i, intermediate := range chain {
			if i > 0 && !bytes.Equal(intermediate.RawSubject, intermediate.RawIssuer) {
				length++
			}
		}
		if length > cert.MaxPathLen {
			return smx509.CertificateInvalidError{Cert: cert, Reason: smx509.TooManyIntermediates}
		}
		return nil
	})
}

// verifyCertificateSignature 验证证书签名
// 入参: cert 证书信息, issuerCert 颁发者证书
// 返回: bool 是否验证通过, error 错误信息
func verifyCertificateSignature(cert *smx509.Certificate, issuerCert []byte) (bool, error) {
	issuer, err := parseSignatureCertificate(issuerCert)
	if err != nil {
		return false, err
	}
	err = issuer.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature)
	return err == nil, err
}

// compactSignatureCerts 清理证书列表
// 入参: certs 证书列表
// 返回: [][]byte 清理后的证书列表
func compactSignatureCerts(certs [][]byte) [][]byte {
	out := make([][]byte, 0, len(certs))
	seen := make(map[string]bool)
	for _, cert := range certs {
		if len(cert) == 0 {
			continue
		}
		key := string(cert)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, cert)
	}
	return out
}

// signatureCertInfo 解析签名证书信息
// 入参: data DER编码证书
// 返回: SignatureCertInfo 签名证书信息
func signatureCertInfo(data []byte) SignatureCertInfo {
	cert, err := parseSignatureCertificate(data)
	if err != nil {
		return SignatureCertInfo{}
	}
	return SignatureCertInfo{
		Raw:          bytes.Clone(cert.Raw),
		Subject:      cert.Subject.String(),
		CommonName:   cert.Subject.CommonName,
		Organization: strings.Join(cert.Subject.Organization, ", "),
		Issuer:       cert.Issuer.String(),
		NotBefore:    cert.NotBefore,
		NotAfter:     cert.NotAfter,
		SerialNumber: cert.SerialNumber.String(),
	}
}

// parseSignatureDateTime 解析签名时间，无时区格式按CST东八区解析并拒绝损坏文本
// 入参: value 签名时间文本
// 返回: time.Time 签名时间, error 错误信息
func parseSignatureDateTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999Z0700",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999Z0700",
		"20060102150405.999999999Z07:00",
		"20060102150405.999999999Z0700",
		"20060102150405Z07:00",
		"20060102150405Z",
	} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05.999999999",
		"2006/01/02 15:04:05",
		"2006/01/02 15:04:05.999999999",
		"20060102150405.999999999",
		"20060102150405",
		"2006-01-02",
		"2006/01/02",
		"20060102",
	} {
		if t, err := time.ParseInLocation(layout, value, signatureCSTZone); err == nil {
			return t, nil
		}
	}
	if t, err := asn1Time(asn1.RawValue{FullBytes: asn1Wrap(asn1.TagUTCTime, []byte(value))}); err == nil && !t.IsZero() {
		return t, nil
	}
	if t, err := asn1Time(asn1.RawValue{FullBytes: asn1Wrap(asn1.TagGeneralizedTime, []byte(value))}); err == nil && !t.IsZero() {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid signature datetime: %s", value)
}

// parseSignatureCertificate 解析唯一的PEM或DER证书
// 入参: data 证书数据
// 返回: *smx509.Certificate 证书, error 错误信息
func parseSignatureCertificate(data []byte) (*smx509.Certificate, error) {
	if block, rest := pem.Decode(data); block != nil {
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
			return nil, fmt.Errorf("expected one certificate")
		}
		data = block.Bytes
	}
	return smx509.ParseCertificate(data)
}

// asn1Time 解析ASN.1时间，无时区GeneralizedTime统一按CST东八区解析
// 入参: raw ASN.1原始值
// 返回: time.Time 时间, error 错误信息
func asn1Time(raw asn1.RawValue) (time.Time, error) {
	var node asn1.RawValue
	rest, err := asn1.Unmarshal(raw.FullBytes, &node)
	if err != nil || len(rest) != 0 {
		return time.Time{}, fmt.Errorf("invalid time")
	}
	if node.Class == asn1.ClassUniversal && node.Tag == asn1.TagGeneralizedTime && !node.IsCompound {
		text := string(node.Bytes)
		if !strings.ContainsAny(text, "Z+-") {
			return parseASN1LocalGeneralizedTime(text)
		}
	}
	var t time.Time
	rest, err = asn1.Unmarshal(raw.FullBytes, &t)
	if err != nil || len(rest) != 0 {
		return time.Time{}, fmt.Errorf("invalid time")
	}
	return t, nil
}

// parseASN1LocalGeneralizedTime 按CST东八区解析无时区GeneralizedTime文本
// 入参: text 无时区GeneralizedTime文本
// 返回: time.Time 时间, error 错误信息
func parseASN1LocalGeneralizedTime(text string) (time.Time, error) {
	if len(text) < 14 {
		return time.Time{}, fmt.Errorf("invalid time")
	}
	for i := range 14 {
		if text[i] < '0' || text[i] > '9' {
			return time.Time{}, fmt.Errorf("invalid time")
		}
	}
	if len(text) == 14 {
		return time.ParseInLocation("20060102150405", text, signatureCSTZone)
	}
	if text[14] != '.' || len(text) == 15 {
		return time.Time{}, fmt.Errorf("invalid time")
	}
	for i := 15; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return time.Time{}, fmt.Errorf("invalid time")
		}
	}
	return time.ParseInLocation("20060102150405.999999999", text, signatureCSTZone)
}

// parseSignatureCerts 解析签名验证证书
// 入参: data DER或PEM编码证书
// 返回: [][]byte DER编码证书列表
func parseSignatureCerts(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	var certs [][]byte
	rest := bytes.TrimSpace(data)
	hasPEM := false
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		hasPEM = true
		if block.Type == "CERTIFICATE" {
			certs = append(certs, append([]byte(nil), block.Bytes...))
		}
		rest = next
	}
	if len(certs) != 0 {
		return certs
	}
	if hasPEM {
		return nil
	}
	return [][]byte{append([]byte(nil), data...)}
}

// IntegrityValid 判断签名完整性与外观位置是否有效
// 返回: bool 是否有效
func (report SignatureVerifyReport) IntegrityValid() bool {
	sealOK := report.Type == SignTypeSign && !report.SealChecked || report.SealChecked && report.SealOK
	return report.Error == "" && report.StampPositionError == "" && report.DigestOK && referencesOK(report.References) &&
		(!report.CoverageChecked || report.CoverageOK) &&
		report.DataHashChecked && report.DataHashOK && report.SignedValueChecked && report.SignedValueOK &&
		sealOK && (!report.SealMatchChecked || report.SealMatchOK) && report.CertChecked && report.CertOK
}

// TrustedValid 判断签名是否可信有效
// 返回: bool 签名完整性、时间语义、证书信任及证书有效期是否均验证通过
func (report SignatureVerifyReport) TrustedValid() bool {
	if !report.IntegrityValid() || !report.certificatePolicyOK() || !report.CertTrustChecked || !report.CertTrustOK || !report.CertTimeChecked || !report.CertTimeOK {
		return false
	}
	for _, ts := range report.Timestamps {
		if !ts.Valid || !ts.CertTrustChecked || !ts.CertTrustOK {
			return false
		}
	}
	return true
}

// HasFailure 判断签名验证是否存在错误或失败项
// 返回: bool 是否存在错误或失败
func (report SignatureVerifyReport) HasFailure() bool {
	if report.Error != "" || report.StampPositionError != "" || len(report.References) == 0 {
		return true
	}
	for _, ref := range report.References {
		if ref.Error != "" || ref.Checked && !ref.OK {
			return true
		}
	}
	return report.DataHashChecked && !report.DataHashOK ||
		report.SignedValueChecked && !report.SignedValueOK ||
		report.SealChecked && !report.SealOK ||
		report.SealMatchChecked && !report.SealMatchOK ||
		report.CertChecked && !report.CertOK || !report.certificatePolicyOK()
}

// VerifySignatures 验证文档签名，加密文档需在打开阅读器时提供解密凭据或使用已解密阅读器
// 入参: opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 错误信息
func (r *Reader) VerifySignatures(opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	return r.VerifySignaturesContext(context.Background(), opts...)
}

// VerifySignaturesContext 验证全部文档签名并在读取与流式摘要阶段响应取消
// 加密文档验签前需在打开阅读器时提供解密凭据或使用已解密阅读器
// 入参: ctx 取消上下文，nil使用后台上下文, opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 错误信息
func (r *Reader) VerifySignaturesContext(ctx context.Context, opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.ofd == nil || len(r.ofd.DocBody) == 0 {
		return nil, fmt.Errorf("no docbody found")
	}
	if _, err := r.docContext(ctx); err != nil {
		return nil, err
	}
	cache := make(map[[2]string][]byte)
	withCache := append([]SignatureVerifyOption{withSignatureDigestCache(cache)}, opts...)
	var reports []SignatureVerifyReport
	for i := range r.ofd.DocBody {
		items, err := r.VerifyDocumentSignaturesContext(ctx, i, withCache...)
		reports = append(reports, items...)
		if err != nil {
			return reports, fmt.Errorf("document %d: %w", i, err)
		}
	}
	return reports, nil
}

// VerifyDocumentSignatures 验证指定DocBody的签名且不切换阅读器当前文档
// 加密文档验签前需提供解密凭据或使用已解密阅读器
// 入参: index 从0开始的文档索引, opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 错误信息
func (r *Reader) VerifyDocumentSignatures(index int, opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	return r.VerifyDocumentSignaturesContext(context.Background(), index, opts...)
}

// VerifyDocumentSignaturesContext 验证指定文档签名并在读取和摘要阶段响应取消
// 加密文档验签前需提供解密凭据或使用已解密阅读器
// 入参: ctx 取消上下文，nil使用后台上下文, index 文档索引, opts 验证选项
// 返回: []SignatureVerifyReport 验证报告, error 验证或取消错误
func (r *Reader) VerifyDocumentSignaturesContext(ctx context.Context, index int, opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var selectedVersion *string
	if index == r.documentIndex {
		selectedVersion = r.selectedVersion
	}
	return r.verifyDocumentSignaturesContext(ctx, index, selectedVersion, opts...)
}

// VerifyDocumentVersionSignatures 验证指定文档指定版本的签名且不切换阅读器当前文档
// 加密文档验签前需提供解密凭据或使用已解密阅读器
// 入参: docIndex 从0开始的文档索引, versionID 版本标识，空值选择主入口, opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 错误信息
func (r *Reader) VerifyDocumentVersionSignatures(docIndex int, versionID string, opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	return r.VerifyDocumentVersionSignaturesContext(context.Background(), docIndex, versionID, opts...)
}

// VerifyDocumentVersionSignaturesContext 验证指定文档指定版本的签名并在读取和摘要阶段响应取消
// 加密文档验签前需提供解密凭据或使用已解密阅读器
// 入参: ctx 取消上下文，nil使用后台上下文, docIndex 从0开始的文档索引, versionID 版本标识，空值选择主入口, opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 验证或取消错误
func (r *Reader) VerifyDocumentVersionSignaturesContext(ctx context.Context, docIndex int, versionID string, opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	id := versionID
	return r.verifyDocumentSignaturesContext(ctx, docIndex, &id, opts...)
}

// verifyDocumentSignaturesContext 按指定文档索引与版本选择执行签名验证
// 入参: ctx 取消上下文, index 文档索引, selectedVersion 显式版本选择，nil表示按默认规则选择, opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 验证或取消错误
func (r *Reader) verifyDocumentSignaturesContext(ctx context.Context, index int, selectedVersion *string, opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.ofd == nil || index < 0 || index >= len(r.ofd.DocBody) {
		return nil, fmt.Errorf("document index out of range: %d", index)
	}
	options := signatureVerifyOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	if options.digestCache == nil {
		options.digestCache = make(map[[2]string][]byte)
	}
	if options.sigFileCache == nil {
		options.sigFileCache = make(map[string]*SignatureFile)
	}
	body := r.ofd.DocBody[index]
	var view *Reader
	var docRoot string
	var err error
	sameVersion := r.selectedVersion == nil && selectedVersion == nil || r.selectedVersion != nil && selectedVersion != nil && *r.selectedVersion == *selectedVersion
	if index == r.documentIndex && sameVersion && r.doc != nil && r.fileIndex != nil && r.fileNames != nil {
		if r.doc.Signatures == "" {
			return nil, nil
		}
		snapshot := *r
		docRoot = snapshot.documentRoot
		if docRoot == "" {
			docRoot, err = snapshot.DocumentRootContext(ctx)
			if err != nil {
				return nil, err
			}
		}
		if snapshot.rootDir == "" {
			snapshot.rootDir = path.Dir(docRoot)
		}
		view = &snapshot
	} else {
		view = r.deriveView(nil, index, selectedVersion)
		if r.fileIndex != nil && r.fileNames != nil && r.ofd != nil {
			view.fileIndex, view.fileNames, view.ofd = r.fileIndex, r.fileNames, r.ofd
		} else if err := view.initRootContext(ctx); err != nil {
			return nil, err
		}
		docRoot, err = view.DocumentRootContext(ctx)
		if err != nil {
			return nil, err
		}
		data, err := view.readFileViewContext(ctx, docRoot)
		if err != nil {
			return nil, err
		}
		var doc Document
		if err := xml.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		view.doc, view.rootDir = &doc, path.Dir(docRoot)
		doc.Signatures = view.versionSignaturesContext(ctx, body.Signatures)
		if doc.Signatures == "" {
			return nil, nil
		}
	}
	options.DocIndex, options.DocRoot = index, docRoot
	if view.versionInfo != nil {
		options.VersionID = view.versionInfo.ID
	}
	sigListPath := view.ResPath(view.doc.Signatures)
	data, err := view.readFileViewContext(ctx, sigListPath)
	if err != nil {
		return nil, err
	}
	var signatures Signatures
	if err := xml.Unmarshal(data, &signatures); err != nil {
		return nil, err
	}
	var scope *versionSignatureScope
	if view.versionInfo != nil {
		scope, err = view.buildVersionSignatureScope(ctx, sigListPath, signatures)
		if err != nil {
			return nil, err
		}
	}
	reports := make([]SignatureVerifyReport, 0, len(signatures.List))
	for _, sigRef := range signatures.List {
		if err := ctx.Err(); err != nil {
			return reports, err
		}
		if scope != nil && !view.versionSignatureMatches(ctx, scope, sigRef) {
			continue
		}
		reports = append(reports, view.verifySignature(ctx, sigListPath, &signatures, sigRef, &options))
	}
	return reports, ctx.Err()
}

// applySignatureCertificatePolicy 分别按签署校验时间与印章制章时间验证签署证书和制章证书
// 入参: options 验证选项, signerCerts 签署证书列表, sealCert 制章证书, extraCerts 辅助证书池, trustedTimestamp 可信签名时间戳时间, trustedSealTime 可信制章时间戳时间
func (report *SignatureVerifyReport) applySignatureCertificatePolicy(options *signatureVerifyOptions, signerCerts [][]byte, sealCert []byte, extraCerts [][]byte, trustedTimestamp, trustedSealTime *time.Time) {
	signerCerts = compactSignatureCerts(signerCerts)
	if len(signerCerts) == 0 && len(sealCert) == 0 && report.DataHashChecked && !report.DataHashOK {
		return
	}
	verifyTime := options.VerifyTime
	if verifyTime == nil && trustedTimestamp != nil {
		verifyTime = trustedTimestamp
	}
	if verifyTime == nil && len(options.TrustCerts) != 0 {
		now := time.Now()
		verifyTime = &now
	}
	sealTime := report.SealInfo.CreateTime
	if trustedSealTime != nil {
		sealTime = *trustedSealTime
	}
	if verifyTime != nil {
		report.CertTimeChecked = true
		report.CertTimeOK = signatureCertsValidAt(signerCerts, *verifyTime)
		if len(sealCert) != 0 {
			report.CertTimeOK = report.CertTimeOK && !sealTime.IsZero() && signatureCertsValidAt([][]byte{sealCert}, sealTime)
		}
	}
	if len(options.TrustCerts) != 0 {
		report.CertTrustChecked = true
		report.CertTrustOK = true
		pool := append([][]byte{}, options.TrustCerts...)
		pool = append(pool, options.SignCerts...)
		pool = append(pool, extraCerts...)
		pool = compactSignatureCerts(pool)
		for _, cert := range signerCerts {
			if err := verifySignatureCertificateTrust(cert, pool, options.TrustCerts, *verifyTime); err != nil {
				report.CertTrustOK = false
				report.CertTrustError = err.Error()
				break
			}
		}
		if report.CertTrustOK && len(sealCert) != 0 {
			if sealTime.IsZero() {
				report.CertTrustOK = false
				report.CertTrustError = "seal create time missing"
			} else if err := verifySignatureCertificateTrust(sealCert, pool, options.TrustCerts, sealTime); err != nil {
				report.CertTrustOK = false
				report.CertTrustError = err.Error()
			}
		}
		if len(signerCerts) == 0 {
			report.CertTrustOK = false
			report.CertTrustError = "signature certificate missing"
		}
	}
}

// applySignatureTimePolicy 应用签名时间、印章有效期与制章时间策略，多SignerInfo各自独立校验其SigningTime
// 入参: trustedSealTime 可信印章时间戳
func (report *SignatureVerifyReport) applySignatureTimePolicy(trustedSealTime *time.Time) {
	if len(report.signerTimes) != 0 {
		if !report.SignatureTimeChecked {
			report.SignatureTimeChecked, report.SignatureTimeOK = true, true
		}
		checkedCerts := make(map[string]bool, len(report.signerTimes))
		for _, item := range report.signerTimes {
			checkedCerts[string(item.cert)] = true
			report.SignatureTimeOK = report.SignatureTimeOK && signatureCertsValidAt([][]byte{item.cert}, item.at)
		}
		if !report.SignatureTime.IsZero() {
			for _, cert := range report.signerCerts {
				if !checkedCerts[string(cert)] {
					report.SignatureTimeOK = report.SignatureTimeOK && signatureCertsValidAt([][]byte{cert}, report.SignatureTime)
				}
			}
		}
	} else if !report.SignatureTime.IsZero() {
		if len(report.signerCerts) > 0 {
			if !report.SignatureTimeChecked {
				report.SignatureTimeChecked, report.SignatureTimeOK = true, true
			}
			report.SignatureTimeOK = report.SignatureTimeOK && signatureCertsValidAt(report.signerCerts, report.SignatureTime)
		} else if !report.SignCert.NotBefore.IsZero() && !report.SignCert.NotAfter.IsZero() {
			if !report.SignatureTimeChecked {
				report.SignatureTimeChecked, report.SignatureTimeOK = true, true
			}
			report.SignatureTimeOK = report.SignatureTimeOK && timeInRange(report.SignatureTime, report.SignCert.NotBefore, report.SignCert.NotAfter)
		}
	}
	if len(report.SealCert.Raw) != 0 || !report.SealCert.NotBefore.IsZero() || !report.SealCert.NotAfter.IsZero() {
		sealTime := report.SealInfo.CreateTime
		if trustedSealTime != nil {
			sealTime = *trustedSealTime
		}
		report.SealCertTimeChecked = true
		report.SealCertTimeOK = !sealTime.IsZero() && !report.SealCert.NotBefore.IsZero() && !report.SealCert.NotAfter.IsZero() && timeInRange(sealTime, report.SealCert.NotBefore, report.SealCert.NotAfter)
	}
	if !report.SealInfo.ValidStart.IsZero() || !report.SealInfo.ValidEnd.IsZero() {
		if !report.SignatureTime.IsZero() {
			valid := !report.SealInfo.ValidStart.IsZero() && !report.SealInfo.ValidEnd.IsZero() && timeInRange(report.SignatureTime, report.SealInfo.ValidStart, report.SealInfo.ValidEnd)
			if !report.SealInfo.CreateTime.IsZero() && report.SignatureTime.Before(report.SealInfo.CreateTime) {
				valid = false
			}
			if report.SealTimeChecked {
				report.SealTimeOK = report.SealTimeOK && valid
			} else {
				report.SealTimeChecked = true
				report.SealTimeOK = valid
			}
		} else if !report.SealTimeChecked {
			report.SealTimeChecked = true
			report.SealTimeOK = false
		}
	}
}

// certificatePolicyOK 判断证书策略是否通过
// 返回: bool 是否通过
func (report SignatureVerifyReport) certificatePolicyOK() bool {
	if report.PolicyChecked && !report.PolicyOK || report.CoverageChecked && !report.CoverageOK ||
		report.TimestampChecked && !report.TimestampOK || report.RevocationChecked && !report.RevocationOK {
		return false
	}
	if report.SignatureTimeChecked && !report.SignatureTimeOK {
		return false
	}
	if report.SealCertTimeChecked && !report.SealCertTimeOK {
		return false
	}
	if report.SealTimeChecked && !report.SealTimeOK {
		return false
	}
	if report.CertTimeChecked && !report.CertTimeOK {
		return false
	}
	if report.CertTrustChecked && !report.CertTrustOK {
		return false
	}
	return true
}

// verifySignature 验证单个签名
// 入参: ctx 取消上下文, sigListPath 签名列表路径, sigList 已解析签名列表, sigRef 签名引用, options 验证选项
// 返回: SignatureVerifyReport 签名验证报告
func (r *Reader) verifySignature(ctx context.Context, sigListPath string, sigList *Signatures, sigRef Signature, options *signatureVerifyOptions) SignatureVerifyReport {
	sigPath := signatureRefPath(sigListPath, sigRef.BaseLoc)
	report := SignatureVerifyReport{
		DocIndex:  options.DocIndex,
		VersionID: options.VersionID,
		DocRoot:   options.DocRoot,
		ID:        sigRef.ID,
		BaseLoc:   sigRef.BaseLoc,
		Type:      normalizeSignType(sigRef.Type),
	}
	if !validPackagePath(sigPath) {
		report.Error = fmt.Sprintf("invalid signature path: %s", sigRef.BaseLoc)
		return report
	}
	sigData, err := r.readFileViewContext(ctx, sigPath)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	sigFile, err := parseSignatureFile(sigData)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	if options.sigFileCache != nil {
		options.sigFileCache[cleanPackagePath(sigPath)] = sigFile
	}
	report.Provider = sigFile.SignedInfo.Provider
	report.SignatureMethod = strings.TrimSpace(sigFile.SignedInfo.SignatureMethod)
	report.SignatureDateTime = sigFile.SignedInfo.SignatureDateTime
	report.DigestMethod = sigFile.SignedInfo.References.CheckMethod
	if report.DigestMethod == "" {
		report.DigestMethod = "MD5"
	}
	report.References = r.verifySignatureReferences(ctx, sigPath, sigFile.SignedInfo.References, options.digestCache)
	if err := ctx.Err(); err != nil {
		report.Error = err.Error()
		return report
	}
	report.Stamps = append(report.Stamps, sigFile.SignedInfo.StampAnnot...)
	report.StampPositions, err = r.SignatureStampPositions(report.Stamps)
	if err != nil {
		report.StampPositionError = err.Error()
	}
	report.DigestOK = referencesOK(report.References)
	r.applySignatureCoverage(ctx, &report, sigListPath, sigList, sigFile, options)
	report.applySignaturePolicy(options, r.ofd.Version, 0, report.SignatureMethod, report.DigestMethod)
	signedValuePath := signatureRefPath(sigPath, sigFile.SignedValue)
	if !validPackagePath(signedValuePath) {
		report.Error = fmt.Sprintf("invalid signed value path: %s", sigFile.SignedValue)
		return report
	}
	signedValue, err := r.readFileViewContext(ctx, signedValuePath)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	switch report.Type {
	case SignTypeSign:
		if strings.TrimSpace(sigFile.SignedInfo.Seal.BaseLoc) != "" {
			report.SealChecked = true
			report.SealOK = false
			report.Error = "digital signature cannot contain seal"
			return report
		}
		result, err := verifyDigitalSignature(report.SignatureMethod, report.DigestMethod, signedValue, sigData, options)
		if result != nil {
			if report.SignatureMethod == "" {
				report.SignatureMethod = strings.TrimSpace(result.SignatureMethod)
				if report.SignatureMethod != "" {
					report.applySignaturePolicy(options, r.ofd.Version, 0, report.SignatureMethod, report.DigestMethod)
				}
			}
			report.signerCerts = result.SignerCerts
			report.signerTimes = result.SignerTimes
			report.DataHashChecked = result.DataHashChecked
			report.DataHashOK = result.DataHashOK
			report.SignedValueChecked = result.SignedChecked
			report.SignedValueOK = result.SignedOK
			report.CertChecked = result.CertChecked
			report.CertOK = result.CertOK
			report.SignCert = result.CertInfo
			report.Signer = result.CertInfo.CommonName
			if !result.SignatureTime.IsZero() {
				report.SignatureTime = result.SignatureTime
			}
		}
		if report.SignatureMethod == "" && options.Policy != nil && len(options.Policy.SignatureMethods) != 0 {
			report.applySignaturePolicyError(rejectSignaturePolicy("signature algorithm rejected: %s", report.SignatureMethod))
		}
		if err != nil {
			report.applySignaturePolicyError(err)
			report.Error = err.Error()
			return report
		}
		if strings.TrimSpace(report.SignatureDateTime) != "" {
			sigTime, timeErr := parseSignatureDateTime(report.SignatureDateTime)
			if timeErr != nil {
				report.SignatureTimeChecked = true
				report.SignatureTimeOK = false
				report.Error = timeErr.Error()
				return report
			}
			if result.SignatureTime.IsZero() {
				report.SignatureTime = sigTime
			}
		}
		trustedTime, _ := report.applySignatureTimestamps(ctx, options, signedValue, result.Timestamps)
		report.applySignatureTimePolicy(nil)
		report.applySignatureCertificatePolicy(options, result.SignerCerts, nil, result.Certs, trustedTime, nil)
		report.applySignatureRevocation(ctx, options, result.SignerCerts, nil, result.Certs, trustedTime, nil)
		report.Valid = report.IntegrityValid() && report.certificatePolicyOK()
		return report
	case SignTypeSeal:
	default:
		report.Error = fmt.Sprintf("unsupported signature type: %s", report.Type)
		return report
	}
	sesResult, err := verifySESSignature(signedValue, sigData, options)
	if sesResult != nil {
		if report.SignatureMethod == "" {
			report.SignatureMethod = strings.TrimSpace(sesResult.SignatureMethod)
		}
		if len(sesResult.SignCertRaw) != 0 {
			report.signerCerts = [][]byte{sesResult.SignCertRaw}
		}
		report.DataHashChecked = sesResult.DataHashChecked
		report.DataHashOK = sesResult.DataHashOK
		report.SignedValueChecked = sesResult.SignedChecked
		report.SignedValueOK = sesResult.SignedOK
		report.SealChecked = sesResult.SealChecked
		report.SealOK = sesResult.SealOK
		report.CertChecked = sesResult.CertChecked
		report.CertOK = sesResult.CertOK
		report.SignCert = sesResult.SignCert
		report.SealCert = sesResult.SealCert
		report.SealInfo = sesResult.SealInfo
		report.SealType = sesResult.SealType
		report.SignatureTime = sesResult.SignatureTime
		report.Signer = sesResult.SignCert.CommonName
	}
	if err != nil {
		if strings.Contains(err.Error(), "signature time") {
			report.SignatureTimeChecked = true
			report.SignatureTimeOK = false
		}
		report.applySignaturePolicyError(err)
		report.Error = err.Error()
		return report
	}
	if report.SignatureMethod != "" && !signatureAlgorithmEquivalent(report.SignatureMethod, sesResult.SignatureMethod, "SM3") {
		report.Error = "signature method does not match SES"
		return report
	}
	var dateTimeErr error
	if strings.TrimSpace(report.SignatureDateTime) != "" {
		var parsedTime time.Time
		parsedTime, dateTimeErr = parseSignatureDateTime(report.SignatureDateTime)
		if dateTimeErr == nil && report.SignatureTime.IsZero() {
			report.SignatureTime = parsedTime
		}
	}
	report.applySignaturePolicy(options, r.ofd.Version, sesResult.SealInfo.Version, sesResult.SignatureMethod, "SM3")
	trustedTime, trustedSealTime := report.applySignatureTimestamps(ctx, options, signedValue, sesResult.Timestamps)
	report.applySignatureTimePolicy(trustedSealTime)
	if dateTimeErr != nil {
		report.SignatureTimeChecked = true
		report.SignatureTimeOK = false
	}
	var signCerts [][]byte
	if len(sesResult.SignCertRaw) != 0 {
		signCerts = [][]byte{sesResult.SignCertRaw}
	}
	report.applySignatureCertificatePolicy(options, signCerts, sesResult.SealCertRaw, sesResult.Certs, trustedTime, trustedSealTime)
	report.applySignatureRevocation(ctx, options, signCerts, sesResult.SealCertRaw, sesResult.Certs, trustedTime, trustedSealTime)
	if strings.TrimSpace(sigFile.SignedInfo.Seal.BaseLoc) != "" {
		sealPath := signatureRefPath(sigPath, sigFile.SignedInfo.Seal.BaseLoc)
		if !validPackagePath(sealPath) {
			report.Error = fmt.Sprintf("invalid seal path: %s", sigFile.SignedInfo.Seal.BaseLoc)
			return report
		}
		sealData, err := r.readFileViewContext(ctx, sealPath)
		if err != nil {
			report.Error = err.Error()
			return report
		}
		report.SealMatchChecked = true
		report.SealMatchOK = bytes.Equal(sealData, sesResult.SealRaw)
	}
	if dateTimeErr != nil && report.Error == "" {
		report.Error = dateTimeErr.Error()
	}
	report.Valid = report.IntegrityValid() && report.certificatePolicyOK()
	return report
}

// verifySignatureReferences 验证签名保护文件列表，检测重复引用并在取消时保留全部条目
// 入参: ctx 取消上下文, sigPath 签名文件路径, refs 签名保护文件列表, cache 共享摘要缓存
// 返回: []SignatureReferenceVerify 保护文件验证结果
func (r *Reader) verifySignatureReferences(ctx context.Context, sigPath string, refs SignatureReferences, cache map[[2]string][]byte) []SignatureReferenceVerify {
	count := len(refs.Reference)
	results := make([]SignatureReferenceVerify, count)
	counts := make(map[string]int, count)
	cleanPaths := make([]string, count)
	for i, ref := range refs.Reference {
		refPath := signatureRefPath(sigPath, ref.FileRef)
		results[i] = SignatureReferenceVerify{FileRef: ref.FileRef, Path: refPath}
		if validPackagePath(refPath) {
			clean := cleanPackagePath(refPath)
			cleanPaths[i] = clean
			counts[clean]++
		}
	}
	pending := make([]int, 0, count)
	for i := range count {
		if cleanPaths[i] != "" && counts[cleanPaths[i]] > 1 {
			results[i].Error = "duplicate signature reference"
			continue
		}
		pending = append(pending, i)
	}
	workers := min(runtime.GOMAXPROCS(0), 4)
	if r.resources != nil && r.resources.options.Parallelism > 0 && r.resources.options.Parallelism < workers {
		workers = r.resources.options.Parallelism
	}
	if workers <= 1 || len(pending) < 4 {
		for _, idx := range pending {
			if err := ctx.Err(); err != nil {
				results[idx].Error = err.Error()
				continue
			}
			results[idx] = r.verifySignatureReference(ctx, sigPath, refs.CheckMethod, refs.Reference[idx], cache, nil)
		}
		return results
	}
	if workers > len(pending) {
		workers = len(pending)
	}
	completed := make([]bool, len(pending))
	var cacheMu sync.Mutex
	var next atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for {
				pos := int(next.Add(1) - 1)
				if pos >= len(pending) || ctx.Err() != nil {
					return
				}
				idx := pending[pos]
				results[idx] = r.verifySignatureReference(ctx, sigPath, refs.CheckMethod, refs.Reference[idx], cache, &cacheMu)
				completed[pos] = true
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		for pos, idx := range pending {
			if !completed[pos] {
				results[idx].Error = err.Error()
			}
		}
	}
	return results
}

// verifySignatureReference 验证签名保护文件并在单次会话内复用流式哈希结果
// 入参: ctx 取消上下文, sigPath 签名文件路径, method 摘要算法, ref 保护文件引用, cache 共享摘要缓存, cacheMu 缓存互斥锁
// 返回: SignatureReferenceVerify 保护文件验证结果
func (r *Reader) verifySignatureReference(ctx context.Context, sigPath, method string, ref SignatureReference, cache map[[2]string][]byte, cacheMu *sync.Mutex) SignatureReferenceVerify {
	refPath := signatureRefPath(sigPath, ref.FileRef)
	result := SignatureReferenceVerify{
		FileRef: ref.FileRef,
		Path:    refPath,
	}
	if !validPackagePath(refPath) {
		result.Error = fmt.Sprintf("invalid reference path: %s", ref.FileRef)
		return result
	}
	checkValue, err := base64.StdEncoding.DecodeString(strings.TrimSpace(ref.CheckValue))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	algKey, ok := canonicalSignatureDigestKey(method)
	if !ok {
		result.Error = fmt.Sprintf("unsupported digest method: %s", method)
		return result
	}
	cleanPath := cleanPackagePath(refPath)
	cacheKey := [2]string{cleanPath, algKey}
	var actual []byte
	if cache != nil {
		if cacheMu != nil {
			cacheMu.Lock()
		}
		if cached, found := cache[cacheKey]; found {
			actual = bytes.Clone(cached)
		}
		if cacheMu != nil {
			cacheMu.Unlock()
		}
	}
	if actual == nil {
		actual, err = r.streamSignatureDigest(ctx, method, refPath)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		if cache != nil {
			cloned := bytes.Clone(actual)
			if cacheMu != nil {
				cacheMu.Lock()
			}
			cache[cacheKey] = cloned
			if cacheMu != nil {
				cacheMu.Unlock()
			}
		}
	}
	result.CheckValue = checkValue
	result.Actual = actual
	result.Checked = true
	result.OK = subtle.ConstantTimeCompare(checkValue, actual) == 1
	return result
}

// streamSignatureDigest 流式计算包内文件摘要，避免大文件整块驻留内存
// 入参: ctx 取消上下文, method 摘要算法, refPath 包内路径
// 返回: []byte 摘要值, error 读取、不支持算法或取消错误
func (r *Reader) streamSignatureDigest(ctx context.Context, method, refPath string) ([]byte, error) {
	var digest hash.Hash
	switch {
	case strings.TrimSpace(method) == "":
		digest = md5.New()
	case isSM3DigestMethod(method):
		digest = sm3.New()
	default:
		algorithm, ok := signatureDigestHash(method)
		if !ok {
			return nil, fmt.Errorf("unsupported digest method: %s", method)
		}
		digest = algorithm.New()
	}
	input, err := r.openFile(refPath)
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(digest, imageInput{ReadCloser: input, ctx: ctx})
	closeErr := input.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return digest.Sum(nil), nil
}
