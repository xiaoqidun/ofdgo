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
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
	"time"

	"github.com/emmansun/gmsm/smx509"
	"golang.org/x/crypto/ocsp"
)

// SignatureRevocationEvidence 离线吊销证据
type SignatureRevocationEvidence struct {
	CRLs          [][]byte
	OCSPResponses [][]byte
}

// SignatureRevocationRequest 外部吊销证据请求
type SignatureRevocationRequest struct {
	Certificate []byte
	Issuer      []byte
	VerifyTime  time.Time
}

// SignatureRevocationOptions 离线吊销检查选项
// 检查签署者及制章者证书, 不代表整条证书链或TSA的吊销状态
// Fetch仅提供DER证据, 本库不主动联网, 缺少NextUpdate时必须显式设置MaxAge
// VerifyTime缺省取验签指定时间或当前时间, 不采用签名自报时间
type SignatureRevocationOptions struct {
	SignatureRevocationEvidence
	Certificates [][]byte
	Fetch        func(SignatureRevocationRequest) (SignatureRevocationEvidence, error)
	Required     bool
	VerifyTime   *time.Time
	MaxAge       time.Duration
}

// SignatureRevocationReport 单张证书吊销检查结果
// Checked仅表示已取得经验证的状态结论, OK仅在状态为good时为true
type SignatureRevocationReport struct {
	Certificate SignatureCertInfo
	Source      string
	Status      string
	Checked     bool
	OK          bool
	ThisUpdate  time.Time
	NextUpdate  time.Time
	RevokedAt   time.Time
	Error       string
}

// WithSignatureRevocation 设置离线吊销证据检查
// 入参: options 吊销选项
// 返回: SignatureVerifyOption 验签选项
func WithSignatureRevocation(options SignatureRevocationOptions) SignatureVerifyOption {
	options.CRLs = cloneSignatureEvidence(options.CRLs)
	options.OCSPResponses = cloneSignatureEvidence(options.OCSPResponses)
	options.Certificates = appendSignatureCerts(nil, options.Certificates...)
	if options.VerifyTime != nil {
		t := *options.VerifyTime
		options.VerifyTime = &t
	}
	return func(o *signatureVerifyOptions) { o.Revocation = &options }
}

// VerifySignatureRevocation 验证单张证书的离线吊销证据
// 入参: cert 证书DER, issuer 直接颁发者DER, options 吊销选项
// 返回: SignatureRevocationReport 验证结果
func VerifySignatureRevocation(cert, issuer []byte, options SignatureRevocationOptions) SignatureRevocationReport {
	result := SignatureRevocationReport{Certificate: signatureCertInfo(cert), Status: "unknown"}
	c, err := parseSignatureCertificate(cert)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	i, err := parseSignatureCertificate(issuer)
	if err != nil || !i.IsCA || i.KeyUsage != 0 && i.KeyUsage&smx509.KeyUsageCertSign == 0 || !bytes.Equal(c.RawIssuer, i.RawSubject) {
		result.Error = "invalid revocation issuer"
		return result
	}
	valid, err := verifyCertificateSignature(c, issuer)
	if err != nil || !valid {
		result.Error = "certificate issuer signature mismatch"
		return result
	}
	at := time.Now()
	if options.VerifyTime != nil {
		at = *options.VerifyTime
	}
	if !timeInRange(at, i.NotBefore, i.NotAfter) || len(i.UnhandledCriticalExtensions) != 0 {
		result.Error = "invalid revocation issuer validity"
		return result
	}
	evidence := options.SignatureRevocationEvidence
	if options.Fetch != nil {
		fetched, err := options.Fetch(SignatureRevocationRequest{Certificate: bytes.Clone(cert), Issuer: bytes.Clone(issuer), VerifyTime: at})
		if err != nil {
			result.Error = err.Error()
			return result
		}
		evidence.CRLs = append(cloneSignatureEvidence(evidence.CRLs), fetched.CRLs...)
		evidence.OCSPResponses = append(cloneSignatureEvidence(evidence.OCSPResponses), fetched.OCSPResponses...)
	}
	var good *SignatureRevocationReport
	for _, raw := range evidence.CRLs {
		item := verifySignatureCRL(raw, cert, issuer, at, options.MaxAge)
		if item.Checked && item.Status == "revoked" {
			return item
		}
		if item.OK {
			copy := item
			good = &copy
		} else if item.Error != "" {
			result.Error = item.Error
		}
	}
	for _, raw := range evidence.OCSPResponses {
		item := verifySignatureOCSP(raw, cert, issuer, at, options.MaxAge)
		if item.Checked && item.Status == "revoked" {
			return item
		}
		if item.OK {
			copy := item
			good = &copy
		} else if item.Error != "" {
			result.Error = item.Error
		}
		if item.Checked && item.Status == "unknown" && !result.Checked {
			result = item
		}
	}
	if good != nil {
		return *good
	}
	if result.Error == "" {
		result.Error = "usable revocation evidence not found"
	}
	return result
}

