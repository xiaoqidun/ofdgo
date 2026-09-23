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

// GeometryPath 保存独立于绘图库的几何路径，不扁平化曲线或舍入坐标
// 坐标以左上角为原点，单位为毫米，子路径从Move开始，Close回到当前子路径起点
type GeometryPath []GeometrySegment

// GeometryVerb 表示几何路径指令
type GeometryVerb uint8

const (
	GeometryMove GeometryVerb = iota
	GeometryLine
	GeometryQuad
	GeometryCubic
	GeometryArc
	GeometryClose
)

// GeometrySegment 保存路径端点、贝塞尔控制点或端点式椭圆弧参数
// 弧线Rotation单位为度，Sweep为true表示顺时针，Large表示大弧
type GeometrySegment struct {
	Verb               GeometryVerb
	End                Point
	Control1, Control2 Point
	RadiusX, RadiusY   float64
	Rotation           float64
	Large, Sweep       bool
}
