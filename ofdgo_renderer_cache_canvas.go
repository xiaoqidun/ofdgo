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
	"crypto/sha256"
	"encoding/binary"
	"math"

	"github.com/tdewolff/canvas"
)

// canvasGeometryDigest 按完整坐标、路径顺序及几何容差计算摘要
// 入参: paths 非空路径序列
// 返回: [32]byte 几何摘要
func canvasGeometryDigest(paths ...*canvas.Path) [32]byte {
	hash := sha256.New()
	var data [512]byte
	binary.LittleEndian.PutUint64(data[:8], math.Float64bits(canvas.Tolerance))
	binary.LittleEndian.PutUint64(data[8:16], math.Float64bits(canvas.Epsilon))
	binary.LittleEndian.PutUint64(data[16:24], math.Float64bits(canvas.BentleyOttmannEpsilon))
	hash.Write(data[:24])
	for _, path := range paths {
		values := path.Data()
		binary.LittleEndian.PutUint64(data[:8], uint64(len(values)))
		hash.Write(data[:8])
		for len(values) > 0 {
			count := min(len(values), len(data)/8)
			for i, value := range values[:count] {
				binary.LittleEndian.PutUint64(data[i*8:], math.Float64bits(value))
			}
			hash.Write(data[:count*8])
			values = values[count:]
		}
	}
	var key [32]byte
	hash.Sum(key[:0])
	return key
}

// settleCanvasPath 复用复杂路径的填充规范化结果，返回独立副本
// 入参: path 页面坐标路径, rule 填充规则
// 返回: *canvas.Path 规范化填充轮廓
func (r *Renderer) settleCanvasPath(path *canvas.Path, rule canvas.FillRule) *canvas.Path {
	if len(path.Data()) < 256 {
		return path.Settle(rule)
	}
	key := canvasFillKey{digest: canvasGeometryDigest(path), rule: rule}
	cache := r.canvasSettledPaths()
	if result, ok := cache.get(key); ok {
		return result.Copy()
	}
	result := path.Settle(rule)
	if cost := len(result.Data())*8 + 256; cost <= cache.limit {
		cache.put(key, result.Copy(), cost)
	}
	return result
}
