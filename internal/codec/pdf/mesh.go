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

package pdf

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync"

	"github.com/xiaoqidun/pdfgo"
)

// MeshTriangle 保存按输出精度细分的曲面网格
type MeshTriangle struct {
	vertices [3]meshVertex
	patch    int
}

// MeshPrepared 保存变换后的网格补片和三角形及其包围盒，供各瓦片按需流式细分
type MeshPrepared struct {
	patches   []meshPreparedPatch
	triangles []meshPreparedTriangle
}

// meshPreparedPatch 保存单个变换后的双三次曲面控制点及包围盒
type meshPreparedPatch struct {
	points                 [4][4]pdfgo.Point
	minX, minY, maxX, maxY float64
	index                  int
}

// meshPreparedTriangle 保存单个变换后的直接三角形及包围盒
type meshPreparedTriangle struct {
	vertices               [3]meshVertex
	minX, minY, maxX, maxY float64
	patch                  int
}

// MeshRasterOptions 保存网格采样的坐标、精度及并行配置
// Matrix将PDF页面坐标转换为毫米，Origin为输出块的毫米原点
type MeshRasterOptions struct {
	Matrix        pdfgo.Matrix
	Origin        pdfgo.Point
	DPI           float64
	Width, Height int
	Workers       int
	SoftMask      bool
}

// meshVertex 保存局部毫米坐标及原曲面参数
type meshVertex struct {
	point pdfgo.Point
	u, v  float64
}

// meshSample 保存采样点参数，patch为一基编号，零表示未覆盖
type meshSample struct {
	u, v  float64
	patch int
}

// MeshEdge 按坐标及颜色分量匹配相邻三角形的共享边，不改变顶点顺序
// 入参: previous 前一三角形, current 当前三角形
// 返回: int OFD共享边标志，零表示独立三角形
func MeshEdge(previous, current pdfgo.MeshTriangle) int {
	if previous.Points[2] != current.Points[1] || !slices.Equal(previous.Colors[2], current.Colors[1]) {
		return 0
	}
	for _, index := range [2]int{1, 0} {
		if previous.Points[index] == current.Points[0] && slices.Equal(previous.Colors[index], current.Colors[0]) {
			return 2 - index
		}
	}
	return 0
}

// PrepareMesh 预处理网格控制点与包围盒，避免预先展开全部细分三角形
// 入参: ctx 取消上下文, mesh 原始曲面, matrix 页面变换
// 返回: *MeshPrepared 预处理网格, error 取消或坐标错误
func PrepareMesh(ctx context.Context, mesh *pdfgo.MeshGradient, matrix pdfgo.Matrix) (*MeshPrepared, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	matrix = matrix.Mul(mesh.Matrix)
	prepared := &MeshPrepared{
		patches:   make([]meshPreparedPatch, len(mesh.Patches)),
		triangles: make([]meshPreparedTriangle, len(mesh.Triangles)),
	}
	for index, patch := range mesh.Patches {
		if index%CompositeTileSize == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		var out meshPreparedPatch
		out.index = index
		minX, minY := math.Inf(1), math.Inf(1)
		maxX, maxY := math.Inf(-1), math.Inf(-1)
		for i := range patch.Points {
			for j := range patch.Points[i] {
				pt := matrix.Apply(patch.Points[i][j])
				if !finite(pt.X) || !finite(pt.Y) {
					return nil, fmt.Errorf("invalid mesh patch coordinates")
				}
				out.points[i][j] = pt
				if pt.X < minX {
					minX = pt.X
				}
				if pt.X > maxX {
					maxX = pt.X
				}
				if pt.Y < minY {
					minY = pt.Y
				}
				if pt.Y > maxY {
					maxY = pt.Y
				}
			}
		}
		out.minX, out.minY, out.maxX, out.maxY = minX, minY, maxX, maxY
		prepared.patches[index] = out
	}
	for index, triangle := range mesh.Triangles {
		if index%CompositeTileSize == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		p0, p1, p2 := matrix.Apply(triangle.Points[0]), matrix.Apply(triangle.Points[1]), matrix.Apply(triangle.Points[2])
		if !finite(p0.X) || !finite(p0.Y) || !finite(p1.X) || !finite(p1.Y) || !finite(p2.X) || !finite(p2.Y) {
			return nil, fmt.Errorf("invalid mesh triangle coordinates")
		}
		prepared.triangles[index] = meshPreparedTriangle{
			vertices: [3]meshVertex{{p0, 0, 0}, {p1, 1, 0}, {p2, 0, 1}},
			minX:     math.Min(p0.X, math.Min(p1.X, p2.X)),
			minY:     math.Min(p0.Y, math.Min(p1.Y, p2.Y)),
			maxX:     math.Max(p0.X, math.Max(p1.X, p2.X)),
			maxY:     math.Max(p0.Y, math.Max(p1.Y, p2.Y)),
			patch:    len(mesh.Patches) + index,
		}
	}
	return prepared, nil
}

