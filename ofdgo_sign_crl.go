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
	"sort"
	"time"

	"github.com/emmansun/gmsm/smx509"
)

// signatureCRL 已验证的直接CRL及目标条目
type signatureCRL struct {
	list       *smx509.RevocationList
	baseNumber *big.Int
	authority  []byte
	entry      *smx509.RevocationListEntry
}

// verifySignatureCRLs 验证完整CRL及增量合并后的目标状态
// 入参: raws CRL字节, cert 证书, issuer 颁发者, at 验证时间, maxAge 最大年龄
// 返回: SignatureRevocationReport 吊销结果
func verifySignatureCRLs(raws [][]byte, cert, issuer *smx509.Certificate, at time.Time, maxAge time.Duration) SignatureRevocationReport {
	result := SignatureRevocationReport{Certificate: signatureCertInfo(cert.Raw), Source: "CRL", Status: "unknown"}
	lists := make([]signatureCRL, 0, len(raws))
	for _, raw := range raws {
		item, err := readSignatureCRL(raw, cert, issuer, at)
		if err != nil {
			result.Error = err.Error()
			continue
		}
		lists = append(lists, item)
	}
	sort.Slice(lists, func(i, j int) bool {
		if !lists[i].list.ThisUpdate.Equal(lists[j].list.ThisUpdate) {
			return lists[i].list.ThisUpdate.Before(lists[j].list.ThisUpdate)
		}
		return lists[i].list.Number.Cmp(lists[j].list.Number) < 0
	})
	versions := make(map[struct {
		number string
		delta  bool
	}]*smx509.RevocationList, len(lists))
	bases := make(map[string][]*signatureCRL)
	for n := range lists {
		item := &lists[n]
		if n != 0 {
			previous := lists[n-1].list
			order := item.list.Number.Cmp(previous.Number)
			if order < 0 || order == 0 && !item.list.ThisUpdate.Equal(previous.ThisUpdate) {
				result.Error = "inconsistent CRL numbering"
				return result
			}
		}
		key := struct {
			number string
			delta  bool
		}{item.list.Number.String(), item.baseNumber != nil}
		if previous := versions[key]; previous != nil && !bytes.Equal(item.list.RawTBSRevocationList, previous.RawTBSRevocationList) {
			result.Error = "conflicting CRL version"
			return result
		}
		versions[key] = item.list
		if item.baseNumber == nil {
			id := string(item.authority)
			bases[id] = append(bases[id], item)
		}
	}
	var selected *signatureCRL
	var selectedEntry *smx509.RevocationListEntry
	for n := range lists {
		item := &lists[n]
		if !signatureEvidenceFresh(item.list.ThisUpdate, item.list.NextUpdate, at, maxAge) {
			result.Error = "stale or future CRL"
			continue
		}
		entry := item.entry
		if item.baseNumber != nil {
			candidates := bases[string(item.authority)]
			index := sort.Search(len(candidates), func(n int) bool {
				return candidates[n].list.Number.Cmp(item.list.Number) >= 0
			})
			if index == 0 || candidates[index-1].list.Number.Cmp(item.baseNumber) < 0 {
				result.Error = "matching complete CRL not found"
				continue
			}
			if entry == nil {
				entry = candidates[index-1].entry
			}
		}
		if selected != nil && item.list.Number.Cmp(selected.list.Number) == 0 && !signatureCRLStatusEqual(entry, selectedEntry) {
			result.Error = "conflicting complete and delta CRL status"
			return result
		}
		selected, selectedEntry = item, entry
	}
	if selected == nil {
		if result.Error == "" {
			result.Error = "usable CRL not found"
		}
		return result
	}
	result.ThisUpdate, result.NextUpdate = selected.list.ThisUpdate, selected.list.NextUpdate
	result.Checked, result.OK, result.Status, result.Error = true, true, "good", ""
	if selectedEntry != nil && selectedEntry.ReasonCode != 8 {
		result.OK, result.Status, result.RevokedAt = false, "revoked", selectedEntry.RevocationTime
	}
	return result
}

