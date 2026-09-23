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
	"strconv"
	"strings"
)

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

// ParseGeometryPath 解析标准OFD紧缩路径，保留弧线和原始数值精度
// 入参: data 紧缩路径
// 返回: GeometryPath 独立路径, error 指令或参数错误
func ParseGeometryPath(data string) (GeometryPath, error) {
	tokens := strings.Fields(data)
	if len(tokens) == 0 {
		return nil, nil
	}
	if tokens[0] != "M" && tokens[0] != "S" {
		return nil, fmt.Errorf("path must start with M or S")
	}
	var result GeometryPath
	for i := 0; i < len(tokens); {
		command := tokens[i]
		i++
		count := 0
		switch command {
		case "M", "S", "L":
			count = 2
		case "Q":
			count = 4
		case "B":
			count = 6
		case "A":
			count = 7
		case "C":
		default:
			return nil, fmt.Errorf("invalid path command %q", command)
		}
		if len(tokens)-i < count {
			return nil, fmt.Errorf("missing arguments for path command %s", command)
		}
		var values [7]float64
		for j, token := range tokens[i : i+count] {
			if command == "A" && (j == 3 || j == 4) && (token == "true" || token == "false") {
				if token == "true" {
					values[j] = 1
				}
				continue
			}
			value, err := strconv.ParseFloat(token, 64)
			if err != nil || !finite(value) || strings.ContainsAny(token, "xX_") {
				return nil, fmt.Errorf("invalid number %q", token)
			}
			values[j] = value
		}
		i += count
		segment := GeometrySegment{}
		switch command {
		case "M", "S":
			segment.Verb, segment.End = GeometryMove, Point{values[0], values[1]}
		case "L":
			segment.Verb, segment.End = GeometryLine, Point{values[0], values[1]}
		case "Q":
			segment.Verb, segment.Control1, segment.End = GeometryQuad, Point{values[0], values[1]}, Point{values[2], values[3]}
		case "B":
			segment.Verb, segment.Control1, segment.Control2, segment.End = GeometryCubic, Point{values[0], values[1]}, Point{values[2], values[3]}, Point{values[4], values[5]}
		case "A":
			if values[0] < 0 || values[1] < 0 || values[3] != 0 && values[3] != 1 || values[4] != 0 && values[4] != 1 {
				return nil, fmt.Errorf("invalid arc radii or flags")
			}
			segment = GeometrySegment{Verb: GeometryArc, RadiusX: values[0], RadiusY: values[1], Rotation: values[2], Large: values[3] != 0, Sweep: values[4] != 0, End: Point{values[5], values[6]}}
		case "C":
			segment.Verb = GeometryClose
		}
		result = append(result, segment)
	}
	return result, nil
}

// objectGeometryPath 解析对象路径并由当前几何实现应用页面变换
// 入参: geometry 几何后端, object 路径对象
// 返回: GeometryPath 页面路径, error 参数或变换错误
func objectGeometryPath(geometry GeometryBackend, object PathObject) (GeometryPath, error) {
	path, err := ParseGeometryPath(object.AbbreviatedData)
	if err != nil {
		return nil, err
	}
	box, err := creationBox(object.Boundary)
	if err != nil {
		return nil, err
	}
	if object.CTM != "" {
		if _, err := creationNumbers(object.CTM, 6); err != nil {
			return nil, err
		}
	}
	return geometry.Transform(path, TranslationMatrix(box.X, box.Y).Multiply(NewMatrix(object.CTM)))
}

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
		open = segment.Verb != GeometryClose
	}
	if open {
		result = append(result, GeometrySegment{Verb: GeometryClose})
	}
	return result
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
