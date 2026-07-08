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
	"crypto/elliptic"
	"encoding/asn1"
	"encoding/binary"
	"math/big"
)

const sm2DefaultUserID = "1234567812345678"

var sm2P256 = newSM2P256()

// sm2PublicKey SM2公钥
type sm2PublicKey struct {
	X *big.Int
	Y *big.Int
}

// newSM2P256 创建SM2椭圆曲线
// 返回: elliptic.Curve SM2椭圆曲线
func newSM2P256() elliptic.Curve {
	c := &elliptic.CurveParams{Name: "SM2-P-256"}
	c.P = sm2Big("FFFFFFFEFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF00000000FFFFFFFFFFFFFFFF")
	c.N = sm2Big("FFFFFFFEFFFFFFFFFFFFFFFFFFFFFFFF7203DF6B21C6052B53BBF40939D54123")
	c.B = sm2Big("28E9FA9E9D9F5E344D5A9E4BCF6509A7F39789F515AB8F92DDBCBD414D940E93")
	c.Gx = sm2Big("32C4AE2C1F1981195F9904466A39C9948FE30BBFF2660BE1715A4589334C74C7")
	c.Gy = sm2Big("BC3736A2F4F6779C59BDCEE36B692153D0A9877CC62A474002DF32E52139F0A0")
	c.BitSize = 256
	return c
}

// sm2Big 解析SM2大整数常量
// 入参: s 十六进制字符串
// 返回: *big.Int 大整数
func sm2Big(s string) *big.Int {
	n, _ := new(big.Int).SetString(s, 16)
	return n
}

// sm2VerifySignature 验证SM2签名值
// 入参: pub 公钥, userID 用户标识, msg 原文, sig 签名值
// 返回: bool 是否验证通过
func sm2VerifySignature(pub sm2PublicKey, userID, msg, sig []byte) bool {
	r, s, ok := parseSM2Signature(sig)
	if !ok {
		return false
	}
	return sm2Verify(pub, userID, msg, r, s)
}

// parseSM2Signature 解析SM2签名值
// 入参: sig 签名值
// 返回: *big.Int R值, *big.Int S值, bool 是否解析成功
func parseSM2Signature(sig []byte) (*big.Int, *big.Int, bool) {
	if len(sig) == 64 {
		return new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]), true
	}
	var rs struct {
		R *big.Int
		S *big.Int
	}
	rest, err := asn1.Unmarshal(sig, &rs)
	if err != nil || len(rest) != 0 || rs.R == nil || rs.S == nil {
		return nil, nil, false
	}
	return rs.R, rs.S, true
}

// sm2Verify 验证SM2签名
// 入参: pub 公钥, userID 用户标识, msg 原文, r R值, s S值
// 返回: bool 是否验证通过
func sm2Verify(pub sm2PublicKey, userID, msg []byte, r, s *big.Int) bool {
	n := sm2P256.Params().N
	if r.Sign() <= 0 || s.Sign() <= 0 || r.Cmp(n) >= 0 || s.Cmp(n) >= 0 {
		return false
	}
	if pub.X == nil || pub.Y == nil || !sm2P256.IsOnCurve(pub.X, pub.Y) {
		return false
	}
	e := new(big.Int).SetBytes(sm2MessageDigest(pub, userID, msg))
	t := new(big.Int).Add(r, s)
	t.Mod(t, n)
	if t.Sign() == 0 {
		return false
	}
	x1, y1 := sm2P256.ScalarBaseMult(s.Bytes())
	x2, y2 := sm2P256.ScalarMult(pub.X, pub.Y, t.Bytes())
	x, _ := sm2P256.Add(x1, y1, x2, y2)
	if x == nil {
		return false
	}
	v := new(big.Int).Add(e, x)
	v.Mod(v, n)
	return v.Cmp(r) == 0
}

// sm2MessageDigest 计算SM2签名摘要
// 入参: pub 公钥, userID 用户标识, msg 原文
// 返回: []byte 摘要值
func sm2MessageDigest(pub sm2PublicKey, userID, msg []byte) []byte {
	h := newSM3()
	h.Write(sm2ZA(pub, userID))
	h.Write(msg)
	return h.Sum(nil)
}

// sm2ZA 计算SM2用户标识杂凑值
// 入参: pub 公钥, userID 用户标识
// 返回: []byte ZA值
func sm2ZA(pub sm2PublicKey, userID []byte) []byte {
	if len(userID) == 0 {
		userID = []byte(sm2DefaultUserID)
	}
	h := newSM3()
	var entl [2]byte
	binary.BigEndian.PutUint16(entl[:], uint16(len(userID)*8))
	h.Write(entl[:])
	h.Write(userID)
	h.Write(sm2Fixed(sm2A()))
	h.Write(sm2Fixed(sm2P256.Params().B))
	h.Write(sm2Fixed(sm2P256.Params().Gx))
	h.Write(sm2Fixed(sm2P256.Params().Gy))
	h.Write(sm2Fixed(pub.X))
	h.Write(sm2Fixed(pub.Y))
	return h.Sum(nil)
}

// sm2A 获取SM2曲线A参数
// 返回: *big.Int 曲线A参数
func sm2A() *big.Int {
	return new(big.Int).Sub(sm2P256.Params().P, big.NewInt(3))
}

// sm2Fixed 转换为SM2固定长度字节
// 入参: n 大整数
// 返回: []byte 固定长度字节
func sm2Fixed(n *big.Int) []byte {
	out := make([]byte, 32)
	if n == nil {
		return out
	}
	b := n.Bytes()
	if len(b) > len(out) {
		b = b[len(b)-len(out):]
	}
	copy(out[len(out)-len(b):], b)
	return out
}
