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
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ParsePageRange 解析一基页码及闭区间，返回升序去重的零基页面索引
// 入参: value 页码表达式，如1-6,7,8-10, count 文档总页数
// 返回: []int 页面索引, error 错误信息
func ParsePageRange(value string, count int) ([]int, error) {
	var intervals [][2]int
	for _, part := range strings.Split(strings.ReplaceAll(value, "，", ","), ",") {
		first, last, interval := strings.Cut(strings.TrimSpace(part), "-")
		start, err := strconv.Atoi(strings.TrimSpace(first))
		if err != nil {
			return nil, fmt.Errorf("invalid page range %q", part)
		}
		end := start
		if interval {
			end, err = strconv.Atoi(strings.TrimSpace(last))
			if err != nil {
				return nil, fmt.Errorf("invalid page range %q", part)
			}
		}
		if start < 1 || end < start || end > count {
			return nil, fmt.Errorf("page range %q outside 1-%d", part, count)
		}
		intervals = append(intervals, [2]int{start - 1, end})
	}
	slices.SortFunc(intervals, func(a, b [2]int) int { return cmp.Compare(a[0], b[0]) })
	merged := intervals[:0]
	for _, interval := range intervals {
		if len(merged) != 0 && interval[0] <= merged[len(merged)-1][1] {
			merged[len(merged)-1][1] = max(merged[len(merged)-1][1], interval[1])
		} else {
			merged = append(merged, interval)
		}
	}
	length := 0
	for _, interval := range merged {
		length += interval[1] - interval[0]
	}
	indices := make([]int, 0, length)
	for _, interval := range merged {
		for index := interval[0]; index < interval[1]; index++ {
			indices = append(indices, index)
		}
	}
	return indices, nil
}

// exportPageIndices 校验导出索引并按原页序去重，省略索引时选取全部页面
// 入参: count 文档总页数, indices 零基页面索引
// 返回: []int 页面索引, error 错误信息
func exportPageIndices(count int, indices []int) ([]int, error) {
	if count == 0 {
		return nil, fmt.Errorf("no pages found")
	}
	if len(indices) == 0 {
		indices = make([]int, count)
		for index := range indices {
			indices[index] = index
		}
		return indices, nil
	}
	for _, index := range indices {
		if index < 0 || index >= count {
			return nil, fmt.Errorf("page index %d out of range", index)
		}
	}
	indices = slices.Clone(indices)
	slices.Sort(indices)
	return slices.Compact(indices), nil
}