// readSignatureCRL 校验直接CRL签名、扩展及目标条目
// 入参: raw CRL字节, cert 证书, issuer 颁发者, at 验证时间
// 返回: signatureCRL 已验证列表, error 校验错误
func readSignatureCRL(raw []byte, cert, issuer *smx509.Certificate, at time.Time) (signatureCRL, error) {
	crl, err := smx509.ParseRevocationList(raw)
	if err != nil {
		return signatureCRL{}, err
	}
	if len(crl.Raw) != len(raw) {
		return signatureCRL{}, fmt.Errorf("trailing CRL data")
	}
	if !bytes.Equal(crl.RawIssuer, issuer.RawSubject) {
		return signatureCRL{}, fmt.Errorf("CRL issuer mismatch")
	}
	if len(crl.AuthorityKeyId) != 0 && !bytes.Equal(crl.AuthorityKeyId, issuer.SubjectKeyId) {
		return signatureCRL{}, fmt.Errorf("CRL authority key mismatch")
	}
	if err := crl.CheckSignatureFrom(issuer); err != nil {
		return signatureCRL{}, err
	}
	if crl.ThisUpdate.IsZero() || crl.ThisUpdate.After(at) || !crl.NextUpdate.IsZero() && !crl.NextUpdate.After(crl.ThisUpdate) {
		return signatureCRL{}, fmt.Errorf("invalid CRL time window")
	}
	item := signatureCRL{list: crl}
	for n, ext := range crl.Extensions {
		for _, previous := range crl.Extensions[:n] {
			if ext.Id.Equal(previous.Id) {
				return signatureCRL{}, fmt.Errorf("duplicate CRL extension")
			}
		}
		switch ext.Id.String() {
		case "2.5.29.35":
			if ext.Critical {
				return signatureCRL{}, fmt.Errorf("invalid CRL authority key extension")
			}
			item.authority = ext.Value
		case "2.5.29.20":
			number, err := signatureCRLNumber(ext.Value)
			if err != nil || ext.Critical {
				return signatureCRL{}, fmt.Errorf("invalid CRL number")
			}
			crl.Number = number
		case "2.5.29.27":
			number, err := signatureCRLNumber(ext.Value)
			if err != nil || !ext.Critical {
				return signatureCRL{}, fmt.Errorf("invalid delta CRL indicator")
			}
			item.baseNumber = number
		case "2.5.29.28":
			return signatureCRL{}, fmt.Errorf("scoped CRL not supported")
		default:
			if ext.Critical {
				return signatureCRL{}, fmt.Errorf("unsupported critical CRL extension")
			}
		}
	}
	if crl.Number == nil || item.baseNumber != nil && item.baseNumber.Cmp(crl.Number) >= 0 {
		return signatureCRL{}, fmt.Errorf("invalid CRL numbering")
	}
	for n := range crl.RevokedCertificateEntries {
		entry := &crl.RevokedCertificateEntries[n]
		for m, ext := range entry.Extensions {
			for _, previous := range entry.Extensions[:m] {
				if ext.Id.Equal(previous.Id) {
					return signatureCRL{}, fmt.Errorf("duplicate CRL entry extension")
				}
			}
			if ext.Critical || ext.Id.String() == "2.5.29.29" {
				return signatureCRL{}, fmt.Errorf("unsupported CRL entry extension")
			}
			if ext.Id.String() == "2.5.29.21" {
				var reason asn1.Enumerated
				rest, err := asn1.Unmarshal(ext.Value, &reason)
				if err != nil || len(rest) != 0 || reason < 0 || reason > 10 || reason == 7 {
					return signatureCRL{}, fmt.Errorf("invalid CRL reason code")
				}
			}
		}
		if entry.ReasonCode == 8 && item.baseNumber == nil {
			return signatureCRL{}, fmt.Errorf("removeFromCRL requires delta processing")
		}
		if entry.RevocationTime.After(crl.ThisUpdate) {
			return signatureCRL{}, fmt.Errorf("CRL revocation time is in the future")
		}
		if entry.SerialNumber.Cmp(cert.SerialNumber) == 0 {
			if item.entry != nil {
				return signatureCRL{}, fmt.Errorf("duplicate CRL certificate entry")
			}
			item.entry = entry
		}
	}
	return item, nil
}

// signatureCRLNumber 解析非负CRL编号
// 入参: raw ASN.1整数
// 返回: *big.Int 编号, error 编码错误
func signatureCRLNumber(raw []byte) (*big.Int, error) {
	var number *big.Int
	rest, err := asn1.Unmarshal(raw, &number)
	if err != nil || len(rest) != 0 || number.Sign() < 0 {
		return nil, fmt.Errorf("invalid CRL number encoding")
	}
	return number, nil
}

// signatureCRLStatusEqual 比较完整及增量列表的目标状态
// 入参: a 首个条目, b 第二个条目
// 返回: bool 状态是否一致
func signatureCRLStatusEqual(a, b *smx509.RevocationListEntry) bool {
	aGood, bGood := a == nil || a.ReasonCode == 8, b == nil || b.ReasonCode == 8
	if aGood || bGood {
		return aGood == bGood
	}
	return a.ReasonCode == b.ReasonCode && a.RevocationTime.Equal(b.RevocationTime)
}
