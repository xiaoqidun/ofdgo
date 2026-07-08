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
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"io"
	"math/big"
	"path"
	"strings"
	"time"
)

// SignatureVerifyReport 签名验证报告
type SignatureVerifyReport struct {
	ID                 string
	BaseLoc            string
	Type               SignType
	Provider           SignatureProvider
	Signer             string
	SignCert           SignatureCertInfo
	SealCert           SignatureCertInfo
	SealType           string
	SignatureMethod    string
	SignatureDateTime  string
	DigestMethod       string
	References         []SignatureReferenceVerify
	Stamps             []SignatureStamp
	StampPositions     []SignatureStampPosition
	StampPositionError string
	DigestOK           bool
	DataHashOK         bool
	SignedValueOK      bool
	SealOK             bool
	SealMatchOK        bool
	CertOK             bool
	CertTimeChecked    bool
	CertTimeOK         bool
	CertTrustChecked   bool
	CertTrustOK        bool
	Valid              bool
	Error              string
}

// SignatureCertInfo 签名证书信息
type SignatureCertInfo struct {
	Subject      string
	CommonName   string
	Organization string
	Issuer       string
	SerialNumber string
	NotBefore    time.Time
	NotAfter     time.Time
}

// SignatureReferenceVerify 签名保护文件验证结果
type SignatureReferenceVerify struct {
	FileRef    string
	Path       string
	CheckValue []byte
	Actual     []byte
	OK         bool
	Error      string
}

// signatureVerifyOptions 签名验证选项
type signatureVerifyOptions struct {
	SignCerts  [][]byte
	TrustCerts [][]byte
	VerifyTime *time.Time
}

var signatureMethodReplacer = strings.NewReplacer("-", "", "_", "", " ", "")

// SignatureVerifyOption 签名验证选项函数
type SignatureVerifyOption func(*signatureVerifyOptions)

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
// 入参: cert DER或PEM编码证书
// 返回: SignatureVerifyOption 签名验证选项
func WithSignatureTrustCert(cert []byte) SignatureVerifyOption {
	return func(o *signatureVerifyOptions) {
		o.TrustCerts = appendSignatureCerts(o.TrustCerts, cert)
	}
}

// WithSignatureTrustCerts 添加多张签名信任证书
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

// appendSignatureCerts 追加签名证书
// 入参: dst 目标证书列表, certs DER或PEM编码证书列表
// 返回: [][]byte 证书列表
func appendSignatureCerts(dst [][]byte, certs ...[]byte) [][]byte {
	for _, cert := range certs {
		dst = append(dst, parseSignatureCerts(cert)...)
	}
	return dst
}

// VerifySignaturesBytes 验证OFD字节数据签名
// 入参: data OFD字节数据, opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 错误信息
func VerifySignaturesBytes(data []byte, opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	return VerifySignaturesReader(bytes.NewReader(data), int64(len(data)), opts...)
}

// VerifySignaturesStream 验证OFD顺序流签名
// 入参: r IO顺序读取器, opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 错误信息
func VerifySignaturesStream(r io.Reader, opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return VerifySignaturesBytes(data, opts...)
}

// VerifySignaturesReader 验证OFD读取器签名
// 入参: r IO读取器, size 数据大小, opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 错误信息
func VerifySignaturesReader(r io.ReaderAt, size int64, opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	reader, err := NewReader(r, size)
	if err != nil {
		return nil, err
	}
	return reader.VerifySignatures(opts...)
}

// VerifySignatures 验证文档签名
// 入参: opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 错误信息
func (r *Reader) VerifySignatures(opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	options := signatureVerifyOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	if doc.Signatures == "" {
		return nil, nil
	}
	sigListPath := r.ResPath(doc.Signatures)
	data, err := r.readFileExact(sigListPath)
	if err != nil {
		return nil, err
	}
	var signatures Signatures
	if err := xml.Unmarshal(data, &signatures); err != nil {
		return nil, err
	}
	reports := make([]SignatureVerifyReport, 0, len(signatures.List))
	for _, sigRef := range signatures.List {
		reports = append(reports, r.verifySignature(sigListPath, sigRef, &options))
	}
	return reports, nil
}

