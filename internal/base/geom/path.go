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

package geom

import (
	"fmt"
	"math"
)

// 几何路径指令，包括直线、曲线、椭圆弧和闭合
const (
	Move Verb = iota
	Line
	Quad
	Cubic
	Arc
	ClosePath
)

// Path 保存独立于绘图库的几何路径，不扁平化曲线或舍入坐标
// 坐标以左上角为原点，单位为毫米，子路径从Move开始，Close回到当前子路径起点
type Path []Segment

// Verb 表示几何路径指令
type Verb uint8

// Segment 保存路径端点、贝塞尔控制点或端点式椭圆弧参数
// 弧线Rotation单位为度，Sweep为true表示顺时针，Large表示大弧
type Segment struct {
	Verb               Verb
	End                Point
	Control1, Control2 Point
	RadiusX, RadiusY   float64
	Rotation           float64
	Large, Sweep       bool
}

// StrokeOptions 描述描边参数，长度与路径坐标采用相同单位
// Width须为正有限数；Cap和Join使用标准线帽和连接样式名称，空值分别采用Butt和Miter
// MiterLimit为尖角长度与半线宽的比值上限，由调用方设置默认值
// MiterLimitSet供调用方区分显式零值与默认值，不参与几何计算
// DashOffset为虚线起始偏移，允许负值；Dashes交替指定实线段和间隔长度，奇数项按重复一遍处理
// Tolerance为曲线展开的误差目标，0采用0.001
type StrokeOptions struct {
	Width         float64
	Cap, Join     string
	MiterLimit    float64
	MiterLimitSet bool
	DashOffset    float64
	Tolerance     float64
	Dashes        []float64
}

// Close 为填充轮廓补齐隐式闭合边，不改变源路径
// 入参: path 填充路径
// 返回: Path 显式闭合路径
func Close(path Path) Path {
	open, missing := false, false
	for _, segment := range path {
		if segment.Verb == Move && open {
			missing = true
			break
		}
		open = segment.Verb != ClosePath
	}
	if !missing && !open {
		return path
	}
	result := make(Path, 0, len(path)+1)
	open = false
	for _, segment := range path {
		if segment.Verb == Move && open {
			result = append(result, Segment{Verb: ClosePath})
		}
		result = append(result, segment)
		open = segment.Verb != ClosePath
	}
	if open {
		result = append(result, Segment{Verb: ClosePath})
	}
	return result
}

// Rectangle 构建页面坐标矩形，保留输入精度
// 入参: box 矩形范围
// 返回: Path 闭合路径
func Rectangle(box Box) Path {
	return Path{
		{Verb: Move, End: Point{X: box.X, Y: box.Y}},
		{Verb: Line, End: Point{X: box.X + box.W, Y: box.Y}},
		{Verb: Line, End: Point{X: box.X + box.W, Y: box.Y + box.H}},
		{Verb: Line, End: Point{X: box.X, Y: box.Y + box.H}},
		{Verb: ClosePath, End: Point{X: box.X, Y: box.Y}},
	}
}

// ValidateStroke 校验各后端共用的描边参数
// 入参: options 描边样式
// 返回: error 无效样式
func ValidateStroke(options StrokeOptions) error {
	if !finite(options.Width) || options.Width <= 0 || !finite(options.Tolerance) || options.Tolerance < 0 || !finite(options.DashOffset) || !finite(options.MiterLimit) || options.MiterLimit < 0 {
		return fmt.Errorf("invalid stroke width or tolerance")
	}
	if options.Cap != "" && options.Cap != "Butt" && options.Cap != "Round" && options.Cap != "Square" {
		return fmt.Errorf("invalid stroke cap %q", options.Cap)
	}
	if options.Join != "" && options.Join != "Miter" && options.Join != "Round" && options.Join != "Bevel" {
		return fmt.Errorf("invalid stroke join %q", options.Join)
	}
	for _, dash := range options.Dashes {
		if !finite(dash) || dash < 0 {
			return fmt.Errorf("invalid stroke dash")
		}
	}
	return nil
}

// RectangleBounds 识别单个轴对齐矩形，不用包围盒代替实际路径
// 入参: path 填充路径
// 返回: Box 矩形范围, bool 是否为非退化矩形
func RectangleBounds(path Path) (Box, bool) {
	if len(path) > 0 && path[len(path)-1].Verb == ClosePath {
		path = path[:len(path)-1]
	}
	if len(path) == 5 && path[4].Verb == Line && path[4].End == path[0].End {
		path = path[:4]
	}
	if len(path) != 4 || path[0].Verb != Move || path[1].Verb != Line || path[2].Verb != Line || path[3].Verb != Line {
		return Box{}, false
	}
	a, b, c, d := path[0].End, path[1].End, path[2].End, path[3].End
	for _, point := range [...]Point{a, b, c, d} {
		if !finite(point.X) || !finite(point.Y) {
			return Box{}, false
		}
	}
	if a.X == c.X || a.Y == c.Y {
		return Box{}, false
	}
	if !(a.X == b.X && b.Y == c.Y && c.X == d.X && d.Y == a.Y || a.Y == b.Y && b.X == c.X && c.Y == d.Y && d.X == a.X) {
		return Box{}, false
	}
	width, height := math.Abs(c.X-a.X), math.Abs(c.Y-a.Y)
	if !finite(width) || !finite(height) {
		return Box{}, false
	}
	return Box{X: min(a.X, c.X), Y: min(a.Y, c.Y), W: width, H: height}, true
}