// MeshTriangles 按曲面二阶导数界自适应细分，按总补片预算限制单次全展开三角形总数
// 入参: ctx 取消上下文, mesh 原始曲面, matrix 页面变换, dpi 输出精度
// 返回: []MeshTriangle 细分结果, error 取消或坐标错误
func MeshTriangles(ctx context.Context, mesh *pdfgo.MeshGradient, matrix pdfgo.Matrix, dpi float64) ([]MeshTriangle, error) {
	matrix = matrix.Mul(mesh.Matrix)
	const maxTriangles = 1 << 22
	patchCount := len(mesh.Patches)
	maxStepsPerPatch := 2048.0
	if patchCount > 0 {
		budgetPerPatch := float64(maxTriangles) / float64(2*patchCount)
		if budgetPerPatch < 1 {
			budgetPerPatch = 1
		}
		maxStepsPerPatch = math.Min(maxStepsPerPatch, math.Max(1, math.Floor(math.Sqrt(budgetPerPatch))))
	}
	var triangles []MeshTriangle
	for index, patch := range mesh.Patches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for i := range patch.Points {
			for j := range patch.Points[i] {
				patch.Points[i][j] = matrix.Apply(patch.Points[i][j])
			}
		}
		var duu, dvv, duv float64
		for i := 0; i < 4; i++ {
			for j := 0; j < 4; j++ {
				a := patch.Points[i][j]
				if i < 2 {
					b, c := patch.Points[i+1][j], patch.Points[i+2][j]
					duu = math.Max(duu, 6*math.Hypot(c.X-2*b.X+a.X, c.Y-2*b.Y+a.Y))
				}
				if j < 2 {
					b, c := patch.Points[i][j+1], patch.Points[i][j+2]
					dvv = math.Max(dvv, 6*math.Hypot(c.X-2*b.X+a.X, c.Y-2*b.Y+a.Y))
				}
				if i < 3 && j < 3 {
					b, c, d := patch.Points[i+1][j], patch.Points[i][j+1], patch.Points[i+1][j+1]
					duv = math.Max(duv, 9*math.Hypot(d.X-b.X-c.X+a.X, d.Y-b.Y-c.Y+a.Y))
				}
			}
		}
		steps := math.Max(1, math.Ceil(math.Sqrt(8*(duu+dvv+2*duv)*dpi/25.4)))
		if !finite(steps) {
			return nil, fmt.Errorf("mesh subdivision exceeds platform integer range")
		}
		steps = math.Min(steps, maxStepsPerPatch)
		n := int(steps)
		row := make([]meshVertex, n+1)
		for i := range row {
			if i%CompositeTileSize == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			u := float64(i) / steps
			row[i] = meshVertex{patch.PointAt(u, 0), u, 0}
		}
		for j := 1; j <= n; j++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			v := float64(j) / steps
			left := meshVertex{patch.PointAt(0, v), 0, v}
			for i := 1; i <= n; i++ {
				if i%CompositeTileSize == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				u := float64(i) / steps
				right := meshVertex{patch.PointAt(u, v), u, v}
				a, b := row[i-1], row[i]
				triangles = append(triangles, MeshTriangle{[3]meshVertex{a, b, left}, index}, MeshTriangle{[3]meshVertex{b, right, left}, index})
				row[i-1], left = left, right
			}
			row[n] = left
		}
	}
	for index, triangle := range mesh.Triangles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vertices := [3]meshVertex{
			{matrix.Apply(triangle.Points[0]), 0, 0},
			{matrix.Apply(triangle.Points[1]), 1, 0},
			{matrix.Apply(triangle.Points[2]), 0, 1},
		}
		triangles = append(triangles, MeshTriangle{vertices, len(mesh.Patches) + index})
	}
	return triangles, nil
}

