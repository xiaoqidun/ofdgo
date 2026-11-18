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
	"context"
	"image"

	basethreed "github.com/xiaoqidun/ofdgo/internal/base/threed"
)

// U3DOptions 控制U3D解码规模，MaxDecodedBytes为0时采用256MiB上限
type U3DOptions = basethreed.U3DOptions

// U3DModel 保存独立于PDF的U3D场景及动作数据，不执行动作或读取外部文件
type U3DModel = basethreed.U3DModel

// U3DMetadata 保留块所属名称与原始元数据，数据不与输入共享存储
type U3DMetadata = basethreed.U3DMetadata

// U3DNode 保存组、模型、光源或视图节点，父节点矩阵按列存储
type U3DNode = basethreed.U3DNode

// U3DParent 保存父节点名称及局部到父节点的变换，空名称引用默认节点
type U3DParent = basethreed.U3DParent

// U3DMaterial 保存材质参数，Attributes各位决定相应参数是否生效
type U3DMaterial = basethreed.U3DMaterial

// U3DShader 保存材质与八个纹理通道，Textures中的空项表示未启用
type U3DShader = basethreed.U3DShader

// U3DLight 保存环境、方向、点或聚光资源，Kind依次为0至3
type U3DLight = basethreed.U3DLight

// U3DAnimation 保存ECMA-363第9.7.3节的动画修饰器，Index为链内位置
// Attributes的1、2、4、8分别表示启动、锁定根骨骼、单轨播放和动作间平滑过渡
// TimeScale为整体时间倍率，BlendTime以毫秒计；Motions为空时使用无轨道的默认动作
type U3DAnimation = basethreed.U3DAnimation

// U3DAnimationMotion 保存动画中的动作引用，空名称引用默认动作
// Attributes的1、2分别表示循环和同步结束，TimeOffset以毫秒计，TimeScale为动作时间倍率
type U3DAnimationMotion = basethreed.U3DAnimationMotion

// U3DDirectionalLight 定义无限远光源，Direction为发射方向，CameraRelative使用相机坐标
type U3DDirectionalLight = basethreed.U3DDirectionalLight

// U3DLighting 替换场景灯光，空灯光列表表示无光源，DiffuseOffset加到受光材质的漫反射颜色
// 颜色及漫反射增量的各分量取值为0到1；不修改材质自发光或不受光着色器
type U3DLighting = basethreed.U3DLighting

// U3DMesh 保存静态三角面、线段和点的共享属性数组，索引对应原始作者几何
type U3DMesh = basethreed.U3DMesh

// U3DShading 描述图元使用的颜色及纹理坐标，纹理维数依层排列
type U3DShading = basethreed.U3DShading

// U3DFace 保存着色描述索引和依原始绕序排列的三个角点
type U3DFace = basethreed.U3DFace

// U3DLine 保存着色描述及线段两端的独立属性索引
type U3DLine = basethreed.U3DLine

// U3DPoint 保存点集着色描述及独立属性索引
type U3DPoint = basethreed.U3DPoint

// U3DCorner 保存各属性索引，未启用的属性不可用于访问数组
type U3DCorner = basethreed.U3DCorner

// U3DRenderMode 定义静态几何的呈现方式，边线和顶点使用单像素覆盖
type U3DRenderMode = basethreed.U3DRenderMode

const (
	U3DRenderSolid              = basethreed.U3DRenderSolid              // 实体
	U3DRenderSolidWireframe     = basethreed.U3DRenderSolidWireframe     // 实体线框
	U3DRenderWireframe          = basethreed.U3DRenderWireframe          // 线框
	U3DRenderShadedWireframe    = basethreed.U3DRenderShadedWireframe    // 着色线框
	U3DRenderHiddenWireframe    = basethreed.U3DRenderHiddenWireframe    // 隐藏线框
	U3DRenderVertices           = basethreed.U3DRenderVertices           // 顶点
	U3DRenderShadedVertices     = basethreed.U3DRenderShadedVertices     // 着色顶点
	U3DRenderBoundingBox        = basethreed.U3DRenderBoundingBox        // 包围盒线框
	U3DRenderBoundingBoxFaces   = basethreed.U3DRenderBoundingBoxFaces   // 包围盒面
	U3DRenderBoundingBoxOutline = basethreed.U3DRenderBoundingBoxOutline // 包围盒面及线框
	U3DRenderIllustration       = basethreed.U3DRenderIllustration       // 插图
	U3DRenderSolidOutline       = basethreed.U3DRenderSolidOutline       // 实体轮廓
	U3DRenderShadedIllustration = basethreed.U3DRenderShadedIllustration // 着色插图
)

