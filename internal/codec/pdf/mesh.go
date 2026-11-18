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

// MeshTriangles 按曲面二阶导数界细分，将几何误差控制在输出像素的十六分之一内
// 入参: ctx 取消上下文, mesh 原始曲面, matrix 页面变换, dpi 输出精度
// 返回: []MeshTriangle 细分结果, error 取消或坐标错误
func MeshTriangles(ctx context.Context, mesh *pdfgo.MeshGradient, matrix pdfgo.Matrix, dpi float64) ([]MeshTriangle, error) {
	matrix = matrix.Mul(mesh.Matrix)
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
		steps = math.Min(steps, 2048)
		n := int(steps)
		if len(triangles)+2*n*n > 1<<22 {
			return nil, fmt.Errorf("mesh triangle count exceeds limit")
		}
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
		vertices := triangle.vertices
		for i := range vertices {
			vertices[i].point.X = (vertices[i].point.X - options.Origin.X) * scale
			vertices[i].point.Y = (vertices[i].point.Y - options.Origin.Y) * scale
		}
		a, b, d := vertices[0].point, vertices[1].point, vertices[2].point
		denominator := (b.Y-d.Y)*(a.X-d.X) + (d.X-b.X)*(a.Y-d.Y)
		if denominator == 0 {
			continue
		}
		minX, maxX := math.Min(a.X, math.Min(b.X, d.X)), math.Max(a.X, math.Max(b.X, d.X))
		minY, maxY := math.Min(a.Y, math.Min(b.Y, d.Y)), math.Max(a.Y, math.Max(b.Y, d.Y))
		if maxX < 0 || maxY < 0 || minX >= float64(w) || minY >= float64(h) {
			continue
		}
		endX := int(math.Min(float64(w), math.Ceil(maxX)))
		endY := int(math.Min(float64(h), math.Ceil(maxY)))
		for y := int(math.Max(0, math.Floor(minY))); y < endY; y++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for x := int(math.Max(0, math.Floor(minX))); x < endX; x++ {
				px, py := float64(x)+.5, float64(y)+.5
				l0 := ((b.Y-d.Y)*(px-d.X) + (d.X-b.X)*(py-d.Y)) / denominator
				l1 := ((d.Y-a.Y)*(px-d.X) + (a.X-d.X)*(py-d.Y)) / denominator
				l2 := 1 - l0 - l1
				if l0 < -1e-10 || l1 < -1e-10 || l2 < -1e-10 {
					continue
				}
				u := math.Max(0, math.Min(1, l0*vertices[0].u+l1*vertices[1].u+l2*vertices[2].u))
				v := math.Max(0, math.Min(1, l0*vertices[0].v+l1*vertices[1].v+l2*vertices[2].v))
				current := &grid[y*w+x]
				if triangle.patch+1 > current.patch || triangle.patch+1 == current.patch && (v > current.v || v == current.v && u > current.u) {
					*current = meshSample{u, v, triangle.patch + 1}
				}
			}
		}
	}
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
	sampleRows := func(start, end int) error {
		colors := make(map[[4]float64][4]float64)
		colorants := PrepareShadingColorants(pdfgo.Paint{Mesh: mesh}, space, options.SoftMask)
		var sampler *pdfgo.MeshSampler
		if colorants == nil {
			sampler = mesh.NewSampler()
		}
		for y := start * samples; y < end*samples; y++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			for x := 0; x < w; x++ {
				if box := mesh.Bounds; box != nil {
					point := inverse.Apply(pdfgo.Point{X: options.Origin.X + (float64(x)+.5)/scale, Y: options.Origin.Y + (float64(y)+.5)/scale})
					if point.X < box.XMin || point.X > box.XMax || point.Y < box.YMin || point.Y > box.YMax {
						continue
					}
				}
				sample := grid[y*w+x]
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
					if cached, ok := colors[values]; ok {
						values = cached
					} else if colorants == nil {
						source := values
						values, err = converter.Convert(values[:mesh.Space.Components()])
						if err != nil {
							return err
						}
						if len(colors) == 2*CompositeTileSize {
							clear(colors)
						}
						colors[source] = values
					}
				}
				pixel := &pixels[(y/samples)*options.Width+x/samples]
				for i, value := range values {
					pixel.Values[i] += value
				}
				pixel.Shape++
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
		pixel.Shape /= samples * samples
	}
	return pixels, nil
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