// meshSplitCubic 将三次贝塞尔曲线按中点一分为二
// 入参: p0 起点, p1 第一控制点, p2 第二控制点, p3 终点
// 返回: [4]pdfgo.Point 左半段控制点, [4]pdfgo.Point 右半段控制点
func meshSplitCubic(p0, p1, p2, p3 pdfgo.Point) ([4]pdfgo.Point, [4]pdfgo.Point) {
	m01 := pdfgo.Point{X: (p0.X + p1.X) * 0.5, Y: (p0.Y + p1.Y) * 0.5}
	m12 := pdfgo.Point{X: (p1.X + p2.X) * 0.5, Y: (p1.Y + p2.Y) * 0.5}
	m23 := pdfgo.Point{X: (p2.X + p3.X) * 0.5, Y: (p2.Y + p3.Y) * 0.5}
	m012 := pdfgo.Point{X: (m01.X + m12.X) * 0.5, Y: (m01.Y + m12.Y) * 0.5}
	m123 := pdfgo.Point{X: (m12.X + m23.X) * 0.5, Y: (m12.Y + m23.Y) * 0.5}
	mid := pdfgo.Point{X: (m012.X + m123.X) * 0.5, Y: (m012.Y + m123.Y) * 0.5}
	return [4]pdfgo.Point{p0, m01, m012, mid}, [4]pdfgo.Point{mid, m123, m23, p3}
}

// meshSplitPatchU 将双三次曲面沿u中点切分为左右两片
// 入参: p 曲面控制点网格
// 返回: [4][4]pdfgo.Point 左半片控制点, [4][4]pdfgo.Point 右半片控制点
func meshSplitPatchU(p [4][4]pdfgo.Point) ([4][4]pdfgo.Point, [4][4]pdfgo.Point) {
	var left, right [4][4]pdfgo.Point
	for j := range 4 {
		l, r := meshSplitCubic(p[0][j], p[1][j], p[2][j], p[3][j])
		for i := range 4 {
			left[i][j], right[i][j] = l[i], r[i]
		}
	}
	return left, right
}

// meshSplitPatchV 将双三次曲面沿v中点切分为上下两片
// 入参: p 曲面控制点网格
// 返回: [4][4]pdfgo.Point 下半片控制点, [4][4]pdfgo.Point 上半片控制点
func meshSplitPatchV(p [4][4]pdfgo.Point) ([4][4]pdfgo.Point, [4][4]pdfgo.Point) {
	var bottom, top [4][4]pdfgo.Point
	for i := range 4 {
		bottom[i], top[i] = meshSplitCubic(p[i][0], p[i][1], p[i][2], p[i][3])
	}
	return bottom, top
}

