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

package font

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// SystemFiles 获取当前系统可访问的字体文件，不读取字体内容
// 返回: []string 去重后的字体路径，不支持系统字体的环境返回空列表
func SystemFiles() []string {
	if !CanLoadSystemFonts() {
		return nil
	}
	var files []string
	seen := make(map[string]bool)
	for _, dir := range SystemDirs() {
		_ = filepath.WalkDir(dir, func(name string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() && IsFontFileName(name) && !seen[name] {
				seen[name] = true
				files = append(files, name)
			}
			return nil
		})
	}
	return files
}

// SystemDirs 获取当前操作系统的系统字体目录列表
// 返回: []string 字体目录列表
func SystemDirs() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "android":
		return []string{"/product/fonts", "/system/fonts"}
	case "darwin":
		return []string{"/System/Library/Fonts", "/Library/Fonts", filepath.Join(home, "Library", "Fonts")}
	case "linux":
		dirs := []string{"/usr/share/fonts", "/usr/local/share/fonts", filepath.Join(home, ".fonts")}
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		dirs = append(dirs, filepath.Join(data, "fonts"))
		for _, dir := range filepath.SplitList(os.Getenv("XDG_DATA_DIRS")) {
			dirs = append(dirs, filepath.Join(dir, "fonts"))
		}
		return dirs
	case "windows":
		windows := os.Getenv("WINDIR")
		if windows == "" {
			windows = `C:\Windows`
		}
		dirs := []string{filepath.Join(windows, "Fonts")}
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			dirs = append(dirs, filepath.Join(local, "Microsoft", "Windows", "Fonts"))
		}
		return dirs
	}
	return nil
}
