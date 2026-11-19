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
	"math"
	"strconv"
	"strings"

	basegeom "github.com/xiaoqidun/ofdgo/internal/base/geom"
)

// GeometryVerb 表示几何路径指令
type GeometryVerb = basegeom.Verb

// 几何路径指令，包括直线、曲线、椭圆弧和闭合
const (
	GeometryMove  = basegeom.Move
	GeometryLine  = basegeom.Line
	GeometryQuad  = basegeom.Quad
	GeometryCubic = basegeom.Cubic
	GeometryArc   = basegeom.Arc
	GeometryClose = basegeom.ClosePath
)

// GeometryPath 保存独立于绘图库的几何路径，不扁平化曲线或舍入坐标
// 坐标以左上角为原点，单位为毫米，子路径从Move开始，Close回到当前子路径起点
type GeometryPath []GeometrySegment

// GeometrySegment 保存路径端点、贝塞尔控制点或端点式椭圆弧参数
// 弧线Rotation单位为度，Sweep为true表示顺时针，Large表示大弧
type GeometrySegment = basegeom.Segment

// ParseGeometryPath 解析标准OFD紧缩路径，保留弧线和原始数值精度
// 入参: data 紧缩路径
// 返回: GeometryPath 独立路径, error 指令或参数错误
func ParseGeometryPath(data string) (GeometryPath, error) {
	count, err := walkGeometryPath(data, nil)
	if err != nil || count == 0 {
		return nil, err
	}
	result := make(GeometryPath, 0, count)
	_, err = walkGeometryPath(data, func(segment GeometrySegment) {
		result = append(result, segment)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
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

// Bounds 通过解析极值计算直线、贝塞尔和椭圆弧的轴对齐边界
// 不进行曲线展开且不使用Tolerance，结果受float64舍入误差影响，不保证向外舍入
// 返回: Box 浮点路径范围, error 无效路径、弧线或数值溢出
func (p GeometryPath) Bounds() (Box, error) {
	return basegeom.Path(p).Bounds()
}

// Curves 按毫米误差上限将端点式椭圆弧转换为三次贝塞尔曲线，其他指令保持原样
// 入参: tolerance 正有限误差上限
// 返回: GeometryPath 独立路径, error 参数错误
func (p GeometryPath) Curves(tolerance float64) (GeometryPath, error) {
	result, err := basegeom.Path(p).Curves(tolerance)
	return GeometryPath(result), err
}

// Stroke 按端点和转角样式将曲线、虚线转换为非零填充轮廓
// 曲线按Tolerance展开，默认误差为0.001毫米，重叠轮廓保持同向绕数
// 结果未做布尔整理，按非零规则填充，区域运算需能处理重叠子路径的后端
// 入参: options 描边样式
// 返回: GeometryPath 描边轮廓, error 无效参数或精度限制
func (p GeometryPath) Stroke(options StrokeOptions) (GeometryPath, error) {
	if options.MiterLimit == 0 && !options.MiterLimitSet {
		options.MiterLimit = defaultMiterLimit
	}
	result, err := basegeom.Path(p).Stroke(options)
	if unsupported, ok := err.(basegeom.UnsupportedError); ok {
		err = fmt.Errorf("%s: %w", unsupported, ErrBackendUnavailable)
	}
	return GeometryPath(result), err
}

// Transform 精确变换控制点和椭圆轴，退化弧沿投影极值分段保留折返
// 入参: matrix 仿射变换
// 返回: GeometryPath 独立路径, error 无效路径或数值溢出
func (p GeometryPath) Transform(matrix Matrix) (GeometryPath, error) {
	result, err := basegeom.Path(p).Transform(basegeom.MatrixFromValues(matrix.Values()))
	return GeometryPath(result), err
}

// walkGeometryPath 逐指令解析紧缩路径，纯校验时不分配路径或词元列表
// 入参: data 紧缩路径, visit 指令访问器，nil仅校验
// 返回: int 指令数量, error 指令或参数错误
func walkGeometryPath(data string, visit func(GeometrySegment)) (int, error) {
	var args [7]string
	command := ""
	count, read, segments := 0, 0, 0
	for token := range strings.FieldsSeq(data) {
		if command == "" {
			if segments == 0 && token != "M" && token != "S" {
				return 0, fmt.Errorf("path must start with M or S")
			}
			command, read = token, 0
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
				count = 0
			default:
				return 0, fmt.Errorf("invalid path command %q", command)
			}
		} else {
			args[read] = token
			read++
		}
		if read != count {
			continue
		}
		segment, err := geometryPathSegment(command, args[:count])
		if err != nil {
			return 0, err
		}
		if visit != nil {
			visit(segment)
		}
		segments++
		command = ""
	}
	if command != "" {
		return 0, fmt.Errorf("missing arguments for path command %s", command)
	}
	return segments, nil
}

// geometryPathSegment 校验单条指令参数并保留原始数值精度
// 入参: command 标准指令, args 已按指令数量收集的参数
// 返回: GeometrySegment 路径段, error 参数错误
func geometryPathSegment(command string, args []string) (GeometrySegment, error) {
	var values [7]float64
	for i, token := range args {
		if command == "A" && (i == 3 || i == 4) && (token == "true" || token == "false") {
			if token == "true" {
				values[i] = 1
			}
			continue
		}
		value, err := strconv.ParseFloat(token, 64)
		if err != nil || !finite(value) || strings.ContainsAny(token, "xX_") {
			return GeometrySegment{}, fmt.Errorf("invalid number %q", token)
		}
		values[i] = value
	}
	segment := GeometrySegment{}
	switch command {
	case "M", "S":
		segment.Verb, segment.End = GeometryMove, Point{X: values[0], Y: values[1]}
	case "L":
		segment.Verb, segment.End = GeometryLine, Point{X: values[0], Y: values[1]}
	case "Q":
		segment.Verb, segment.Control1, segment.End = GeometryQuad, Point{X: values[0], Y: values[1]}, Point{X: values[2], Y: values[3]}
	case "B":
		segment.Verb, segment.Control1, segment.Control2, segment.End = GeometryCubic, Point{X: values[0], Y: values[1]}, Point{X: values[2], Y: values[3]}, Point{X: values[4], Y: values[5]}
	case "A":
		if values[3] != 0 && values[3] != 1 || values[4] != 0 && values[4] != 1 {
			return GeometrySegment{}, fmt.Errorf("invalid arc radii or flags")
		}
		segment = GeometrySegment{Verb: GeometryArc, RadiusX: math.Abs(values[0]), RadiusY: math.Abs(values[1]), Rotation: values[2], Large: values[3] != 0, Sweep: values[4] != 0, End: Point{X: values[5], Y: values[6]}}
	case "C":
		segment.Verb = GeometryClose
	}
	return segment, nil
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
	box, err := path.Bounds()
	if err != nil {
		return PathObject{}, err
	}
	data, err := path.OFD()
	if err != nil {
		return PathObject{}, err
	}
	fill, stroke := true, false
	box.W, box.H = max(box.W, 1e-6), max(box.H, 1e-6)
	return PathObject{Boundary: editorBoxString(box), CTM: TranslationMatrix(-box.X, -box.Y).String(), Fill: &fill, Stroke: &stroke, AbbreviatedData: data}, nil
}

// validate 校验公共路径指令、子路径起点和有限坐标
// 返回: error 无效路径
func (p GeometryPath) validate() error {
	return basegeom.Path(p).Validate()
}