// rasterMeshTriangle 使用增量重心坐标将单个三角形光栅化到超采样网格
// 入参: grid 超采样网格, w 网格宽度, h 网格高度, scale 毫米到子像素缩放比例, originX 瓦片毫米原点X, originY 瓦片毫米原点Y, v0 顶点0, v1 顶点1, v2 顶点2, patchID 补片一基编号
func rasterMeshTriangle(grid []meshSample, w, h int, scale, originX, originY float64, v0, v1, v2 meshVertex, patchID int) {
	ax, ay := (v0.point.X-originX)*scale, (v0.point.Y-originY)*scale
	bx, by := (v1.point.X-originX)*scale, (v1.point.Y-originY)*scale
	dx, dy := (v2.point.X-originX)*scale, (v2.point.Y-originY)*scale
	denominator := (by-dy)*(ax-dx) + (dx-bx)*(ay-dy)
	if denominator == 0 {
		return
	}
	minX := math.Min(ax, math.Min(bx, dx))
	maxX := math.Max(ax, math.Max(bx, dx))
	minY := math.Min(ay, math.Min(by, dy))
	maxY := math.Max(ay, math.Max(by, dy))
	if maxX < 0 || maxY < 0 || minX >= float64(w) || minY >= float64(h) {
		return
	}
	startX := int(math.Max(0, math.Floor(minX)))
	startY := int(math.Max(0, math.Floor(minY)))
	endX := int(math.Min(float64(w), math.Ceil(maxX)))
	endY := int(math.Min(float64(h), math.Ceil(maxY)))
	invDen := 1 / denominator
	dl0x := (by - dy) * invDen
	dl0y := (dx - bx) * invDen
	dl1x := (dy - ay) * invDen
	dl1y := (ax - dx) * invDen
	px0, py0 := float64(startX)+0.5-dx, float64(startY)+0.5-dy
	l0Row := dl0x*px0 + dl0y*py0
	l1Row := dl1x*px0 + dl1y*py0
	for y := startY; y < endY; y++ {
		l0, l1 := l0Row, l1Row
		rowOff := y * w
		for x := startX; x < endX; x++ {
			l2 := 1 - l0 - l1
			if l0 >= -1e-10 && l1 >= -1e-10 && l2 >= -1e-10 {
				u := l0*v0.u + l1*v1.u + l2*v2.u
				if u < 0 {
					u = 0
				} else if u > 1 {
					u = 1
				}
				v := l0*v0.v + l1*v1.v + l2*v2.v
				if v < 0 {
					v = 0
				} else if v > 1 {
					v = 1
				}
				current := &grid[rowOff+x]
				if patchID > current.patch || patchID == current.patch && (v > current.v || v == current.v && u > current.u) {
					*current = meshSample{u, v, patchID}
				}
			}
			l0 += dl0x
			l1 += dl1x
		}
		l0Row += dl0y
		l1Row += dl1y
	}
}

