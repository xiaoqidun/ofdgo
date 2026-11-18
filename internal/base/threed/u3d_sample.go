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

package threed

import (
	"context"
	"fmt"
	"math"
)

// U3DTrackState 指定动作资源、轨道和采样时间，Time与关键帧使用相同单位
// Loop将时间循环到首末帧之间，未启用时停留在端点，不自动解释动画修饰器的时间参数
type U3DTrackState struct {
	Motion, Track string
	Time          float64
	Loop          bool
}

// Sample 按时间采样轨道，不修改原始帧或按帧数量分配临时数组
// 位移和缩放采用线性插值，旋转归一化后采用最短弧球面插值
// 允许帧乱序，同一时刻取文件中最后一帧；循环区间不包含末端，单帧轨道始终返回该帧
// 入参: ctx 取消上下文, time 与关键帧同单位的时间, loop 是否循环
// 返回: U3DKeyFrame 采样姿态, error 空轨道、数值或取消错误
func (track U3DMotionTrack) Sample(ctx context.Context, time float64, loop bool) (U3DKeyFrame, error) {
	if ctx == nil || !finite(time) || len(track.Frames) == 0 {
		return U3DKeyFrame{}, fmt.Errorf("invalid U3D motion sample")
	}
	first, last := 0, 0
	for index, frame := range track.Frames {
		if index%256 == 0 {
			if err := ctx.Err(); err != nil {
				return U3DKeyFrame{}, err
			}
		}
		if !finite(float64(frame.Time)) {
			return U3DKeyFrame{}, fmt.Errorf("invalid U3D key frame time")
		}
		if frame.Time <= track.Frames[first].Time {
			first = index
		}
		if frame.Time >= track.Frames[last].Time {
			last = index
		}
	}
	minimum, maximum := float64(track.Frames[first].Time), float64(track.Frames[last].Time)
	if loop && maximum > minimum {
		time = math.Mod(time-minimum, maximum-minimum)
		if time < 0 {
			time += maximum - minimum
		}
		time += minimum
	} else {
		time = max(minimum, min(time, maximum))
	}
	lower, upper := first, last
	for index, frame := range track.Frames {
		if index%256 == 0 {
			if err := ctx.Err(); err != nil {
				return U3DKeyFrame{}, err
			}
		}
		value := float64(frame.Time)
		if value <= time && frame.Time >= track.Frames[lower].Time {
			lower = index
		}
		if value > time && frame.Time <= track.Frames[upper].Time {
			upper = index
		}
	}
	a, b := track.Frames[lower], track.Frames[upper]
	if float64(a.Time) == time {
		b = a
	}
	qa, err := a.quaternion()
	if err != nil {
		return U3DKeyFrame{}, err
	}
	qb, err := b.quaternion()
	if err != nil {
		return U3DKeyFrame{}, err
	}
	fraction := float64(0)
	if b.Time != a.Time {
		fraction = (time - float64(a.Time)) / (float64(b.Time) - float64(a.Time))
	}
	result := U3DKeyFrame{Time: float32(time)}
	for i := range result.Displacement {
		result.Displacement[i] = float32(float64(a.Displacement[i])*(1-fraction) + float64(b.Displacement[i])*fraction)
		result.Scale[i] = float32(float64(a.Scale[i])*(1-fraction) + float64(b.Scale[i])*fraction)
	}
	var dot float64
	for i := range qa {
		dot += qa[i] * qb[i]
	}
	if dot < 0 {
		dot = -dot
		for i := range qb {
			qb[i] = -qb[i]
		}
	}
	left, right := 1-fraction, fraction
	if dot < .9995 {
		angle := math.Acos(min(1, dot))
		divisor := math.Sin(angle)
		left, right = math.Sin(left*angle)/divisor, math.Sin(right*angle)/divisor
	}
	var rotation [4]float64
	var length float64
	for i := range rotation {
		rotation[i] = left*qa[i] + right*qb[i]
		length += rotation[i] * rotation[i]
	}
	length = math.Sqrt(length)
	for i := range result.Rotation {
		result.Rotation[i] = float32(rotation[i] / length)
	}
	if err := ctx.Err(); err != nil {
		return U3DKeyFrame{}, err
	}
	return result, nil
}

// Matrix 将关键帧位移、旋转和缩放组合成相对父项的列式仿射矩阵，不使用Time
// 返回: [16]float64 先缩放、再旋转、后平移的变换, error 姿态数值错误
func (frame U3DKeyFrame) Matrix() ([16]float64, error) {
	q, err := frame.quaternion()
	if err != nil {
		return [16]float64{}, err
	}
	w, x, y, z := q[0], q[1], q[2], q[3]
	matrix := [16]float64{
		1 - 2*(y*y+z*z), 2 * (x*y + w*z), 2 * (x*z - w*y), 0,
		2 * (x*y - w*z), 1 - 2*(x*x+z*z), 2 * (y*z + w*x), 0,
		2 * (x*z + w*y), 2 * (y*z - w*x), 1 - 2*(x*x+y*y), 0,
		float64(frame.Displacement[0]), float64(frame.Displacement[1]), float64(frame.Displacement[2]), 1,
	}
	for axis, scale := range frame.Scale {
		for component := range 3 {
			matrix[axis*4+component] *= float64(scale)
		}
	}
	return matrix, nil
}

// quaternion 校验姿态分量并归一化W、X、Y、Z旋转四元数
// 返回: [4]float64 单位四元数, error 非有限或零旋转错误
func (frame U3DKeyFrame) quaternion() ([4]float64, error) {
	var rotation [4]float64
	for _, values := range [][3]float32{frame.Displacement, frame.Scale} {
		for _, value := range values {
			if !finite(float64(value)) {
				return rotation, fmt.Errorf("invalid U3D key frame transform")
			}
		}
	}
	var length float64
	for i, value := range frame.Rotation {
		rotation[i] = float64(value)
		length += rotation[i] * rotation[i]
	}
	if !finite(length) || length == 0 {
		return rotation, fmt.Errorf("invalid U3D key frame rotation")
	}
	length = math.Sqrt(length)
	for i := range rotation {
		rotation[i] /= length
	}
	return rotation, nil
}

// trackMatrix 按资源和轨道名称采样节点变换，不推测未命名轨道的绑定关系
// 入参: state 动作轨道状态
// 返回: [16]float64 节点变换, error 引用、数值或取消错误
func (r *Renderer) trackMatrix(state U3DTrackState) ([16]float64, error) {
	var track *U3DMotionTrack
	if err := r.ctx.Err(); err != nil {
		return [16]float64{}, err
	}
	if index, exists := r.motionIndex[state.Motion]; exists {
		motion := &r.model.Motions[index]
		for i := range motion.Tracks {
			if i%256 == 0 {
				if err := r.ctx.Err(); err != nil {
					return [16]float64{}, err
				}
			}
			if motion.Tracks[i].Name == state.Track {
				if track != nil {
					return [16]float64{}, fmt.Errorf("ambiguous U3D motion track %q", state.Track)
				}
				track = &motion.Tracks[i]
			}
		}
	}
	if track == nil {
		return [16]float64{}, fmt.Errorf("missing U3D motion track %q in %q", state.Track, state.Motion)
	}
	frame, err := track.Sample(r.ctx, state.Time, state.Loop)
	if err != nil {
		return [16]float64{}, err
	}
	return frame.Matrix()
}
