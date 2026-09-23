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
	"fmt"
	"strings"
)

// GeometryPath 保存独立于绘图库的几何路径，不扁平化曲线或舍入坐标
// 坐标以左上角为原点，单位为毫米，子路径从Move开始，Close回到当前子路径起点
type GeometryPath []GeometrySegment

// closedGeometry 为填充轮廓补齐隐式闭合边，不改变源路径
// 入参: path 填充路径
// 返回: GeometryPath 显式闭合路径
func closedGeometry(path GeometryPath) GeometryPath {
	result := make(GeometryPath, 0, len(path)+1)
	open := false
	for _, segment := range path {
		if segment.Verb == GeometryMove && open {
			result = append(result, GeometrySegment{Verb: GeometryClose})
		}
		result = append(result, segment)
		if segment.Verb == GeometryMove {
			open = true
		}
		if segment.Verb == GeometryClose {
			open = false
		}
	}
	if open {
		result = append(result, GeometrySegment{Verb: GeometryClose})
	}
	return result
}

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

// SVG 将几何路径编码为SVG路径，不改变曲线或坐标精度
// 返回: string SVG路径, error 不支持的路径指令
func (p GeometryPath) SVG() (string, error) {
	return p.encode(true)
}

// OFD 将几何路径编码为OFD紧缩路径
// 返回: string 紧缩路径, error 不支持的路径指令
func (p GeometryPath) OFD() (string, error) {
	return p.encode(false)
}

// encode 按目标格式写入路径，圆弧标志采用两种格式均支持的0和1
// 入参: svg 是否使用SVG指令
// 返回: string 路径数据, error 不支持的路径指令
func (p GeometryPath) encode(svg bool) (string, error) {
	var value strings.Builder
	for _, segment := range p {
		x, y := ofdNumber(segment.End.X), ofdNumber(segment.End.Y)
		switch segment.Verb {
		case GeometryMove:
			fmt.Fprintf(&value, "M %s %s ", x, y)
		case GeometryLine:
			fmt.Fprintf(&value, "L %s %s ", x, y)
		case GeometryQuad:
			fmt.Fprintf(&value, "Q %s %s %s %s ", ofdNumber(segment.Control1.X), ofdNumber(segment.Control1.Y), x, y)
		case GeometryCubic:
			command := "B"
			if svg {
				command = "C"
			}
			fmt.Fprintf(&value, "%s %s %s %s %s %s %s ", command, ofdNumber(segment.Control1.X), ofdNumber(segment.Control1.Y), ofdNumber(segment.Control2.X), ofdNumber(segment.Control2.Y), x, y)
		case GeometryArc:
			large, sweep := 0, 0
			if segment.Large {
				large = 1
			}
			if segment.Sweep {
				sweep = 1
			}
			fmt.Fprintf(&value, "A %s %s %s %d %d %s %s ", ofdNumber(segment.RadiusX), ofdNumber(segment.RadiusY), ofdNumber(segment.Rotation), large, sweep, x, y)
		case GeometryClose:
			if svg {
				value.WriteString("Z ")
			} else {
				value.WriteString("C ")
			}
		default:
			return "", fmt.Errorf("unsupported geometry command %d", segment.Verb)
		}
	}
	return strings.TrimSpace(value.String()), nil
}

// geometryClipPath 将页面路径转换为标准填充裁剪对象
// 入参: path 页面毫米路径
// 返回: PathObject 裁剪路径, error 无效指令错误
func geometryClipPath(path GeometryPath) (PathObject, error) {
	data, err := path.OFD()
	if err != nil {
		return PathObject{}, err
	}
	fill, stroke := true, false
	return PathObject{Boundary: "0 0 1 1", Fill: &fill, Stroke: &stroke, AbbreviatedData: data}, nil
}