// rasterMeshPatch 对落在当前瓦片内的双三次曲面按二阶导数与像素跨度自适应递归细分光栅化
// 入参: ctx 取消上下文, grid 超采样网格, w 网格宽度, h 网格高度, scale 毫米到子像素缩放比例, originX 瓦片毫米原点X, originY 瓦片毫米原点Y, tileMinX 瓦片左边界, tileMinY 瓦片下边界, tileMaxX 瓦片右边界, tileMaxY 瓦片上边界, flatLimit 平直度阈值, subPxMM 子像素毫米尺寸, pts 曲面控制点网格, u0 起始u参数, u1 结束u参数, v0 起始v参数, v1 结束v参数, patchID 补片一基编号, depth 当前递归深度, steps 递归步数计数
// 返回: error 取消错误
func rasterMeshPatch(ctx context.Context, grid []meshSample, w, h int, scale, originX, originY, tileMinX, tileMinY, tileMaxX, tileMaxY, flatLimit, subPxMM float64, pts [4][4]pdfgo.Point, u0, u1, v0, v1 float64, patchID, depth int, steps *int) error {
	*steps++
	if *steps&1023 == 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	minX, minY := pts[0][0].X, pts[0][0].Y
	maxX, maxY := minX, minY
	for i := range 4 {
		for j := range 4 {
			pt := pts[i][j]
			if pt.X < minX {
				minX = pt.X
			} else if pt.X > maxX {
				maxX = pt.X
			}
			if pt.Y < minY {
				minY = pt.Y
			} else if pt.Y > maxY {
				maxY = pt.Y
			}
		}
	}
	if maxX < tileMinX || minX > tileMaxX || maxY < tileMinY || minY > tileMaxY {
		return nil
	}
	split := depth < 11 && (maxX-minX > subPxMM || maxY-minY > subPxMM)
	var needU, needV bool
	if split {
		var duu2, dvv2, duv2 float64
		for i := range 4 {
			for j := range 4 {
				a := pts[i][j]
				if i < 2 {
					b, c := pts[i+1][j], pts[i+2][j]
					dx, dy := c.X-2*b.X+a.X, c.Y-2*b.Y+a.Y
					if d := dx*dx + dy*dy; d > duu2 {
						duu2 = d
					}
				}
				if j < 2 {
					b, c := pts[i][j+1], pts[i][j+2]
					dx, dy := c.X-2*b.X+a.X, c.Y-2*b.Y+a.Y
					if d := dx*dx + dy*dy; d > dvv2 {
						dvv2 = d
					}
				}
				if i < 3 && j < 3 {
					b, c, d := pts[i+1][j], pts[i][j+1], pts[i+1][j+1]
					dx, dy := d.X-b.X-c.X+a.X, d.Y-b.Y-c.Y+a.Y
					if v := dx*dx + dy*dy; v > duv2 {
						duv2 = v
					}
				}
			}
		}
		duu := 6 * math.Sqrt(duu2)
		dvv := 6 * math.Sqrt(dvv2)
		duv := 9 * math.Sqrt(duv2)
		needU = duu+duv > flatLimit
		needV = dvv+duv > flatLimit
	}
	if !needU && !needV {
		p00 := meshVertex{pts[0][0], u0, v0}
		p10 := meshVertex{pts[3][0], u1, v0}
		p01 := meshVertex{pts[0][3], u0, v1}
		p11 := meshVertex{pts[3][3], u1, v1}
		rasterMeshTriangle(grid, w, h, scale, originX, originY, p00, p10, p01, patchID)
		rasterMeshTriangle(grid, w, h, scale, originX, originY, p10, p11, p01, patchID)
		return nil
	}
	if needU && needV {
		um, vm := (u0+u1)*0.5, (v0+v1)*0.5
		left, right := meshSplitPatchU(pts)
		lb, lt := meshSplitPatchV(left)
		rb, rt := meshSplitPatchV(right)
		if err := rasterMeshPatch(ctx, grid, w, h, scale, originX, originY, tileMinX, tileMinY, tileMaxX, tileMaxY, flatLimit, subPxMM, lb, u0, um, v0, vm, patchID, depth+1, steps); err != nil {
			return err
		}
		if err := rasterMeshPatch(ctx, grid, w, h, scale, originX, originY, tileMinX, tileMinY, tileMaxX, tileMaxY, flatLimit, subPxMM, rb, um, u1, v0, vm, patchID, depth+1, steps); err != nil {
			return err
		}
		if err := rasterMeshPatch(ctx, grid, w, h, scale, originX, originY, tileMinX, tileMinY, tileMaxX, tileMaxY, flatLimit, subPxMM, lt, u0, um, vm, v1, patchID, depth+1, steps); err != nil {
			return err
		}
		return rasterMeshPatch(ctx, grid, w, h, scale, originX, originY, tileMinX, tileMinY, tileMaxX, tileMaxY, flatLimit, subPxMM, rt, um, u1, vm, v1, patchID, depth+1, steps)
	}
	if needU {
		um := (u0 + u1) * 0.5
		left, right := meshSplitPatchU(pts)
		if err := rasterMeshPatch(ctx, grid, w, h, scale, originX, originY, tileMinX, tileMinY, tileMaxX, tileMaxY, flatLimit, subPxMM, left, u0, um, v0, v1, patchID, depth+1, steps); err != nil {
			return err
		}
		return rasterMeshPatch(ctx, grid, w, h, scale, originX, originY, tileMinX, tileMinY, tileMaxX, tileMaxY, flatLimit, subPxMM, right, um, u1, v0, v1, patchID, depth+1, steps)
	}
	vm := (v0 + v1) * 0.5
	bottom, top := meshSplitPatchV(pts)
	if err := rasterMeshPatch(ctx, grid, w, h, scale, originX, originY, tileMinX, tileMinY, tileMaxX, tileMaxY, flatLimit, subPxMM, bottom, u0, u1, v0, vm, patchID, depth+1, steps); err != nil {
		return err
	}
	return rasterMeshPatch(ctx, grid, w, h, scale, originX, originY, tileMinX, tileMinY, tileMaxX, tileMaxY, flatLimit, subPxMM, top, u0, u1, vm, v1, patchID, depth+1, steps)
}

