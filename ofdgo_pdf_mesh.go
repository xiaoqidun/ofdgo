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
	"runtime"
	"sync"

	"github.com/xiaoqidun/pdfgo"
)

// pdfMeshVertex 保存局部毫米坐标及原曲面参数
type pdfMeshVertex struct {
	point pdfgo.Point
	u, v  float64
}

// pdfMeshTriangle 保存按输出精度细分的曲面网格
type pdfMeshTriangle struct {
	vertices [3]pdfMeshVertex
	patch    int
}

// pdfMeshKey 区分网格及其输出混合空间
type pdfMeshKey struct {
	mesh       *pdfgo.MeshGradient
	space      *pdfgo.ColorSpace
	conversion pdfgo.ColorConversion
}

// pdfMeshSample 保存采样点参数，patch为一基编号，零表示未覆盖
type pdfMeshSample struct {
	u, v  float64
	patch int
}

// meshColor 将线性三角网格映射为OFD高洛德渐变，保留顶点和覆盖顺序
// 入参: paint PDF网格画刷, box 对象边界
// 返回: *FillColor OFD渐变, error 不可线性表达的颜色或坐标错误
func (p *pdfImporter) meshColor(paint pdfgo.Paint, box Box) (*FillColor, error) {
	if err := pdfGradientError(paint); err != nil {
		return nil, err
	}
	mesh := paint.Mesh
	matrix := p.matrix.Mul(mesh.Matrix)
	shading := &GouraudShd{}
	icc := ""
	if pdfICCTransfer(mesh.Space) {
		var err error
		icc, err = p.iccColorSpace(mesh.Space, mesh.Intent)
		if err != nil {
			return nil, err
		}
	}
	for _, triangle := range mesh.Triangles {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		for i, point := range triangle.Points {
			point = matrix.Apply(point)
			if !finite(point.X) || !finite(point.Y) {
				return nil, fmt.Errorf("nonfinite mesh vertex")
			}
			values := triangle.Colors[i]
			if icc != "" {
				value, err := pdfICCValues(values, mesh.Space.Components())
				if err != nil {
					return nil, err
				}
				shading.Point = append(shading.Point, ShdPoint{X: point.X - box.X, Y: point.Y - box.Y, Color: ShdColor{Value: value, ColorSpace: icc}})
				continue
			}
			rgb, err := mesh.Space.RGB(values[:mesh.Space.Components()], mesh.Intent)
			if err != nil {
				return nil, err
			}
			color := pdfgo.Paint{RGB: rgb, Alpha: 1}
			if mesh.Space.Model == "DeviceCMYK" && !mesh.Space.Calibrated() {
				color.CMYK = (*[4]float64)(values)
			}
			converted := p.color(color)
			shading.Point = append(shading.Point, ShdPoint{X: point.X - box.X, Y: point.Y - box.Y, Color: ShdColor{Value: converted.Value, ColorSpace: converted.ColorSpace}})
		}
	}
	color := p.color(paint)
	color.Value = ""
	color.GouraudShd = shading
	return color, nil
}