// U3DMotion 保存骨骼或节点动作轨道，解码不执行动画
type U3DMotion = basethreed.U3DMotion

// U3DMotionTrack 保存命名轨道及其关键帧，保持文件中的时间与帧次序
type U3DMotionTrack = basethreed.U3DMotionTrack

// U3DKeyFrame 保存关键帧时间及相对父项的变换，Rotation按W、X、Y、Z排列
type U3DKeyFrame = basethreed.U3DKeyFrame

// U3DCamera 定义沿正Z方向观察的相机，ScaleX和ScaleY为投影到像素的倍率
// ToWorld按列存储，全零表示单位矩阵；Far为0且ClipFar为假时不设置远裁剪面
// ShiftX和ShiftY为投影原点相对视口中心的像素偏移，向右及向下为正
type U3DCamera = basethreed.U3DCamera

// U3DRenderOptions 控制静态绘制，指定Camera时使用整个输出视口
// Camera为空时使用View指定的节点或首个视图节点，Instance指定其父路径实例
// Lighting为空时沿用模型灯光，非空时替换全部模型灯光
// Opacity为空时沿用着色器混合，非空时叠加面片、着色边点及包围盒轮廓的透明度
// 纹理合成后先应用节点透明度，再应用Opacity
// Mode控制绘制方式，AuxiliaryColor用于固定颜色的边点，FaceColor用于包围盒面及插图面，空值分别使用黑色和背景色
// CreaseAngle为轮廓夹角，单位为度，空值使用45；着色插图以漫反射色的1/4补充自发光
// Nodes按名称覆盖节点，重复名称使用最后一项，每次绘制均从原场景开始
// Time以秒指定动画采样时刻，零值为初始时刻，不随实际时钟推进
// ResolveTexture提供外部纹理，空值不访问外部资源；回调自身的内存与超时由调用方控制
// Background为空时使用白色；缓存与实例上限为0时分别使用256MiB和65536，不含输入及运行时开销
type U3DRenderOptions = basethreed.U3DRenderOptions

// U3DTrackState 指定动作资源、轨道和采样时间，Time与关键帧使用相同单位
// Loop将时间循环到首末帧之间，未启用时停留在端点，不自动解释动画修饰器的时间参数
type U3DTrackState = basethreed.U3DTrackState

// U3DCrossSection 定义世界坐标中的剖切面，AxisU与AxisV须为非零正交方向
// 两轴叉积指向被裁去的一侧；平面方形边长取静态场景包围盒对角线
// Opacity为0时不显示平面，Intersection为空时不显示交线，颜色分量范围为0到1
type U3DCrossSection = basethreed.U3DCrossSection

// U3DTextureLayer 保存单个着色器纹理通道，矩阵按列排列
// Blend依次以0至3表示相乘、相加、替换及线性混合，BlendSource为1时使用BlendConstant
// Mode依次以0至4表示模型坐标、平面、圆柱、球面及反射映射
// Alpha控制是否使用纹理透明度，Repeat低两位分别启用横向及纵向平铺
// 平面映射直接取参考位置的XY；圆柱以Z为纵坐标，圆柱与球面的经线从正X轴朝正Y轴递增
// 球面纬度从正Z极到负Z极映射为0至1；反射映射使用相机空间球面环境图，图像纵坐标向下
type U3DTextureLayer = basethreed.U3DTextureLayer

// U3DBone 保存相对父骨骼的初始位姿，Orientation按W、X、Y、Z排列
// Attributes标志决定链接、关节字段是否存在，以及各轴旋转约束是否启用和限幅
// RotationConstraints按X、Y、Z轴排列，每项依次为上限、下限；未指定骨骼姿态时不变形
type U3DBone = basethreed.U3DBone

// U3DBoneJoint 保存骨骼局部坐标系内关节椭圆的中心偏移和两轴尺寸
type U3DBoneJoint = basethreed.U3DBoneJoint

// U3DBoneState 按骨骼名称指定相对父骨骼末端的姿态，根骨骼相对模型原点
// Frame可由动作轨道采样得到，Time不参与变形；单位缩放需显式设为[3]float32{1, 1, 1}
// 未覆盖项沿用文件或动画求值后的局部姿态，重复名称使用最后一项，不施加逆运动学约束
type U3DBoneState = basethreed.U3DBoneState

