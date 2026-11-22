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

//go:build !js || !wasm

package webview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xiaoqidun/guigo"
	webuiassets "github.com/xiaoqidun/ofdgo/assets/webui"
)

// applicationTheme 保存主题、窗口材质及最后一次解析的系统外观
type applicationTheme struct {
	Mode     string `json:"mode"`
	Dark     bool   `json:"dark"`
	Material string `json:"material"`
}

// themeColors 返回应用对应外观的原生标题栏配色
// 入参: dark 是否使用深色
// 返回: guigo.TitleBarColors 标题栏配色
func themeColors(dark bool) guigo.TitleBarColors {
	if dark {
		return guigo.TitleBarColors{Background: "#20272b", Foreground: "#e0e7eb", InactiveBackground: "#242b30", InactiveForeground: "#9aa8b1"}
	}
	return guigo.TitleBarColors{Background: "#e8edee", Foreground: "#263238", InactiveBackground: "#eef1f3", InactiveForeground: "#728087"}
}

// windowMaterial 将应用偏好转换为当前平台支持的原生材质
// 入参: value 材质偏好
// 返回: guigo.WindowMaterial 可用的窗口材质
func windowMaterial(value string) guigo.WindowMaterial {
	material := guigo.WindowSolid
	switch value {
	case "mica-alt":
		material = guigo.WindowMicaAlt
	case "acrylic":
		material = guigo.WindowAcrylic
	}
	if !guigo.SupportsWindowMaterial(material) {
		return guigo.WindowSolid
	}
	return material
}

// validate 检查应用主题及解析后的配色是否一致
// 返回: error 偏好错误
func (a applicationTheme) validate() error {
	if a.Mode != "system" && a.Mode != "light" && a.Mode != "dark" {
		return fmt.Errorf("主题无效")
	}
	if a.Mode == "light" && a.Dark || a.Mode == "dark" && !a.Dark {
		return fmt.Errorf("主题配色无效")
	}
	if a.Material != "solid" && a.Material != "mica-alt" && a.Material != "acrylic" {
		return fmt.Errorf("窗口材质无效")
	}
	return nil
}

// loadTheme 在创建窗口前读取用户目录中的主题偏好
// 返回: error 读取错误
func (h *host) loadTheme() error {
	h.theme = applicationTheme{Mode: "system", Dark: guigo.SystemDark(), Material: "solid"}
	data, err := os.ReadFile(h.themePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var theme applicationTheme
	if json.Unmarshal(data, &theme) == nil && theme.validate() == nil {
		if theme.Mode == "system" {
			theme.Dark = guigo.SystemDark()
		}
		h.theme = theme
	}
	return nil
}

// themeIndex 在页面首次绘制前提供当前主题偏好
// 返回: []byte 页面内容, error 资源读取错误
func (h *host) themeIndex() ([]byte, error) {
	data, err := webuiassets.NativeFS.ReadFile("index.html")
	if err != nil {
		return nil, err
	}
	h.themeMu.Lock()
	mode, material := h.theme.Mode, h.theme.Material
	h.themeMu.Unlock()
	if mode == "" {
		mode = "system"
	}
	materials := []string{"solid"}
	for _, choice := range []string{"acrylic", "mica-alt"} {
		if windowMaterial(choice) != guigo.WindowSolid {
			materials = append(materials, choice)
		}
	}
	background := ""
	if windowMaterial(material) != guigo.WindowSolid {
		background = " data-window-material"
	}
	return bytes.Replace(data, []byte(`<html lang="zh-CN">`), []byte(`<html lang="zh-CN" data-theme-mode="`+mode+`" data-window-choice="`+material+`" data-window-materials="`+strings.Join(materials, ",")+`"`+background+`>`), 1), nil
}

// setTheme 串行更新窗口配色及材质，成功保存后确认应用偏好
// 入参: value 已解析的主题偏好
// 返回: error 校验、持久化或窗口更新错误
func (h *host) setTheme(value applicationTheme) (err error) {
	if err := value.validate(); err != nil {
		return err
	}
	h.themeMu.Lock()
	defer h.themeMu.Unlock()
	h.mu.Lock()
	window := h.window
	h.mu.Unlock()
	if window == nil {
		return fmt.Errorf("窗口尚未就绪")
	}
	previous := h.theme
	defer func() {
		if err != nil {
			err = errors.Join(err, window.SetWindowMaterial(windowMaterial(previous.Material)), window.SetTitleBarColors(themeColors(previous.Dark)))
		}
	}()
	if err = window.SetWindowMaterial(windowMaterial(value.Material)); err != nil {
		return err
	}
	if err = window.SetTitleBarColors(themeColors(value.Dark)); err != nil {
		return err
	}
	if h.theme != value {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(h.themePath), "appearance-*")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Rename(file.Name(), h.themePath); err != nil {
			return err
		}
		h.theme = value
	}
	return nil
}