// meshTriangles 按曲面二阶导数界细分，将几何误差控制在输出像素的十六分之一内
// 入参: mesh 原始曲面
// 返回: []pdfMeshTriangle 细分结果, error 取消或坐标错误
func (p *pdfImporter) meshTriangles(mesh *pdfgo.MeshGradient) ([]pdfMeshTriangle, error) {
	matrix := p.matrix.Mul(mesh.Matrix)
	var triangles []pdfMeshTriangle
	for index, patch := range mesh.Patches {
		if err := p.ctx.Err(); err != nil {
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
		steps := math.Max(1, math.Ceil(math.Sqrt(8*(duu+dvv+2*duv)*p.rasterDPI/25.4)))
		if !finite(steps) || steps >= math.Sqrt(float64(int(^uint(0)>>1)/2)) {
			return nil, fmt.Errorf("mesh subdivision exceeds platform integer range")
		}
		n := int(steps)
		row := make([]pdfMeshVertex, n+1)
		for i := range row {
			if i%pdfCompositeTileSize == 0 {
				if err := p.ctx.Err(); err != nil {
					return nil, err
				}
			}
			u := float64(i) / steps
			row[i] = pdfMeshVertex{patch.PointAt(u, 0), u, 0}
		}
		for j := 1; j <= n; j++ {
			if err := p.ctx.Err(); err != nil {
				return nil, err
			}
			v := float64(j) / steps
			left := pdfMeshVertex{patch.PointAt(0, v), 0, v}
			for i := 1; i <= n; i++ {
				u := float64(i) / steps
				right := pdfMeshVertex{patch.PointAt(u, v), u, v}
				a, b := row[i-1], row[i]
				triangles = append(triangles, pdfMeshTriangle{[3]pdfMeshVertex{a, b, left}, index}, pdfMeshTriangle{[3]pdfMeshVertex{b, right, left}, index})
				row[i-1], left = left, right
			}
			row[n] = left
		}
	}
	for index, triangle := range mesh.Triangles {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		vertices := [3]pdfMeshVertex{
			{matrix.Apply(triangle.Points[0]), 0, 0},
			{matrix.Apply(triangle.Points[1]), 1, 0},
			{matrix.Apply(triangle.Points[2]), 0, 1},
		}
		triangles = append(triangles, pdfMeshTriangle{vertices, len(mesh.Patches) + index})
	}
	return triangles, nil
}

// mesh 对当前图块执行四倍超采样，曲面重叠按后补片及较大v、u取值
// 入参: mesh 原曲面, space 输出混合空间, conversion 设备转换函数
// 返回: []pdfShadingPixel 颜色及覆盖, error 几何或颜色错误
func (c *pdfCompositor) mesh(mesh *pdfgo.MeshGradient, space *pdfgo.ColorSpace, conversion pdfgo.ColorConversion) ([]pdfShadingPixel, error) {
	if err := c.importer.ctx.Err(); err != nil {
		return nil, err
	}
	key := pdfMeshKey{mesh, space, conversion}
	if pixels, ok := c.meshes[key]; ok {
		return pixels, nil
	}
	converter, err := space.PrepareConversion(mesh.Space, mesh.Intent, conversion)
	if err != nil {
		return nil, err
	}
	triangles, ok := c.cache.meshes[mesh]
	if !ok {
		var err error
		triangles, err = c.importer.meshTriangles(mesh)
		if err != nil {
			return nil, err
		}
		if c.cache.meshes == nil {
			c.cache.meshes = map[*pdfgo.MeshGradient][]pdfMeshTriangle{}
		}
		c.cache.meshes[mesh] = triangles
	}
	const samples = 4
	w, h := c.width*samples, c.height*samples
	grid := make([]pdfMeshSample, w*h)
	scale := c.importer.rasterDPI / 25.4 * samples
	for _, triangle := range triangles {
		if err := c.importer.ctx.Err(); err != nil {
			return nil, err
		}
		vertices := triangle.vertices
		for i := range vertices {
			vertices[i].point.X = (vertices[i].point.X - c.box.X) * scale
			vertices[i].point.Y = (vertices[i].point.Y - c.box.Y) * scale
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
			if err := c.importer.ctx.Err(); err != nil {
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
					*current = pdfMeshSample{u, v, triangle.patch + 1}
				}
			}
		}
	}
	pixels := make([]pdfShadingPixel, c.width*c.height)
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
		inverse, ok = c.importer.matrix.Mul(mesh.Matrix).Inverse()
		if !ok {
			return nil, fmt.Errorf("singular mesh bounds transform")
		}
	}
	sampleRows := func(start, end int) error {
		colors := make(map[[4]float64][4]float64)
		sampler := mesh.NewSampler()
		for y := start * samples; y < end*samples; y++ {
			if err := c.importer.ctx.Err(); err != nil {
				return err
			}
			for x := 0; x < w; x++ {
				if box := mesh.Bounds; box != nil {
					point := inverse.Apply(pdfgo.Point{X: c.box.X + (float64(x)+.5)/scale, Y: c.box.Y + (float64(y)+.5)/scale})
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
					values, err = sampler.ValuesAt(sample.patch-1, sample.u, sample.v)
					if err != nil {
						return err
					}
					if cached, ok := colors[values]; ok {
						values = cached
					} else {
						source := values
						values, err = converter.Convert(values[:mesh.Space.Components()])
						if err != nil {
							return err
						}
						if len(colors) == 2*pdfCompositeTileSize {
							clear(colors)
						}
						colors[source] = values
					}
				}
				pixel := &pixels[(y/samples)*c.width+x/samples]
				for i, value := range values {
					pixel.values[i] += value
				}
				pixel.shape++
			}
		}
		return c.importer.ctx.Err()
	}
	workers := min(runtime.GOMAXPROCS(0), 4, max(1, c.height/32))
	if workers == 1 {
		if err := sampleRows(0, c.height); err != nil {
			return nil, err
		}
	} else {
		var group sync.WaitGroup
		errors := make([]error, workers)
		for worker := range workers {
			group.Go(func() {
				errors[worker] = sampleRows(worker*c.height/workers, (worker+1)*c.height/workers)
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
		if pixel.shape == 0 {
			continue
		}
		for j := range pixel.values {
			pixel.values[j] /= pixel.shape
		}
		pixel.shape /= samples * samples
	}
	if c.meshes == nil {
		c.meshes = map[pdfMeshKey][]pdfShadingPixel{}
	}
	c.meshes[key] = pixels
	return pixels, nil
}