// U3DNodeState 按名称覆盖节点，未指定透明度或可见性时沿父路径继承
// Opacity范围为0到1，采用标准透明混合；Matrix为相对父节点的列式仿射矩阵
// 显式Opacity覆盖材质和纹理合成的透明度，之后仍应用视图附加透明度
// 未匹配的名称不影响场景，Matrix和Motion均为空时保留每条父路径的原始变换
// Motion以指定轨道替换节点变换，与Matrix互斥，不驱动骨架；显式变换优先于动画修饰器
// Bones覆盖动画求值后的指定骨骼姿态，未指定项沿用当前局部变换，不修改共享几何
type U3DNodeState = basethreed.U3DNodeState

// U3DTexture 保存纹理尺寸、图像类型及各通道的图像数据
type U3DTexture = basethreed.U3DTexture

// U3DTextureImage 保存压缩格式、通道位图及完整压缩数据，外部图像仅保留候选URL
// Compression依次以1至4表示JPEG-24、PNG、JPEG-8和TIFF，ByteCount仅用于内置图像
type U3DTextureImage = basethreed.U3DTextureImage

// U3DTextureResolver 由调用方按原始URL读取外部纹理，地址解析、访问策略和读取限额由调用方控制
// 返回的数据在本次绘制结束前不得修改；失败时尝试下一候选地址，取消及超时错误立即返回
// 入参: ctx 取消上下文, url 绝对或相对地址
// 返回: []byte 压缩图像数据, error 读取错误
type U3DTextureResolver = basethreed.U3DTextureResolver

// U3DView 保存视图节点的投影、裁剪、视口及叠加层参数
type U3DView = basethreed.U3DView

// U3DViewLayer 保存背景或覆盖纹理，注册点使用纹理像素，旋转使用弧度
type U3DViewLayer = basethreed.U3DViewLayer

// U3DViewResource 保存视图的绘制轮次，各轮独立指定根节点及雾效
type U3DViewResource = basethreed.U3DViewResource

// U3DViewPass 保存单轮根节点与雾效，Attributes最低位启用雾效
type U3DViewPass = basethreed.U3DViewPass

// U3DBoneWeight 保存某骨骼对一个位置的影响系数
type U3DBoneWeight = basethreed.U3DBoneWeight

// U3DBoneWeights 保存修饰链中的权重，Index为链内位置，Attributes的1、2、4分别表示网格、线集、点集
// 第i个位置的权重为Weights[Offsets[i]:Offsets[i+1]]，空区间表示没有骨骼影响
type U3DBoneWeights = basethreed.U3DBoneWeights

// DecodeU3D 按ECMA-363解码静态几何、骨架、场景节点及材质、纹理图像等资源
// 保留动作轨道和动画修饰器，不在解码时求值；绘制能力由RenderU3D另行校验
// 渐进网格尚未支持
// 点集球面预测暂限不超过两个非对跖方向，不推测多方向合并次序
// 入参: ctx 取消上下文, data U3D数据, options 解码限制
// 返回: *U3DModel 独立场景数据, error 格式、能力、预算或取消错误
func DecodeU3D(ctx context.Context, data []byte, options U3DOptions) (*U3DModel, error) {
	return basethreed.DecodeU3D(ctx, data, options)
}

// RenderU3D 绘制几何及视图纹理层，返回指定时刻的不透明图像，不执行脚本
// 可通过节点覆盖指定动作轨道与采样时间，逐次生成独立姿态，不修改原模型
// 显式骨骼姿态按权重变换位置和法线，暂不生成骨骼链接或推导缺失权重
// 动画修饰器支持单动作的节点和骨骼求值，尚未实现的动作衔接返回明确错误
// 几何纹理支持模型坐标与程序化映射，参考形状约定见U3DTextureLayer
// 纹理按需解码并复用，不自动访问外部图像
// 采用逐顶点光照及逐像素深度混合，空名称引用标准默认资源
// 缺失几何、材质和着色器采用默认资源，缺失的非空光源引用不产生光照
// 缺失纹理返回错误，空纹理名称使用标准默认图像
// 仅绘制默认根的后代，非空父节点引用缺失时不展开该父路径
// 入参: ctx 取消上下文, model 场景, options 视图、输出与资源限制
// 返回: *image.RGBA 图像, error 参数、能力、预算或取消错误
func RenderU3D(ctx context.Context, model *U3DModel, options U3DRenderOptions) (*image.RGBA, error) {
	return basethreed.RenderU3D(ctx, model, options)
}
