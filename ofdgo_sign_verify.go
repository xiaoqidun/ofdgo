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
	"io"
	"math/big"
	"path"
	"strings"
	"time"

	"github.com/emmansun/gmsm/sm2"
	"github.com/emmansun/gmsm/smx509"
)

// SignatureVerifyReport 签名验证报告
// Valid表示签名完整性、签名时间语义及调用方指定的全部策略均通过
// Checked表示对应检查已得出结论, 未检查时不能将OK的零值视为失败
// SealCertTimeOK仅提供制章证书在签名时间的状态信息, 不参与Valid判断
type SignatureVerifyReport struct {
	DocIndex             int
	DocRoot              string
	PolicyChecked        bool
	PolicyOK             bool
	PolicyError          string
	CoverageChecked      bool
	CoverageOK           bool
	CoverageError        string
	CoveredFiles         []string
	UncoveredFiles       []string
	TimestampChecked     bool
	TimestampOK          bool
	Timestamps           []SignatureTimestampReport
	RevocationChecked    bool
	RevocationOK         bool
	Revocations          []SignatureRevocationReport
	ID                   string
	BaseLoc              string
	Type                 SignType
	Provider             SignatureProvider
	Signer               string
	SignCert             SignatureCertInfo
	SealCert             SignatureCertInfo
	SealInfo             SignatureSealInfo
	SealType             string
	SignatureMethod      string
	SignatureDateTime    string
	SignatureTime        time.Time
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
	SealChecked          bool
	SealOK               bool
	SealMatchChecked     bool
	SealMatchOK          bool
	CertChecked          bool
	CertOK               bool
	SignatureTimeChecked bool
	SignatureTimeOK      bool
	SealCertTimeChecked  bool
	SealCertTimeOK       bool
	SealTimeChecked      bool
	SealTimeOK           bool
	CertTimeChecked      bool
	CertTimeOK           bool
	CertTrustChecked     bool
	CertTrustOK          bool
	CertTrustError       string
	Valid                bool
	Error                string
}

// IntegrityValid 判断签名完整性是否有效
// 返回: bool 是否有效
func (report SignatureVerifyReport) IntegrityValid() bool {
	sealOK := report.Type == SignTypeSign || report.SealChecked && report.SealOK
	return report.Error == "" && report.DigestOK && referencesOK(report.References) &&
		report.DataHashChecked && report.DataHashOK && report.SignedValueChecked && report.SignedValueOK &&
		sealOK && report.SealMatchOK && report.CertChecked && report.CertOK
}

// TrustedValid 判断签名是否可信有效
// 返回: bool 签名完整性、时间语义、证书信任及证书有效期是否均验证通过
func (report SignatureVerifyReport) TrustedValid() bool {
	return report.IntegrityValid() && report.certificatePolicyOK() && report.CertTrustChecked && report.CertTimeChecked
}