// MeshRasterPrepared 对预处理网格按当前瓦片包围盒裁剪并逐片流式光栅化采样
// 入参: ctx 取消上下文, mesh 原曲面, prepared 预处理网格, space 混合空间, converter 已准备的颜色转换, options 采样配置
// 返回: []ShadingPixel 颜色及覆盖, error 几何、颜色或取消错误
func MeshRasterPrepared(ctx context.Context, mesh *pdfgo.MeshGradient, prepared *MeshPrepared, space *pdfgo.ColorSpace, converter pdfgo.ColorConverter, options MeshRasterOptions) ([]ShadingPixel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	const samples = 4
	w, h := options.Width*samples, options.Height*samples
	grid := make([]meshSample, w*h)
	pxMM := 25.4 / options.DPI
	scale := float64(samples) / pxMM
	tileMinX, tileMinY := options.Origin.X, options.Origin.Y
	tileMaxX, tileMaxY := tileMinX+float64(options.Width)*pxMM, tileMinY+float64(options.Height)*pxMM
	flatLimit := pxMM / 8.0
	subPxMM := pxMM / float64(samples)
	steps := 0
	for i := range prepared.patches {
		p := &prepared.patches[i]
		if p.maxX < tileMinX || p.minX > tileMaxX || p.maxY < tileMinY || p.minY > tileMaxY {
			continue
		}
		if err := rasterMeshPatch(ctx, grid, w, h, scale, options.Origin.X, options.Origin.Y, tileMinX, tileMinY, tileMaxX, tileMaxY, flatLimit, subPxMM, p.points, 0, 1, 0, 1, p.index+1, 0, &steps); err != nil {
			return nil, err
		}
	}
	for i := range prepared.triangles {
		if i%CompositeTileSize == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		t := &prepared.triangles[i]
		if t.maxX < tileMinX || t.minX > tileMaxX || t.maxY < tileMinY || t.minY > tileMaxY {
			continue
		}
		rasterMeshTriangle(grid, w, h, scale, options.Origin.X, options.Origin.Y, t.vertices[0], t.vertices[1], t.vertices[2], t.patch+1)
	}
	return sampleMeshGrid(ctx, mesh, grid, w, scale, samples, space, converter, options)
}

// MeshPixels 对输出块执行四倍超采样，曲面重叠按后补片及较大v、u取值
// 入参: ctx 取消上下文, mesh 原曲面, triangles 已细分网格, space 混合空间, converter 已准备的颜色转换, options 采样配置
// 返回: []ShadingPixel 颜色及覆盖, error 几何、颜色或取消错误
func MeshPixels(ctx context.Context, mesh *pdfgo.MeshGradient, triangles []MeshTriangle, space *pdfgo.ColorSpace, converter pdfgo.ColorConverter, options MeshRasterOptions) ([]ShadingPixel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	const samples = 4
	w, h := options.Width*samples, options.Height*samples
	grid := make([]meshSample, w*h)
	scale := options.DPI / 25.4 * samples
	for _, triangle := range triangles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rasterMeshTriangle(grid, w, h, scale, options.Origin.X, options.Origin.Y, triangle.vertices[0], triangle.vertices[1], triangle.vertices[2], triangle.patch+1)
	}
	return sampleMeshGrid(ctx, mesh, grid, w, scale, samples, space, converter, options)
}

