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
	"reflect"
	"sync"
)

// editorClonePlans 仅共享类型的可变字段索引，不持有文档对象或资源
var editorClonePlans sync.Map

// cloneEditorData 深复制编辑快照，未导出的排版及原文来源保持不可变共享
// 入参: value 原值
// 返回: T 独立副本
func cloneEditorData[T any](value T) T {
	source := reflect.ValueOf(value)
	if !source.IsValid() {
		return value
	}
	return cloneEditorValue(source).Interface().(T)
}

// cloneEditorValue 复制公开指针、切片及数组，纯值数据不创建额外结构
// 入参: value 原值
// 返回: reflect.Value 独立副本
func cloneEditorValue(value reflect.Value) reflect.Value {
	if !value.IsValid() || !editorCloneMutable(value.Type()) {
		return value
	}
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return value
		}
		result := reflect.New(value.Type().Elem())
		cloneEditorInto(result.Elem(), value.Elem())
		return result
	case reflect.Slice:
		if value.IsNil() {
			return value
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		if !editorCloneMutable(value.Type().Elem()) {
			reflect.Copy(result, value)
		} else {
			for i := range value.Len() {
				cloneEditorInto(result.Index(i), value.Index(i))
			}
		}
		return result
	default:
		result := reflect.New(value.Type()).Elem()
		cloneEditorInto(result, value)
		return result
	}
}

// cloneEditorInto 直接填充目标结构，避免指针及切片成员的二次结构分配
// 入参: target 可写目标, source 原值
func cloneEditorInto(target, source reflect.Value) {
	switch source.Kind() {
	case reflect.Struct:
		target.Set(source)
		for _, i := range editorCloneFields(source.Type()) {
			cloneEditorInto(target.Field(i), source.Field(i))
		}
	case reflect.Array:
		target.Set(source)
		if editorCloneMutable(source.Type().Elem()) {
			for i := range source.Len() {
				cloneEditorInto(target.Index(i), source.Index(i))
			}
		}
	default:
		target.Set(cloneEditorValue(source))
	}
}

// editorCloneMutable 判断类型是否含有需要隔离的公开可变成员
// 入参: kind 数据类型
// 返回: bool 是否需要独立复制
func editorCloneMutable(kind reflect.Type) bool {
	switch kind.Kind() {
	case reflect.Pointer, reflect.Slice:
		return true
	case reflect.Array:
		return editorCloneMutable(kind.Elem())
	case reflect.Struct:
		return len(editorCloneFields(kind)) != 0
	}
	return false
}

// editorCloneFields 按类型复用公开可变字段，索引发布后保持只读
// 入参: kind 结构类型
// 返回: []int 可变字段索引
func editorCloneFields(kind reflect.Type) []int {
	if cached, ok := editorClonePlans.Load(kind); ok {
		return cached.([]int)
	}
	var fields []int
	for i := range kind.NumField() {
		field := kind.Field(i)
		if field.IsExported() && editorCloneMutable(field.Type) {
			fields = append(fields, i)
		}
	}
	actual, _ := editorClonePlans.LoadOrStore(kind, fields)
	return actual.([]int)
}