// HasFailure 判断已完成的签名检查是否存在失败
// 返回: bool 是否存在失败, 未完成检查时仍需判断Valid和Error
func (report SignatureVerifyReport) HasFailure() bool {
	for _, ref := range report.References {
		if ref.Checked && !ref.OK {
			return true
		}
	}
	return report.DataHashChecked && !report.DataHashOK ||
		report.SignedValueChecked && !report.SignedValueOK ||
		report.SealChecked && !report.SealOK ||
		report.SealMatchChecked && !report.SealMatchOK ||
		report.CertChecked && !report.CertOK || !report.certificatePolicyOK()
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

// signatureVerifyOptions 签名验证选项
type signatureVerifyOptions struct {
	SignCerts  [][]byte
	TrustCerts [][]byte
	VerifyTime *time.Time
	Policy     *SignaturePolicy
	Timestamp  *SignatureTimestampOptions
	Revocation *SignatureRevocationOptions
	DocIndex   int
	DocRoot    string
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
// 指定信任后同时验证证书链有效期，默认使用当前时间
// 入参: cert DER或PEM编码证书
// 返回: SignatureVerifyOption 签名验证选项
func WithSignatureTrustCert(cert []byte) SignatureVerifyOption {
	return func(o *signatureVerifyOptions) {
		o.TrustCerts = appendSignatureCerts(o.TrustCerts, cert)
	}
}

// WithSignatureTrustCerts 添加多张签名信任证书
// 指定信任后同时验证证书链有效期，默认使用当前时间
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
	if r.OFD == nil || len(r.OFD.DocBody) == 0 {
		return nil, fmt.Errorf("no docbody found")
	}
	if _, err := r.Doc(); err != nil {
		return nil, err
	}
	var reports []SignatureVerifyReport
	for i := range r.OFD.DocBody {
		items, err := r.VerifyDocumentSignatures(i, opts...)
		reports = append(reports, items...)
		if err != nil {
			return reports, fmt.Errorf("document %d: %w", i, err)
		}
	}
	return reports, nil
}

// VerifyDocumentSignatures 验证指定DocBody的签名且不切换阅读器当前文档
// 入参: index 从0开始的文档索引, opts 签名验证选项
// 返回: []SignatureVerifyReport 签名验证报告, error 错误信息
func (r *Reader) VerifyDocumentSignatures(index int, opts ...SignatureVerifyOption) ([]SignatureVerifyReport, error) {
	if r.OFD == nil || index < 0 || index >= len(r.OFD.DocBody) {
		return nil, fmt.Errorf("document index out of range: %d", index)
	}
	options := signatureVerifyOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	body := r.OFD.DocBody[index]
	options.DocIndex, options.DocRoot = index, r.signatureCoveragePath(body.DocRoot)
	data, err := r.readFile(body.DocRoot)
	if err != nil {
		return nil, err
	}
	var doc Document
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Signatures == "" {
		doc.Signatures = body.Signatures
	}
	if doc.Signatures == "" {
		return nil, nil
	}
	view := *r
	view.doc, view.RootDir = &doc, path.Dir(body.DocRoot)
	sigListPath := view.ResPath(doc.Signatures)
	data, err = view.readFile(sigListPath)
	if err != nil {
		return nil, err
	}
	var signatures Signatures
	if err := xml.Unmarshal(data, &signatures); err != nil {
		return nil, err
	}
	reports := make([]SignatureVerifyReport, 0, len(signatures.List))
	for _, sigRef := range signatures.List {
		reports = append(reports, view.verifySignature(sigListPath, sigRef, &options))
	}
	return reports, nil
}

// verifySignature 验证单个签名
// 入参: sigListPath 签名列表路径, sigRef 签名引用, options 验证选项
// 返回: SignatureVerifyReport 签名验证报告
func (r *Reader) verifySignature(sigListPath string, sigRef Signature, options *signatureVerifyOptions) SignatureVerifyReport {
	sigPath := signatureRefPath(sigListPath, sigRef.BaseLoc)
	report := SignatureVerifyReport{
		DocIndex:    options.DocIndex,
		DocRoot:     options.DocRoot,
		ID:          sigRef.ID,
		BaseLoc:     sigRef.BaseLoc,
		Type:        sigRef.Type,
		SealMatchOK: true,
	}
	if report.Type == "" {
		report.Type = SignTypeSeal
	}
	sigData, err := r.readFile(sigPath)
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
	if report.DigestMethod == "" {
		report.DigestMethod = "MD5"
	}
	report.References = r.verifySignatureReferences(sigPath, sigFile.SignedInfo.References)
	report.Stamps = append(report.Stamps, sigFile.SignedInfo.StampAnnot...)
	report.StampPositions, err = r.SignatureStampPositions(report.Stamps)
	if err != nil {
		report.StampPositionError = err.Error()
	}
	report.DigestOK = referencesOK(report.References)
	r.applySignatureCoverage(&report, sigListPath, options)
	report.applySignaturePolicy(options, r.OFD.Version, 0, report.SignatureMethod, report.DigestMethod)
	signedValuePath := signatureRefPath(sigPath, sigFile.SignedValue)
	signedValue, err := r.readFile(signedValuePath)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	switch report.Type {
	case SignTypeSign:
		result, err := verifyDigitalSignature(report.SignatureMethod, report.DigestMethod, signedValue, sigData, options)
		if result != nil {
			report.DataHashChecked = result.DataHashChecked
			report.DataHashOK = result.DataHashOK
			report.SignedValueChecked = result.SignedChecked
			report.SignedValueOK = result.SignedOK
			report.CertChecked = result.CertChecked
			report.CertOK = result.CertOK
			report.SignCert = result.CertInfo
			report.Signer = result.CertInfo.CommonName
		}
		if err != nil {
			report.applySignaturePolicyError(err)
			report.Error = err.Error()
			return report
		}
		report.SealOK = true
		report.SignatureTime = parseSignatureDateTime(report.SignatureDateTime)
		report.applySignatureTimePolicy()
		report.applySignatureCertificatePolicy(options, result.SignerCerts, result.Certs)
		report.applySignatureEvidence(options, signedValue, result.Timestamps, result.SignerCerts, result.Certs)
		report.Valid = report.IntegrityValid() && report.certificatePolicyOK()
		return report
	case SignTypeSeal:
	default:
		report.Error = fmt.Sprintf("unsupported signature type: %s", report.Type)
		return report
	}
	sesResult, err := verifySESSignature(signedValue, sigData, options)
	if sesResult != nil {
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
		report.applySignaturePolicyError(err)
		report.Error = err.Error()
		return report
	}
	report.applySignatureTimePolicy()
	if report.SignatureMethod != "" && !signatureAlgorithmEquivalent(report.SignatureMethod, sesResult.SignatureMethod, "SM3") {
		report.Error = "signature method does not match SES"
		return report
	}
	report.applySignatureCertificatePolicy(options, [][]byte{sesResult.SignCertRaw, sesResult.SealCertRaw}, sesResult.Certs)
	report.applySignaturePolicy(options, r.OFD.Version, sesResult.SealInfo.Version, sesResult.SignatureMethod, "SM3")
	report.applySignatureEvidence(options, signedValue, sesResult.Timestamps, [][]byte{sesResult.SignCertRaw, sesResult.SealCertRaw}, sesResult.Certs)
	if sigFile.SignedInfo.Seal.BaseLoc != "" {
		sealPath := signatureRefPath(sigPath, sigFile.SignedInfo.Seal.BaseLoc)
		sealData, err := r.readFile(sealPath)
		if err != nil {
			report.Error = err.Error()
			return report
		}
		report.SealMatchChecked = true
		report.SealMatchOK = bytes.Equal(sealData, sesResult.SealRaw)
	}
	report.Valid = report.IntegrityValid() && report.certificatePolicyOK()
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
	data, err := r.readFile(refPath)
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
	result.Checked = true
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
	x509Cert, err := parseSignatureCertificate(cert)
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
		if !ref.Checked || !ref.OK || ref.Error != "" {
			return false
		}
	}
	return true
}

