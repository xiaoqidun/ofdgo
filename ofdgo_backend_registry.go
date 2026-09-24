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
	"sync"
)

// backendRegistry 保存后端工厂，不共享有状态的后端实例
var backendRegistry = struct {
	sync.RWMutex
	factories map[string]func() RenderBackends
}{factories: make(map[string]func() RenderBackends)}

// RegisterRenderBackend 注册后端组合，工厂应返回独立实例，重复标识不会覆盖已有实现
// 入参: name 后端标识, factory 后端工厂
// 返回: error 无效或重复注册错误
func RegisterRenderBackend(name string, factory func() RenderBackends) error {
	if name == "" || factory == nil {
		return fmt.Errorf("backend name and factory are required")
	}
	backendRegistry.Lock()
	defer backendRegistry.Unlock()
	if _, exists := backendRegistry.factories[name]; exists {
		return fmt.Errorf("render backend %q already registered", name)
	}
	backendRegistry.factories[name] = factory
	return nil
}

// NewRenderBackends 创建已注册后端组合，各项能力通过Info报告实际提供者
// 入参: name 已注册后端标识
// 返回: RenderBackends 后端组合, error 未知后端错误
func NewRenderBackends(name string) (RenderBackends, error) {
	backendRegistry.RLock()
	factory := backendRegistry.factories[name]
	backendRegistry.RUnlock()
	if factory == nil {
		return RenderBackends{}, fmt.Errorf("unknown render backend %q", name)
	}
	return factory(), nil
}

// RenderBackendInfos 列出已注册组合及其实际能力，供原生程序和WASM共用
// 返回: map[string]BackendInfo 后端能力
func RenderBackendInfos() map[string]BackendInfo {
	backendRegistry.RLock()
	factories := make(map[string]func() RenderBackends, len(backendRegistry.factories))
	for name, factory := range backendRegistry.factories {
		factories[name] = factory
	}
	backendRegistry.RUnlock()
	result := make(map[string]BackendInfo, len(factories))
	for name, factory := range factories {
		result[name] = factory().Info()
	}
	return result
}