// signatureEvidenceFresh 检查证据时间窗口及最大年龄
// 入参: thisUpdate 生效时间, nextUpdate 下次更新时间, at 验证时间, maxAge 最大年龄
// 返回: bool 证据是否新鲜
func signatureEvidenceFresh(thisUpdate, nextUpdate, at time.Time, maxAge time.Duration) bool {
	if thisUpdate.IsZero() || thisUpdate.After(at) || maxAge < 0 {
		return false
	}
	if maxAge > 0 && at.Sub(thisUpdate) > maxAge {
		return false
	}
	if nextUpdate.IsZero() {
		return maxAge > 0
	}
	return nextUpdate.After(thisUpdate) && at.Before(nextUpdate)
}

// verifySignatureCRL 验证直接完整CRL及目标序列号
// 入参: raw CRL字节, cert 证书, issuer 颁发者, at 验证时间, maxAge 最大年龄
// 返回: SignatureRevocationReport 吊销结果
func verifySignatureCRL(raw, cert, issuer []byte, at time.Time, maxAge time.Duration) (result SignatureRevocationReport) {
	result = SignatureRevocationReport{Certificate: signatureCertInfo(cert), Source: "CRL", Status: "unknown"}
	err := func() error {
		crl, err := smx509.ParseRevocationList(raw)
		if err != nil {
			return err
		}
		parent, err := parseSignatureCertificate(issuer)
		if err != nil {
			return err
		}
		if !bytes.Equal(crl.RawIssuer, parent.RawSubject) {
			return fmt.Errorf("CRL issuer mismatch")
		}
		if len(crl.AuthorityKeyId) != 0 && !bytes.Equal(crl.AuthorityKeyId, parent.SubjectKeyId) {
			return fmt.Errorf("CRL authority key mismatch")
		}
		if err := crl.CheckSignatureFrom(parent); err != nil {
			return err
		}
		for _, ext := range crl.Extensions {
			if ext.Id.String() == "2.5.29.27" || ext.Id.String() == "2.5.29.28" {
				return fmt.Errorf("delta or scoped CRL not supported")
			}
			if ext.Critical {
				return fmt.Errorf("unsupported critical CRL extension")
			}
		}
		result.ThisUpdate, result.NextUpdate = crl.ThisUpdate, crl.NextUpdate
		if !signatureEvidenceFresh(crl.ThisUpdate, crl.NextUpdate, at, maxAge) {
			return fmt.Errorf("stale or future CRL")
		}
		c, err := parseSignatureCertificate(cert)
		if err != nil {
			return err
		}
		for _, entry := range crl.RevokedCertificateEntries {
			for _, ext := range entry.Extensions {
				if ext.Critical || ext.Id.String() == "2.5.29.29" {
					return fmt.Errorf("unsupported CRL entry extension")
				}
			}
			if entry.SerialNumber.Cmp(c.SerialNumber) == 0 {
				if entry.ReasonCode == 8 {
					return fmt.Errorf("removeFromCRL requires delta processing")
				}
				if entry.RevocationTime.After(crl.ThisUpdate) {
					return fmt.Errorf("CRL revocation time is in the future")
				}
				result.Checked, result.Status, result.RevokedAt = true, "revoked", entry.RevocationTime
				return nil
			}
		}
		result.Checked, result.OK, result.Status = true, true, "good"
		return nil
	}()
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

// verifySignatureOCSP 验证OCSP签名、授权、颁发者绑定及状态
// 入参: raw 响应DER, cert 证书, issuer 颁发者, at 验证时间, maxAge 最大年龄
// 返回: SignatureRevocationReport 吊销结果
func verifySignatureOCSP(raw, cert, issuer []byte, at time.Time, maxAge time.Duration) (result SignatureRevocationReport) {
	result = SignatureRevocationReport{Certificate: signatureCertInfo(cert), Source: "OCSP", Status: "unknown"}
	err := func() error {
		c, err := x509.ParseCertificate(cert)
		if err != nil {
			return fmt.Errorf("unsupported OCSP certificate: %w", err)
		}
		parent, err := x509.ParseCertificate(issuer)
		if err != nil {
			return err
		}
		response, err := ocsp.ParseResponseForCert(raw, c, parent)
		if err != nil {
			return err
		}
		if err := verifyOCSPCertID(response.TBSResponseData, c, parent, response.IssuerHash); err != nil {
			return err
		}
		responder := parent
		if response.Certificate != nil && !bytes.Equal(response.Certificate.Raw, parent.Raw) {
			responder = response.Certificate
			if !bytes.Equal(responder.RawIssuer, parent.RawSubject) || responder.CheckSignatureFrom(parent) != nil {
				return fmt.Errorf("OCSP responder is not issued by certificate issuer")
			}
			allowed := false
			for _, usage := range responder.ExtKeyUsage {
				allowed = allowed || usage == x509.ExtKeyUsageOCSPSigning
			}
			if !allowed || responder.KeyUsage != 0 && responder.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
				return fmt.Errorf("unauthorized OCSP responder")
			}
		}
		if len(responder.UnhandledCriticalExtensions) != 0 || !timeInRange(at, responder.NotBefore, responder.NotAfter) || !timeInRange(response.ProducedAt, responder.NotBefore, responder.NotAfter) {
			return fmt.Errorf("OCSP responder certificate not valid")
		}
		if len(response.RawResponderName) != 0 && !bytes.Equal(response.RawResponderName, responder.RawSubject) {
			return fmt.Errorf("OCSP responder name mismatch")
		}
		if len(response.ResponderKeyHash) != 0 {
			var spki struct {
				Algorithm pkix.AlgorithmIdentifier
				Key       asn1.BitString
			}
			if _, err := asn1.Unmarshal(responder.RawSubjectPublicKeyInfo, &spki); err != nil {
				return err
			}
			if !bytes.Equal(response.ResponderKeyHash, signatureHashBytes(crypto.SHA1, spki.Key.Bytes)) {
				return fmt.Errorf("OCSP responder key mismatch")
			}
		}
		result.ThisUpdate, result.NextUpdate, result.RevokedAt = response.ThisUpdate, response.NextUpdate, response.RevokedAt
		if !signatureEvidenceFresh(response.ThisUpdate, response.NextUpdate, at, maxAge) || response.ProducedAt.After(at) || response.ProducedAt.Before(response.ThisUpdate) {
			return fmt.Errorf("stale or future OCSP response")
		}
		switch response.Status {
		case ocsp.Good:
			result.Checked, result.OK, result.Status = true, true, "good"
		case ocsp.Revoked:
			if response.RevokedAt.IsZero() || response.RevokedAt.After(response.ThisUpdate) {
				return fmt.Errorf("invalid OCSP revocation time")
			}
			result.Checked, result.Status = true, "revoked"
		case ocsp.Unknown:
			result.Checked = true
		default:
			return fmt.Errorf("unsupported OCSP status")
		}
		return nil
	}()
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

// verifyOCSPCertID 补充校验x/crypto未验证的颁发者摘要和重复CertID
// 入参: tbs 响应待签名数据, cert 证书, issuer 颁发者, hash CertID摘要算法
// 返回: error 绑定错误
func verifyOCSPCertID(tbs []byte, cert, issuer *x509.Certificate, hash crypto.Hash) error {
	var data struct {
		Version    int `asn1:"optional,explicit,tag:0"`
		Responder  asn1.RawValue
		ProducedAt time.Time `asn1:"generalized"`
		Responses  []asn1.RawValue
		Extensions []pkix.Extension `asn1:"optional,explicit,tag:1"`
	}
	rest, err := asn1.Unmarshal(tbs, &data)
	if err != nil || len(rest) != 0 || data.Version != 0 {
		return fmt.Errorf("invalid OCSP response data")
	}
	for _, ext := range data.Extensions {
		if ext.Critical {
			return fmt.Errorf("unsupported critical OCSP extension")
		}
	}
	var spki struct {
		Algorithm pkix.AlgorithmIdentifier
		Key       asn1.BitString
	}
	if _, err := asn1.Unmarshal(issuer.RawSubjectPublicKeyInfo, &spki); err != nil {
		return err
	}
	matched := 0
	for _, raw := range data.Responses {
		fields, ok := asn1Children(raw.Bytes)
		if !ok || len(fields) < 3 {
			return fmt.Errorf("invalid OCSP single response")
		}
		id, ok := asn1Children(fields[0].Bytes)
		if !ok || len(id) != 4 {
			return fmt.Errorf("invalid OCSP CertID")
		}
		serial, err := asn1IntegerBig(id[3])
		if err != nil {
			return err
		}
		if serial.Cmp(cert.SerialNumber) != 0 {
			continue
		}
		matched++
		nameHash, err := asn1OctetString(id[1])
		if err != nil {
			return err
		}
		keyHash, err := asn1OctetString(id[2])
		if err != nil {
			return err
		}
		if !bytes.Equal(nameHash, signatureHashBytes(hash, issuer.RawSubject)) || !bytes.Equal(keyHash, signatureHashBytes(hash, spki.Key.Bytes)) {
			return fmt.Errorf("OCSP issuer hash mismatch")
		}
	}
	if matched != 1 {
		return fmt.Errorf("ambiguous OCSP certificate status")
	}
	return nil
}

// applySignatureRevocation 检查签署者及制章者的离线吊销证据
// 入参: o 验签选项, certs 目标证书, extra 辅助证书
func (report *SignatureVerifyReport) applySignatureRevocation(o *signatureVerifyOptions, certs, extra [][]byte) {
	var options SignatureRevocationOptions
	if o.Revocation != nil {
		options = *o.Revocation
	}
	if o.Policy != nil {
		options.Required = options.Required || o.Policy.RequireRevocation
	}
	if o.Revocation == nil && !options.Required {
		return
	}
	at := time.Now()
	if o.VerifyTime != nil {
		at = *o.VerifyTime
	}
	if options.VerifyTime != nil {
		at = *options.VerifyTime
	}
	options.VerifyTime = &at
	pool := appendSignatureCerts(nil, options.Certificates...)
	pool = append(pool, extra...)
	pool = append(pool, o.SignCerts...)
	pool = append(pool, o.TrustCerts...)
	pool = compactSignatureCerts(pool)
	for _, raw := range compactSignatureCerts(certs) {
		result := SignatureRevocationReport{Certificate: signatureCertInfo(raw), Status: "unknown", Error: "revocation issuer not found"}
		cert, err := parseSignatureCertificate(raw)
		if err == nil {
			for _, issuerRaw := range pool {
				issuer, err := parseSignatureCertificate(issuerRaw)
				if err != nil || !bytes.Equal(cert.RawIssuer, issuer.RawSubject) {
					continue
				}
				ok, err := verifyCertificateSignature(cert, issuerRaw)
				if err != nil || !ok {
					continue
				}
				result = VerifySignatureRevocation(raw, issuerRaw, options)
				break
			}
		}
		report.Revocations = append(report.Revocations, result)
	}
	if len(report.Revocations) != 0 {
		report.RevocationChecked, report.RevocationOK = true, true
		for _, result := range report.Revocations {
			report.RevocationOK = report.RevocationOK && result.Checked && result.OK
		}
	}
	if options.Required && !report.RevocationChecked {
		report.PolicyChecked, report.PolicyOK, report.PolicyError = true, false, "required revocation evidence missing"
	}
}