// verifySignature 验证单个签名
// 入参: sigListPath 签名列表路径, sigRef 签名引用, options 验证选项
// 返回: SignatureVerifyReport 签名验证报告
func (r *Reader) verifySignature(sigListPath string, sigRef Signature, options *signatureVerifyOptions) SignatureVerifyReport {
	sigPath := signatureRefPath(sigListPath, sigRef.BaseLoc)
	report := SignatureVerifyReport{
		ID:          sigRef.ID,
		BaseLoc:     sigRef.BaseLoc,
		Type:        sigRef.Type,
		SealMatchOK: true,
	}
	sigData, err := r.readFileExact(sigPath)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	sigFile, err := parseSignatureFile(sigData)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	report.Provider = sigFile.SignedInfo.Provider
	report.SignatureMethod = sigFile.SignedInfo.SignatureMethod
	report.SignatureDateTime = sigFile.SignedInfo.SignatureDateTime
	report.DigestMethod = sigFile.SignedInfo.References.CheckMethod
	report.References = r.verifySignatureReferences(sigPath, sigFile.SignedInfo.References)
	report.Stamps = append(report.Stamps, sigFile.SignedInfo.StampAnnot...)
	report.StampPositions, err = r.SignatureStampPositions(report.Stamps)
	if err != nil {
		report.StampPositionError = err.Error()
	}
	report.DigestOK = referencesOK(report.References)
	signedValuePath := signatureRefPath(sigPath, sigFile.SignedValue)
	signedValue, err := r.readFileExact(signedValuePath)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	switch sigRef.Type {
	case SignTypeSign:
		result, err := verifyDigitalSignature(report.SignatureMethod, report.DigestMethod, signedValue, sigData, options)
		if err != nil {
			report.Error = err.Error()
			return report
		}
		report.DataHashOK = result.DataHashOK
		report.SignedValueOK = result.SignedOK
		report.SealOK = true
		report.CertOK = result.CertOK
		report.SignCert = result.CertInfo
		report.Signer = result.CertInfo.CommonName
		report.applySignatureCertificatePolicy(options, result.SignerCerts, result.Certs)
		report.Valid = report.DigestOK && report.DataHashOK && report.SignedValueOK && report.CertOK && report.certificatePolicyOK()
		return report
	case "", SignTypeSeal:
	default:
		report.Error = fmt.Sprintf("unsupported signature type: %s", sigRef.Type)
		return report
	}
	sesResult, err := verifySESSignature(signedValue, sigData, options)
	if sesResult != nil {
		report.SignCert = sesResult.SignCert
		report.SealCert = sesResult.SealCert
		report.SealType = sesResult.SealType
		report.Signer = sesResult.SignCert.CommonName
	}
	if err != nil {
		report.Error = err.Error()
		return report
	}
	report.DataHashOK = sesResult.DataHashOK
	report.SignedValueOK = sesResult.SignedOK
	report.SealOK = sesResult.SealOK
	report.CertOK = sesResult.CertOK
	report.applySignatureCertificatePolicy(options, [][]byte{sesResult.SignCertRaw, sesResult.SealCertRaw}, sesResult.Certs)
	if sigFile.SignedInfo.Seal.BaseLoc != "" {
		sealPath := signatureRefPath(sigPath, sigFile.SignedInfo.Seal.BaseLoc)
		sealData, err := r.readFileExact(sealPath)
		if err != nil {
			report.Error = err.Error()
			return report
		}
		report.SealMatchOK = bytes.Equal(sealData, sesResult.SealRaw)
	}
	report.Valid = report.DigestOK && report.DataHashOK && report.SignedValueOK && report.SealOK && report.SealMatchOK && report.CertOK && report.certificatePolicyOK()
	return report
}

// verifySignatureReferences 验证签名保护文件列表
// 入参: sigPath 签名文件路径, refs 签名保护文件列表
// 返回: []SignatureReferenceVerify 保护文件验证结果
func (r *Reader) verifySignatureReferences(sigPath string, refs SignatureReferences) []SignatureReferenceVerify {
	results := make([]SignatureReferenceVerify, 0, len(refs.Reference))
	for _, ref := range refs.Reference {
		results = append(results, r.verifySignatureReference(sigPath, refs.CheckMethod, ref))
	}
	return results
}

// verifySignatureReference 验证签名保护文件
// 入参: sigPath 签名文件路径, method 摘要算法, ref 保护文件引用
// 返回: SignatureReferenceVerify 保护文件验证结果
func (r *Reader) verifySignatureReference(sigPath, method string, ref SignatureReference) SignatureReferenceVerify {
	refPath := signatureRefPath(sigPath, ref.FileRef)
	result := SignatureReferenceVerify{
		FileRef: ref.FileRef,
		Path:    refPath,
	}
	checkValue, err := base64.StdEncoding.DecodeString(strings.TrimSpace(ref.CheckValue))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	data, err := r.readFileExact(refPath)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	actual, err := signatureDigest(method, data)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.CheckValue = checkValue
	result.Actual = actual
	result.OK = subtle.ConstantTimeCompare(checkValue, actual) == 1
	return result
}