// sampleMeshGrid 对超采样网格执行颜色插值、边界裁切与箱式下采样
// 入参: ctx 取消上下文, mesh 原曲面, grid 超采样网格, w 网格宽度, scale 毫米到子像素缩放比例, samples 单轴超采样倍数, space 混合空间, converter 已准备的颜色转换, options 采样配置
// 返回: []ShadingPixel 颜色及覆盖, error 几何、颜色或取消错误
func sampleMeshGrid(ctx context.Context, mesh *pdfgo.MeshGradient, grid []meshSample, w int, scale float64, samples int, space *pdfgo.ColorSpace, converter pdfgo.ColorConverter, options MeshRasterOptions) ([]ShadingPixel, error) {
	pixels := make([]ShadingPixel, options.Width*options.Height)
	var background [4]float64
	if mesh.Background != nil {
		var err error
		background, err = converter.Convert(mesh.Background[:mesh.Space.Components()])
		if err != nil {
			return nil, err
		}
	}
	var inverse pdfgo.Matrix
	if mesh.Bounds != nil {
		var ok bool
		inverse, ok = options.Matrix.Mul(mesh.Matrix).Inverse()
		if !ok {
			return nil, fmt.Errorf("singular mesh bounds transform")
		}
	}
	sameSpace := mesh.Space != nil && space != nil && mesh.Space.Equal(space)
	directLinearPatch := mesh.Bounds == nil && mesh.Space != nil && mesh.Space.Components() > 1 && len(mesh.Patches) > 0 && len(mesh.Patches[0].Colors[0]) == mesh.Space.Components()
	sampleRows := func(start, end int) error {
		var colors map[[4]float64][4]float64
		if !sameSpace {
			colors = make(map[[4]float64][4]float64)
		}
		colorants := PrepareShadingColorants(pdfgo.Paint{Mesh: mesh}, space, options.SoftMask)
		var sampler *pdfgo.MeshSampler
		if colorants == nil {
			sampler = mesh.NewSampler()
		}
		useFastPixel := directLinearPatch && colorants == nil && samples == 4
		components := mesh.Space.Components()
		convertValues := func(values [4]float64) ([4]float64, error) {
			if sameSpace || colorants != nil {
				return values, nil
			}
			if cached, ok := colors[values]; ok {
				return cached, nil
			}
			source := values
			converted, err := converter.Convert(values[:components])
			if err != nil {
				return [4]float64{}, err
			}
			if len(colors) == 2*CompositeTileSize {
				clear(colors)
			}
			colors[source] = converted
			return converted, nil
		}
		for py := start; py < end; py++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			y0 := py * samples
			pixRowOff := py * options.Width
			for px := 0; px < options.Width; px++ {
				x0 := px * samples
				if useFastPixel {
					r0 := y0*w + x0
					r1 := r0 + w
					r2 := r1 + w
					r3 := r2 + w
					p0 := grid[r0].patch
					if p0 == 0 && mesh.Background == nil &&
						grid[r0+1].patch == 0 && grid[r0+2].patch == 0 && grid[r0+3].patch == 0 &&
						grid[r1].patch == 0 && grid[r1+1].patch == 0 && grid[r1+2].patch == 0 && grid[r1+3].patch == 0 &&
						grid[r2].patch == 0 && grid[r2+1].patch == 0 && grid[r2+2].patch == 0 && grid[r2+3].patch == 0 &&
						grid[r3].patch == 0 && grid[r3+1].patch == 0 && grid[r3+2].patch == 0 && grid[r3+3].patch == 0 {
						continue
					}
					if p0 > 0 && p0 <= len(mesh.Patches) &&
						grid[r0+1].patch == p0 && grid[r0+2].patch == p0 && grid[r0+3].patch == p0 &&
						grid[r1].patch == p0 && grid[r1+1].patch == p0 && grid[r1+2].patch == p0 && grid[r1+3].patch == p0 &&
						grid[r2].patch == p0 && grid[r2+1].patch == p0 && grid[r2+2].patch == p0 && grid[r2+3].patch == p0 &&
						grid[r3].patch == p0 && grid[r3+1].patch == p0 && grid[r3+2].patch == p0 && grid[r3+3].patch == p0 {
						uSum := grid[r0].u + grid[r0+1].u + grid[r0+2].u + grid[r0+3].u +
							grid[r1].u + grid[r1+1].u + grid[r1+2].u + grid[r1+3].u +
							grid[r2].u + grid[r2+1].u + grid[r2+2].u + grid[r2+3].u +
							grid[r3].u + grid[r3+1].u + grid[r3+2].u + grid[r3+3].u
						vSum := grid[r0].v + grid[r0+1].v + grid[r0+2].v + grid[r0+3].v +
							grid[r1].v + grid[r1+1].v + grid[r1+2].v + grid[r1+3].v +
							grid[r2].v + grid[r2+1].v + grid[r2+2].v + grid[r2+3].v +
							grid[r3].v + grid[r3+1].v + grid[r3+2].v + grid[r3+3].v
						values, err := sampler.ValuesAt(p0-1, uSum*0.0625, vSum*0.0625)
						if err != nil {
							return err
						}
						values, err = convertValues(values)
						if err != nil {
							return err
						}
						pixels[pixRowOff+px] = ShadingPixel{Values: [4]float64{values[0] * 16, values[1] * 16, values[2] * 16, values[3] * 16}, Shape: 16}
						continue
					}
				}
				pixel := &pixels[pixRowOff+px]
				for sy := y0; sy < y0+samples; sy++ {
					rowOff := sy * w
					for sx := x0; sx < x0+samples; sx++ {
						if box := mesh.Bounds; box != nil {
							point := inverse.Apply(pdfgo.Point{X: options.Origin.X + (float64(sx)+.5)/scale, Y: options.Origin.Y + (float64(sy)+.5)/scale})
							if point.X < box.XMin || point.X > box.XMax || point.Y < box.YMin || point.Y > box.YMax {
								continue
							}
						}
						sample := grid[rowOff+sx]
						if sample.patch == 0 && mesh.Background == nil {
							continue
						}
						values := background
						if sample.patch != 0 {
							var err error
							if colorants != nil {
								values, err = colorants.MeshAt(sample.patch-1, sample.u, sample.v)
							} else {
								values, err = sampler.ValuesAt(sample.patch-1, sample.u, sample.v)
							}
							if err != nil {
								return err
							}
							values, err = convertValues(values)
							if err != nil {
								return err
							}
						}
						for i, value := range values {
							pixel.Values[i] += value
						}
						pixel.Shape++
					}
				}
			}
		}
		return ctx.Err()
	}
	workers := min(max(1, options.Workers), max(1, options.Height/32))
	if workers == 1 {
		if err := sampleRows(0, options.Height); err != nil {
			return nil, err
		}
	} else {
		var group sync.WaitGroup
		errors := make([]error, workers)
		for worker := range workers {
			group.Go(func() {
				errors[worker] = sampleRows(worker*options.Height/workers, (worker+1)*options.Height/workers)
			})
		}
		group.Wait()
		for _, err := range errors {
			if err != nil {
				return nil, err
			}
		}
	}
	for i := range pixels {
		pixel := &pixels[i]
		if pixel.Shape == 0 {
			continue
		}
		for j := range pixel.Values {
			pixel.Values[j] /= pixel.Shape
		}
		pixel.Shape /= float64(samples * samples)
	}
	return pixels, nil
}
