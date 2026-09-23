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

import "fmt"

// ggNativeStroke 判断GG当前可保真处理的描边范围
// 入参: command 描边指令, scale 每毫米像素数
// 返回: bool 是否可使用原生描边
func ggNativeStroke(command RasterCommand, scale float64) bool {
	if command.Paint.Gradient != nil && command.Stroke.Width*scale < 1 {
		return false
	}
	joins := command.Stroke.Join == "" || command.Stroke.Join == "Miter"
	dashed := false
	for _, value := range command.Stroke.Dashes {
		dashed = dashed || value > 0
	}
	segments := 0
	for _, segment := range command.Path {
		switch segment.Verb {
		case RasterMove:
			segments = 0
		case RasterLine, RasterQuad, RasterCubic:
			segments++
			if joins && segments > 1 {
				return false
			}
			if dashed && segment.Verb != RasterLine {
				return false
			}
		case RasterClose:
			if dashed || joins {
				return false
			}
		}
	}
	return true
}

// expandStroke 使用显式几何提供者展开GG尚不可靠的描边，不切换光栅后端
// 入参: command 描边指令
// 返回: RasterCommand 等价填充指令, error 能力或几何错误
func (b GGBackend) expandStroke(command RasterCommand) (RasterCommand, error) {
	if b.StrokeGeometry == nil {
		return RasterCommand{}, fmt.Errorf("GG stroke geometry: %w", ErrBackendUnavailable)
	}
	values := command.Transform
	matrix := Matrix{a: values[0], b: values[1], c: values[2], d: values[3], e: values[4], f: values[5]}
	path, err := b.StrokeGeometry.Transform(geometryFromRaster(command.Path), matrix)
	if err != nil {
		return RasterCommand{}, err
	}
	path, err = b.StrokeGeometry.Stroke(path, *command.Stroke)
	if err != nil {
		return RasterCommand{}, err
	}
	path, err = path.Curves(.0001)
	if err != nil {
		return RasterCommand{}, err
	}
	if command.Paint.Gradient != nil {
		inverse, ok := matrix.Invert()
		if !ok {
			return RasterCommand{}, fmt.Errorf("invalid stroke gradient transform")
		}
		path, err = b.StrokeGeometry.Transform(path, inverse)
		if err != nil {
			return RasterCommand{}, err
		}
	} else {
		command.Transform = RasterMatrix{1, 0, 0, 1, 0, 0}
	}
	command.Path = make([]RasterSegment, len(path))
	for i, s := range path {
		verb := RasterVerb(s.Verb)
		if s.Verb == GeometryClose {
			verb = RasterClose
		}
		command.Path[i] = RasterSegment{Verb: verb, End: RasterPoint{s.End.X, s.End.Y}, Control1: RasterPoint{s.Control1.X, s.Control1.Y}, Control2: RasterPoint{s.Control2.X, s.Control2.Y}}
	}
	command.Stroke = nil
	command.EvenOdd = false
	return command, nil
}
