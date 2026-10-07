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

import "github.com/xiaoqidun/pdfgo"

// pdfThreeDRenderMode 将PDF标准绘制模式映射到通用几何绘制参数
// 入参: source 三维参数, options 绘制选项
// 返回: error 未支持的模式错误
func pdfThreeDRenderMode(source pdfgo.ThreeD, options *U3DRenderOptions) error {
	mode := source.Presentation.RenderMode
	if mode == nil {
		return nil
	}
	switch mode.Subtype {
	case "Solid":
	case "Transparent":
		options.Opacity = &mode.Opacity
	case "SolidWireframe", "TransparentWireframe":
		options.Mode, options.AuxiliaryColor = U3DRenderSolidWireframe, &mode.AuxiliaryColor
		if mode.Subtype == "TransparentWireframe" {
			options.Opacity = &mode.Opacity
		}
	case "Wireframe":
		options.Mode, options.AuxiliaryColor = U3DRenderWireframe, &mode.AuxiliaryColor
	case "ShadedWireframe":
		options.Mode = U3DRenderShadedWireframe
	case "HiddenWireframe":
		options.Mode, options.AuxiliaryColor = U3DRenderHiddenWireframe, &mode.AuxiliaryColor
	case "Vertices":
		options.Mode, options.AuxiliaryColor = U3DRenderVertices, &mode.AuxiliaryColor
	case "ShadedVertices":
		options.Mode = U3DRenderShadedVertices
	case "BoundingBox":
		options.Mode, options.AuxiliaryColor = U3DRenderBoundingBox, &mode.AuxiliaryColor
	case "TransparentBoundingBox", "TransparentBoundingBoxOutline":
		options.Mode = U3DRenderBoundingBoxFaces
		options.FaceColor, options.Opacity = mode.FaceColor, &mode.Opacity
		if mode.Subtype == "TransparentBoundingBoxOutline" {
			options.Mode, options.AuxiliaryColor = U3DRenderBoundingBoxOutline, &mode.AuxiliaryColor
		}
	case "Illustration":
		options.Mode = U3DRenderIllustration
		options.AuxiliaryColor, options.FaceColor, options.CreaseAngle = &mode.AuxiliaryColor, mode.FaceColor, &mode.CreaseAngle
	case "SolidOutline", "ShadedIllustration":
		options.Mode = U3DRenderSolidOutline
		if mode.Subtype == "ShadedIllustration" {
			options.Mode = U3DRenderShadedIllustration
		}
		options.AuxiliaryColor, options.CreaseAngle = &mode.AuxiliaryColor, &mode.CreaseAngle
	default:
		return &pdfgo.UnsupportedError{Feature: "3D render mode " + string(mode.Subtype)}
	}
	if mode.AnnotationBackground && (mode.Subtype == "Illustration" || mode.Subtype == "TransparentBoundingBox" || mode.Subtype == "TransparentBoundingBoxOutline") {
		options.FaceColor = source.AnnotationBackground
		if source.AnnotationBackground == nil {
			zero := 0.0
			options.Opacity = &zero
		}
	}
	return nil
}