// parseSignatureFile 解析签名文件
// 入参: data 签名文件XML数据
// 返回: *SignatureFile 签名文件结构, error 错误信息
func parseSignatureFile(data []byte) (*SignatureFile, error) {
	var sigFile SignatureFile
	if err := xml.Unmarshal(data, &sigFile); err != nil {
		return nil, err
	}
	return &sigFile, nil
}

// readFileExact 读取OFD包内文件
// 入参: name 包内文件路径
// 返回: []byte 文件数据, error 错误信息
func (r *Reader) readFileExact(name string) ([]byte, error) {
	name = cleanPackagePath(name)
	if f, ok := r.fileIndex[name]; ok {
		return readZipFile(f)
	}
	return nil, fmt.Errorf("file not found: %s", name)
}

// signatureDigest 计算签名摘要
// 入参: method 摘要算法, data 原文数据
// 返回: []byte 摘要值, error 错误信息
func signatureDigest(method string, data []byte) ([]byte, error) {
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
	case "1.2.840.113549.1.1.1", "1.2.840.113549.1.1.5", "1.2.840.113549.1.1.11", "1.2.840.113549.1.1.12", "1.2.840.113549.1.1.13", "1.2.840.113549.1.1.14", "RSA", "RSASHA1", "RSASHA224", "RSASHA256", "RSASHA384", "RSASHA512", "SHA1RSA", "SHA224RSA", "SHA256RSA", "SHA384RSA", "SHA512RSA", "SHA1WITHRSA", "SHA224WITHRSA", "SHA256WITHRSA", "SHA384WITHRSA", "SHA512WITHRSA":
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
	case "1.2.840.10045.4.1", "1.2.840.10045.4.3.1", "1.2.840.10045.4.3.2", "1.2.840.10045.4.3.3", "1.2.840.10045.4.3.4", "ECDSA", "ECDSASHA1", "ECDSASHA224", "ECDSASHA256", "ECDSASHA384", "ECDSASHA512", "SHA1ECDSA", "SHA224ECDSA", "SHA256ECDSA", "SHA384ECDSA", "SHA512ECDSA", "SHA1WITHECDSA", "SHA224WITHECDSA", "SHA256WITHECDSA", "SHA384WITHECDSA", "SHA512WITHECDSA":
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

// verifyPublicKeySignature 验证公钥签名
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
	x509Cert, err := x509.ParseCertificate(cert)
	if err != nil {
		return false, err
	}
	switch pub := x509Cert.PublicKey.(type) {
	case *rsa.PublicKey:
		if !isRSASignatureMethod(method) {
			return false, nil
		}
		return rsa.VerifyPKCS1v15(pub, h, digest, signedValue) == nil, nil
	case *ecdsa.PublicKey:
		if !isECDSASignatureMethod(method) {
			return false, nil
		}
		return verifyECDSASignature(pub, digest, signedValue), nil
	default:
		return false, fmt.Errorf("unsupported public key algorithm")
	}
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
	p := strings.TrimSpace(refPath)
	p = strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(p, "/") {
		return cleanPackagePath(p)
	}
	return path.Clean(resolveResourcePath(basePath, "", p))
}

// referencesOK 判断保护文件摘要是否全部通过
// 入参: refs 保护文件验证结果
// 返回: bool 是否全部通过
func referencesOK(refs []SignatureReferenceVerify) bool {
	if len(refs) == 0 {
		return false
	}
	for _, ref := range refs {
		if !ref.OK {
			return false
		}
	}
	return true
}

// applySignatureCertificatePolicy 应用签名证书策略
// 入参: options 验证选项, certs 待验证证书, extraCerts 证书池
func (report *SignatureVerifyReport) applySignatureCertificatePolicy(options *signatureVerifyOptions, certs [][]byte, extraCerts [][]byte) {
	certs = compactSignatureCerts(certs)
	if options.VerifyTime != nil {
		report.CertTimeChecked = true
		report.CertTimeOK = signatureCertsValidAt(certs, *options.VerifyTime)
	}
	if len(options.TrustCerts) != 0 {
		report.CertTrustChecked = true
		report.CertTrustOK = true
		pool := append([][]byte{}, options.SignCerts...)
		pool = append(pool, extraCerts...)
		pool = append(pool, options.TrustCerts...)
		pool = compactSignatureCerts(pool)
		for _, cert := range certs {
			if !signatureCertTrustedBy(cert, pool, options.TrustCerts, make(map[string]bool)) {
				report.CertTrustOK = false
				break
			}
		}
		if len(certs) == 0 {
			report.CertTrustOK = false
		}
	}
}

