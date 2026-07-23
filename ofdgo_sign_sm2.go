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

// sm2Curve SM2椭圆曲线
type sm2Curve struct {
	P  *big.Int
	N  *big.Int
	B  *big.Int
	Gx *big.Int
	Gy *big.Int
}

// sm2Point SM2雅可比坐标点
type sm2Point struct {
	X *big.Int
	Y *big.Int
	Z *big.Int
}

// newSM2P256 创建SM2椭圆曲线
// 返回: *sm2Curve SM2椭圆曲线
func newSM2P256() *sm2Curve {
	return &sm2Curve{
		P:  sm2Big("FFFFFFFEFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF00000000FFFFFFFFFFFFFFFF"),
		N:  sm2Big("FFFFFFFEFFFFFFFFFFFFFFFFFFFFFFFF7203DF6B21C6052B53BBF40939D54123"),
		B:  sm2Big("28E9FA9E9D9F5E344D5A9E4BCF6509A7F39789F515AB8F92DDBCBD414D940E93"),
		Gx: sm2Big("32C4AE2C1F1981195F9904466A39C9948FE30BBFF2660BE1715A4589334C74C7"),
		Gy: sm2Big("BC3736A2F4F6779C59BDCEE36B692153D0A9877CC62A474002DF32E52139F0A0"),
	}
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
	n := sm2P256.N
	if r.Sign() <= 0 || s.Sign() <= 0 || r.Cmp(n) >= 0 || s.Cmp(n) >= 0 {
		return false
	}
	if pub.X == nil || pub.Y == nil || !sm2P256.isOnCurve(pub.X, pub.Y) {
		return false
	}
	e := new(big.Int).SetBytes(sm2MessageDigest(pub, userID, msg))
	t := new(big.Int).Add(r, s)
	t.Mod(t, n)
	if t.Sign() == 0 {
		return false
	}
	x, ok := sm2P256.combinedMult(pub.X, pub.Y, s, t)
	if !ok {
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
	h.Write(sm2Fixed(sm2P256.B))
	h.Write(sm2Fixed(sm2P256.Gx))
	h.Write(sm2Fixed(sm2P256.Gy))
	h.Write(sm2Fixed(pub.X))
	h.Write(sm2Fixed(pub.Y))
	return h.Sum(nil)
}

// sm2A 获取SM2曲线A参数
// 返回: *big.Int 曲线A参数
func sm2A() *big.Int {
	return new(big.Int).Sub(sm2P256.P, big.NewInt(3))
}

// isOnCurve 判断点是否位于SM2曲线
// 入参: x X坐标, y Y坐标
// 返回: bool 是否位于曲线
func (c *sm2Curve) isOnCurve(x, y *big.Int) bool {
	if x.Sign() < 0 || y.Sign() < 0 || x.Cmp(c.P) >= 0 || y.Cmp(c.P) >= 0 {
		return false
	}
	left := c.fieldSquare(y)
	right := c.fieldAdd(c.fieldSub(c.fieldMul(c.fieldSquare(x), x), c.fieldScale(x, 3)), c.B)
	return left.Cmp(right) == 0
}

// combinedMult 计算sG+tP
// 入参: x 公钥X坐标, y 公钥Y坐标, s 标量S, t 标量T
// 返回: *big.Int 结果X坐标, bool 是否计算成功
func (c *sm2Curve) combinedMult(x, y, s, t *big.Int) (*big.Int, bool) {
	base := c.scalarMult(c.Gx, c.Gy, s.Bytes())
	public := c.scalarMult(x, y, t.Bytes())
	result := c.add(base, public)
	affineX, _, ok := c.affine(result)
	return affineX, ok
}

// scalarMult 计算椭圆曲线标量乘法
// 入参: x 点X坐标, y 点Y坐标, scalar 标量
// 返回: sm2Point 雅可比坐标点
func (c *sm2Curve) scalarMult(x, y *big.Int, scalar []byte) sm2Point {
	result := c.infinity()
	point := sm2Point{X: new(big.Int).Set(x), Y: new(big.Int).Set(y), Z: big.NewInt(1)}
	for _, value := range scalar {
		for bit := 7; bit >= 0; bit-- {
			result = c.double(result)
			if value&(1<<uint(bit)) != 0 {
				result = c.add(result, point)
			}
		}
	}
	return result
}

// add 计算椭圆曲线点加法
// 入参: p 点P, q 点Q
// 返回: sm2Point 结果点
func (c *sm2Curve) add(p, q sm2Point) sm2Point {
	if p.Z.Sign() == 0 {
		return q
	}
	if q.Z.Sign() == 0 {
		return p
	}
	z1z1 := c.fieldSquare(p.Z)
	z2z2 := c.fieldSquare(q.Z)
	u1 := c.fieldMul(p.X, z2z2)
	u2 := c.fieldMul(q.X, z1z1)
	s1 := c.fieldMul(p.Y, c.fieldMul(q.Z, z2z2))
	s2 := c.fieldMul(q.Y, c.fieldMul(p.Z, z1z1))
	if u1.Cmp(u2) == 0 {
		if s1.Cmp(s2) != 0 {
			return c.infinity()
		}
		return c.double(p)
	}
	h := c.fieldSub(u2, u1)
	i := c.fieldSquare(c.fieldScale(h, 2))
	j := c.fieldMul(h, i)
	r := c.fieldScale(c.fieldSub(s2, s1), 2)
	v := c.fieldMul(u1, i)
	x := c.fieldSub(c.fieldSub(c.fieldSquare(r), j), c.fieldScale(v, 2))
	y := c.fieldSub(c.fieldMul(r, c.fieldSub(v, x)), c.fieldScale(c.fieldMul(s1, j), 2))
	z := c.fieldMul(c.fieldSub(c.fieldSub(c.fieldSquare(c.fieldAdd(p.Z, q.Z)), z1z1), z2z2), h)
	return sm2Point{X: x, Y: y, Z: z}
}

// double 计算椭圆曲线点倍加
// 入参: p 点P
// 返回: sm2Point 结果点
func (c *sm2Curve) double(p sm2Point) sm2Point {
	if p.Z.Sign() == 0 || p.Y.Sign() == 0 {
		return c.infinity()
	}
	delta := c.fieldSquare(p.Z)
	gamma := c.fieldSquare(p.Y)
	beta := c.fieldMul(p.X, gamma)
	alpha := c.fieldScale(c.fieldMul(c.fieldSub(p.X, delta), c.fieldAdd(p.X, delta)), 3)
	x := c.fieldSub(c.fieldSquare(alpha), c.fieldScale(beta, 8))
	z := c.fieldSub(c.fieldSub(c.fieldSquare(c.fieldAdd(p.Y, p.Z)), gamma), delta)
	y := c.fieldSub(c.fieldMul(alpha, c.fieldSub(c.fieldScale(beta, 4), x)), c.fieldScale(c.fieldSquare(gamma), 8))
	return sm2Point{X: x, Y: y, Z: z}
}

// affine 将雅可比坐标转换为仿射坐标
// 入参: p 雅可比坐标点
// 返回: *big.Int X坐标, *big.Int Y坐标, bool 是否转换成功
func (c *sm2Curve) affine(p sm2Point) (*big.Int, *big.Int, bool) {
	if p.Z.Sign() == 0 {
		return nil, nil, false
	}
	z := new(big.Int).ModInverse(p.Z, c.P)
	if z == nil {
		return nil, nil, false
	}
	z2 := c.fieldSquare(z)
	x := c.fieldMul(p.X, z2)
	y := c.fieldMul(p.Y, c.fieldMul(z2, z))
	return x, y, true
}

// infinity 获取无穷远点
// 返回: sm2Point 无穷远点
func (c *sm2Curve) infinity() sm2Point {
	return sm2Point{X: new(big.Int), Y: new(big.Int), Z: new(big.Int)}
}

// fieldAdd 计算有限域加法
// 入参: x 左操作数, y 右操作数
// 返回: *big.Int 计算结果
func (c *sm2Curve) fieldAdd(x, y *big.Int) *big.Int {
	value := new(big.Int).Add(x, y)
	return value.Mod(value, c.P)
}

// fieldSub 计算有限域减法
// 入参: x 左操作数, y 右操作数
// 返回: *big.Int 计算结果
func (c *sm2Curve) fieldSub(x, y *big.Int) *big.Int {
	value := new(big.Int).Sub(x, y)
	return value.Mod(value, c.P)
}

// fieldMul 计算有限域乘法
// 入参: x 左操作数, y 右操作数
// 返回: *big.Int 计算结果
func (c *sm2Curve) fieldMul(x, y *big.Int) *big.Int {
	value := new(big.Int).Mul(x, y)
	return value.Mod(value, c.P)
}

// fieldSquare 计算有限域平方
// 入参: x 操作数
// 返回: *big.Int 计算结果
func (c *sm2Curve) fieldSquare(x *big.Int) *big.Int {
	return c.fieldMul(x, x)
}

// fieldScale 计算有限域整数倍
// 入参: x 操作数, scale 倍数
// 返回: *big.Int 计算结果
func (c *sm2Curve) fieldScale(x *big.Int, scale int64) *big.Int {
	return c.fieldMul(x, big.NewInt(scale))
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