// applySignatureCertificatePolicy 应用签名证书策略
// 入参: options 验证选项, certs 待验证证书, extraCerts 证书池
func (report *SignatureVerifyReport) applySignatureCertificatePolicy(options *signatureVerifyOptions, certs [][]byte, extraCerts [][]byte) {
	certs = compactSignatureCerts(certs)
	verifyTime := options.VerifyTime
	if verifyTime == nil && len(options.TrustCerts) != 0 {
		now := time.Now()
		verifyTime = &now
	}
	if verifyTime != nil {
		report.CertTimeChecked = true
		report.CertTimeOK = signatureCertsValidAt(certs, *verifyTime)
	}
	if len(options.TrustCerts) != 0 {
		report.CertTrustChecked = true
		report.CertTrustOK = true
		pool := append([][]byte{}, options.TrustCerts...)
		pool = append(pool, options.SignCerts...)
		pool = append(pool, extraCerts...)
		pool = compactSignatureCerts(pool)
		for _, cert := range certs {
			if err := verifySignatureCertificateTrust(cert, pool, options.TrustCerts, *verifyTime); err != nil {
				report.CertTrustOK = false
				report.CertTrustError = err.Error()
				break
			}
		}
		if len(certs) == 0 {
			report.CertTrustOK = false
			report.CertTrustError = "signature certificate missing"
		}
	}
}

// applySignatureTimePolicy 应用签名时间策略
func (report *SignatureVerifyReport) applySignatureTimePolicy() {
	if !report.SignatureTime.IsZero() && !report.SignCert.NotBefore.IsZero() && !report.SignCert.NotAfter.IsZero() {
		report.SignatureTimeChecked = true
		report.SignatureTimeOK = timeInRange(report.SignatureTime, report.SignCert.NotBefore, report.SignCert.NotAfter)
	}
	if !report.SignatureTime.IsZero() && !report.SealCert.NotBefore.IsZero() && !report.SealCert.NotAfter.IsZero() {
		report.SealCertTimeChecked = true
		report.SealCertTimeOK = timeInRange(report.SignatureTime, report.SealCert.NotBefore, report.SealCert.NotAfter)
	}
	if !report.SignatureTime.IsZero() && !report.SealInfo.ValidStart.IsZero() && !report.SealInfo.ValidEnd.IsZero() {
		report.SealTimeChecked = true
		report.SealTimeOK = timeInRange(report.SignatureTime, report.SealInfo.ValidStart, report.SealInfo.ValidEnd)
	}
}

// timeInRange 判断时间是否位于闭区间
// 入参: t 待判断时间, start 起始时间, end 结束时间
// 返回: bool 是否位于区间
func timeInRange(t, start, end time.Time) bool {
	return !start.After(end) && !t.Before(start) && !t.After(end)
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
			group.pool.AddCert(ca)
		}
	}
	_, err := cert.Verify(smx509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: at, KeyUsages: []smx509.ExtKeyUsage{usage}})
	return err
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

// parseSignatureDateTime 解析带时区的签名时间
// 入参: value 签名时间文本
// 返回: time.Time 签名时间
func parseSignatureDateTime(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{
		time.RFC3339Nano,
		"20060102150405.999999999Z07:00",
		"20060102150405Z07:00",
	} {
		if t, err := time.Parse(layout, value); err == nil {
			return t
		}
	}
	return time.Time{}
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