// certificatePolicyOK 判断证书策略是否通过
// 返回: bool 是否通过
func (report SignatureVerifyReport) certificatePolicyOK() bool {
	if report.CertTimeChecked && !report.CertTimeOK {
		return false
	}
	if report.CertTrustChecked && !report.CertTrustOK {
		return false
	}
	return true
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

// signatureCertTrustedBy 判断证书是否可链到信任证书
// 入参: cert 证书, pool 证书池, trusts 信任证书, visited 已访问证书
// 返回: bool 是否受信任
func signatureCertTrustedBy(cert []byte, pool, trusts [][]byte, visited map[string]bool) bool {
	if len(cert) == 0 {
		return false
	}
	for _, trust := range trusts {
		if bytes.Equal(cert, trust) {
			return true
		}
	}
	key := string(cert)
	if visited[key] {
		return false
	}
	visited[key] = true
	c, err := parseSignatureCertificate(cert)
	if err != nil {
		return false
	}
	for _, issuerCert := range pool {
		if bytes.Equal(cert, issuerCert) {
			continue
		}
		issuer, err := parseSignatureCertificate(issuerCert)
		if err != nil || !bytes.Equal(c.Issuer, issuer.Subject) {
			continue
		}
		if ok, err := verifyCertificateSignature(c, issuerCert); err != nil || !ok {
			continue
		}
		if signatureCertTrustedBy(issuerCert, pool, trusts, visited) {
			return true
		}
	}
	return false
}

// verifyCertificateSignature 验证证书签名
// 入参: cert 证书信息, issuerCert 颁发者证书
// 返回: bool 是否验证通过, error 错误信息
func verifyCertificateSignature(cert signatureCertificate, issuerCert []byte) (bool, error) {
	if isSM2SignatureMethod(cert.SignatureAlg) {
		pub, err := parseSM2PublicKeyFromCert(issuerCert)
		if err != nil {
			return false, err
		}
		return sm2VerifySignature(pub, nil, cert.TBS, cert.Signature), nil
	}
	return verifyPublicKeySignature(cert.SignatureAlg, "", issuerCert, cert.TBS, cert.Signature)
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
	subject := certificateNameValues(cert.SubjectValue)
	issuer := certificateNameValues(cert.IssuerValue)
	info := SignatureCertInfo{
		Subject:      certificateNameString(subject),
		CommonName:   certificateNameFirst(subject, "2.5.4.3"),
		Organization: certificateNameFirst(subject, "2.5.4.10"),
		Issuer:       certificateNameString(issuer),
		NotBefore:    cert.NotBefore,
		NotAfter:     cert.NotAfter,
	}
	if cert.Serial != nil {
		info.SerialNumber = cert.Serial.String()
	}
	return info
}

// signatureCertificate 签名证书结构
type signatureCertificate struct {
	Raw          []byte
	TBS          []byte
	Issuer       []byte
	IssuerValue  asn1.RawValue
	Subject      []byte
	SubjectValue asn1.RawValue
	PublicKey    asn1.RawValue
	Serial       *big.Int
	NotBefore    time.Time
	NotAfter     time.Time
	SignatureAlg string
	Signature    []byte
}

// parseSignatureCertificate 解析签名证书
// 入参: data DER编码证书
// 返回: signatureCertificate 签名证书结构, error 错误信息
func parseSignatureCertificate(data []byte) (signatureCertificate, error) {
	var cert struct {
		TBSCertificate     asn1.RawValue
		SignatureAlgorithm asn1.RawValue
		SignatureValue     asn1.BitString
	}
	rest, err := asn1.Unmarshal(data, &cert)
	if err != nil || len(rest) != 0 {
		return signatureCertificate{}, fmt.Errorf("invalid certificate")
	}
	items, ok := asn1Children(cert.TBSCertificate.Bytes)
	if !ok {
		return signatureCertificate{}, fmt.Errorf("invalid tbs certificate")
	}
	idx := 0
	if len(items) > 0 && items[0].Class == asn1.ClassContextSpecific && items[0].Tag == 0 {
		idx++
	}
	if len(items) <= idx+5 {
		return signatureCertificate{}, fmt.Errorf("invalid certificate")
	}
	serial, err := asn1IntegerBig(items[idx])
	if err != nil {
		return signatureCertificate{}, err
	}
	validity, err := parseCertificateValidity(items[idx+3])
	if err != nil {
		return signatureCertificate{}, err
	}
	alg, err := parseGBTAlgorithm(cert.SignatureAlgorithm)
	if err != nil {
		return signatureCertificate{}, err
	}
	if cert.SignatureValue.BitLength%8 != 0 {
		return signatureCertificate{}, fmt.Errorf("invalid certificate signature")
	}
	return signatureCertificate{
		Raw:          append([]byte(nil), data...),
		TBS:          append([]byte(nil), cert.TBSCertificate.FullBytes...),
		Issuer:       append([]byte(nil), items[idx+2].FullBytes...),
		IssuerValue:  items[idx+2],
		Subject:      append([]byte(nil), items[idx+4].FullBytes...),
		SubjectValue: items[idx+4],
		PublicKey:    items[idx+5],
		Serial:       serial,
		NotBefore:    validity[0],
		NotAfter:     validity[1],
		SignatureAlg: alg,
		Signature:    append([]byte(nil), cert.SignatureValue.Bytes...),
	}, nil
}

// parseCertificateValidity 解析证书有效期
// 入参: raw 证书有效期ASN.1值
// 返回: [2]time.Time 生效和失效时间, error 错误信息
func parseCertificateValidity(raw asn1.RawValue) ([2]time.Time, error) {
	items, ok := asn1Children(raw.Bytes)
	if !ok || len(items) != 2 {
		return [2]time.Time{}, fmt.Errorf("invalid certificate validity")
	}
	notBefore, err := asn1Time(items[0])
	if err != nil {
		return [2]time.Time{}, err
	}
	notAfter, err := asn1Time(items[1])
	if err != nil {
		return [2]time.Time{}, err
	}
	return [2]time.Time{notBefore, notAfter}, nil
}

// asn1Time 解析ASN.1时间
// 入参: raw ASN.1原始值
// 返回: time.Time 时间, error 错误信息
func asn1Time(raw asn1.RawValue) (time.Time, error) {
	var t time.Time
	rest, err := asn1.Unmarshal(raw.FullBytes, &t)
	if err != nil || len(rest) != 0 {
		return time.Time{}, fmt.Errorf("invalid time")
	}
	return t, nil
}

// certificateNameValues 解析证书名称字段
// 入参: raw 名称原始值
// 返回: map[string][]string OID字段列表
func certificateNameValues(raw asn1.RawValue) map[string][]string {
	out := make(map[string][]string)
	sets, ok := asn1Children(raw.Bytes)
	if !ok {
		return out
	}
	for _, set := range sets {
		attrs, ok := asn1Children(set.Bytes)
		if !ok {
			continue
		}
		for _, attr := range attrs {
			items, ok := asn1Children(attr.Bytes)
			if !ok || len(items) < 2 {
				continue
			}
			oid, err := asn1OIDString(items[0])
			if err != nil {
				continue
			}
			if value := strings.TrimSpace(asn1String(items[1])); value != "" {
				out[oid] = append(out[oid], value)
			}
		}
	}
	return out
}

// certificateNameFirst 获取证书名称字段首值
// 入参: values OID字段列表, oid 字段OID
// 返回: string 字段值
func certificateNameFirst(values map[string][]string, oid string) string {
	if items := values[oid]; len(items) > 0 {
		return items[0]
	}
	return ""
}

// certificateNameString 格式化证书名称
// 入参: values OID字段列表
// 返回: string 证书名称
func certificateNameString(values map[string][]string) string {
	var parts []string
	for _, item := range []struct {
		OID   string
		Label string
	}{
		{"2.5.4.3", "CN"},
		{"2.5.4.10", "O"},
		{"2.5.4.11", "OU"},
		{"2.5.4.6", "C"},
		{"2.5.4.8", "ST"},
		{"2.5.4.7", "L"},
		{"2.5.4.5", "SN"},
		{"1.2.840.113549.1.9.1", "E"},
	} {
		for _, value := range values[item.OID] {
			parts = append(parts, item.Label+"="+value)
		}
	}
	return strings.Join(parts, ", ")
}

// parseSignatureCerts 解析签名验证证书
// 入参: data DER或PEM编码证书
// 返回: [][]byte DER编码证书列表
func parseSignatureCerts(data []byte) [][]byte {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil
	}
	var certs [][]byte
	rest := data
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
