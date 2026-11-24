# OFDGo [![PkgGoDev](https://pkg.go.dev/badge/github.com/xiaoqidun/ofdgo)](https://pkg.go.dev/github.com/xiaoqidun/ofdgo)
首个原生、全平台、纯 Go 语言高性能 OFD 引擎

# 主要能力
- 文档处理：创建、读写、合并、拆分、压缩
- 文档管理：新建、导入、切换、排序、删除
- 版本管理：创建、改名、切换、另存、删除
- 页面管理：添加、导入、复制、排序、删除
- 文档结构：图层、模板、目录、书签、标引
- 资源管理：导入、绑定、替换、提取、清理
- 文档安全：签名、签章、验签、加密、解密
- 电子发票：提取、创建、编辑、校验、同步
- 文档编辑：选区、重排、事务、撤销、重做
- 文字处理：插入、编辑、替换、排版、提取
- 文字样式：混排、粗体、斜体、阴影、底色
- 字体处理：匹配、内嵌、外置、子集、提取
- 图片处理：插入、替换、缩放、裁剪、提取
- 图形处理：绘制、变换、组合、对齐、分布
- 色彩管理：调色、渐变、图案、透明、转换
- 注解管理：批注、高亮、水印、印章、链接
- 交互动作：跳转、外链、附件、音频、视频
- 页面渲染：矢量、位图、裁剪、蒙版、合成
- 三维处理：解析、材质、纹理、动画、渲染
- 格式转换：互转、选页、并行、进度、打包
- 打印输出：预览、双面、合印、小册、海报
- 文档阅读：搜索、缩放、旋转、双页、连续
- 引擎架构：解耦、后端、并发、缓存、预算
- 应用交互：标签、链接、关联、拖放、复用

# 在线使用
[OFDGo WebUI](https://ofdgo.aite.me/)，将OFDGo编译为WASM提供服务

# 链接接入
```text
https://ofdgo.aite.me/#url=<URL编码后的文件地址>
```

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `url` | 是 | 文件地址，支持OFD、PDF |
| `name` | 否 | 文件名称，默认自动获取 |
| `page` | 否 | 物理页码，默认1，超过总页数取末页 |

参数用`&`连接，参数值需URL编码。跨域访问时，服务器头需包含：

```http
Access-Control-Allow-Origin: https://ofdgo.aite.me
```

公开资源可设为`*`；自建编辑器替换为对应源（协议、域名及端口）。

# 一键部署
```shell
docker run -d -p 80:80 ccr.ccs.tencentyun.com/xiaoqidun/ofdgo:latest
```

# 构建指南
```batch
:: 1. 编译OFDGo WASM
set GOOS=js
set GOARCH=wasm
go build -o assets/webui/wasm/ofdgo.wasm -trimpath -ldflags "-s -w -buildid=" ./cmd/webui/wasm.go
:: 2. 编译OFDGo WebUI
set GOOS=windows
set GOARCH=amd64
go build -o ofdgo_webui.exe -trimpath -ldflags "-s -w -buildid=" ./cmd/webui/webui.go
```

# 原生桌面
基于 [GUIGo](https://github.com/xiaoqidun/guigo) 实现，运行前需参照其说明安装系统依赖

```batch
set CGO_ENABLED=0

:: 构建Linux arm64（aarch64）
set GOOS=linux
set GOARCH=arm64
go build -o ofdgo_linux_arm64 -trimpath -ldflags "-s -w -buildid=" ./cmd/webview

:: 构建Linux amd64（x64/x86_64）
set GOOS=linux
set GOARCH=amd64
go build -o ofdgo_linux_amd64 -trimpath -ldflags "-s -w -buildid=" ./cmd/webview

:: 构建Windows amd64（x64/x86_64）
set GOOS=windows
set GOARCH=amd64
go build -o ofdgo_windows_amd64.exe -trimpath -ldflags "-H windowsgui -s -w -buildid=" ./cmd/webview
```

# 安装引擎
```shell
go get -u github.com/xiaoqidun/ofdgo
```

# 创建文档
```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/xiaoqidun/ofdgo"
)

func main() {
	// 1. 创建文档
	editor := ofdgo.NewEditor()
	defer editor.Close()
	editor.Info.Title = "示例文档"
	page, err := editor.AddPage(210, 297)
	if err != nil {
		log.Fatal(err)
	}
	// 2. 添加文字
	fontData, err := os.ReadFile("font.ttf")
	if err != nil {
		log.Fatal(err)
	}
	fontID, err := editor.AddFont(ofdgo.FontFile{Data: fontData}, 0)
	if err != nil {
		log.Fatal(err)
	}
	_, err = editor.AddText(page, ofdgo.Box{X: 20, Y: 20, W: 170, H: 15}, "你好，OFDGo！", fontID, 6)
	if err != nil {
		log.Fatal(err)
	}
	// 3. 保存文档
	output, err := ofdgo.NewFileOutput("test.ofd")
	if err != nil {
		log.Fatal(err)
	}
	if _, err := editor.SaveAs(context.Background(), output, ofdgo.WriteOptions{}); err != nil {
		log.Fatal(err)
	}
}
```

# 编辑文档
```go
package main

import (
	"context"
	"log"

	"github.com/xiaoqidun/ofdgo"
)

func main() {
	// 1. 打开文档
	input, err := ofdgo.OpenFile(context.Background(), "test.ofd", ofdgo.InputOptions{})
	if err != nil {
		log.Fatal(err)
	}
	defer input.Close()
	reader, err := ofdgo.NewReader(input)
	if err != nil {
		log.Fatal(err)
	}
	defer reader.Close()
	// 2. 编辑标题
	editor, err := reader.Editor()
	if err != nil {
		log.Fatal(err)
	}
	defer editor.Close()
	editor.Info.Title = "编辑后的"
	// 3. 另存文档
	output, err := ofdgo.NewFileOutput("edited.ofd")
	if err != nil {
		log.Fatal(err)
	}
	if _, err := editor.SaveAs(context.Background(), output, ofdgo.WriteOptions{}); err != nil {
		log.Fatal(err)
	}
}
```

# 转换文档
```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/xiaoqidun/ofdgo"
)

func main() {
	// 1. 打开文档
	input, err := ofdgo.OpenFile(context.Background(), "test.pdf", ofdgo.InputOptions{})
	if err != nil {
		log.Fatal(err)
	}
	defer input.Close()
	// 2. 创建文件
	ofdFile, err := os.Create("test.ofd")
	if err != nil {
		log.Fatal(err)
	}
	defer ofdFile.Close()
	// 3. 转换文档
	report, err := ofdgo.ConvertPDF(context.Background(), input, ofdFile, ofdgo.PDFImportOptions{})
	if err != nil {
		log.Fatal(err)
	}
	// 4. 查看警告
	for _, warning := range report.Warnings {
		log.Printf("第%d页: %s", warning.Page, warning.Message)
	}
}
```

# 渲染文档
```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/xiaoqidun/ofdgo"
)

func main() {
	// 1. 打开OFD文件
	input, err := ofdgo.OpenFile(context.Background(), "test.ofd", ofdgo.InputOptions{})
	if err != nil {
		log.Fatal(err)
	}
	defer input.Close()
	reader, err := ofdgo.NewReader(input)
	if err != nil {
		log.Fatal(err)
	}
	defer reader.Close()
	// 2. 创建PDF文件
	pdfFile, err := os.Create("test.pdf")
	if err != nil {
		log.Fatal(err)
	}
	defer pdfFile.Close()
	// 3. 渲染PDF文件
	renderer := ofdgo.NewRenderer(reader)
	if err := renderer.RenderToMultiPagePDF(pdfFile); err != nil {
		log.Fatal(err)
	}
}
```

# 验证签名
```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/xiaoqidun/ofdgo"
)

func main() {
	// 1. 打开OFD文件
	input, err := ofdgo.OpenFile(context.Background(), "test.ofd", ofdgo.InputOptions{})
	if err != nil {
		log.Fatal(err)
	}
	defer input.Close()
	// 2. 信任的根证书
	trustCert, err := os.ReadFile("ca.crt")
	if err != nil {
		log.Fatal(err)
	}
	// 3. 验证OFD签名
	reports, err := ofdgo.VerifySignatures(input, ofdgo.WithSignatureTrustCert(trustCert))
	if err != nil {
		log.Fatal(err)
	}
	// 4. 判断验证结果
	if len(reports) == 0 {
		log.Println("文件未发现签名")
		return
	}
	valid := true
	for _, report := range reports {
		if report.TrustedValid() {
			log.Printf("签名%s验证通过", report.ID)
			continue
		}
		valid = false
		if report.Error == "" {
			log.Printf("签名%s验证失败", report.ID)
		} else {
			log.Printf("签名%s验证失败: %s", report.ID, report.Error)
		}
	}
	if !valid {
		os.Exit(1)
	}
}
```

# 提取发票
```go
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/xiaoqidun/ofdgo"
)

func main() {
	// 1. 打开OFD文件
	input, err := ofdgo.OpenFile(context.Background(), "invoice.ofd", ofdgo.InputOptions{})
	if err != nil {
		log.Fatal(err)
	}
	defer input.Close()
	reader, err := ofdgo.NewReader(input)
	if err != nil {
		log.Fatal(err)
	}
	defer reader.Close()
	// 2. 提取发票信息
	report, err := reader.Invoices()
	if err != nil {
		log.Fatal(err)
	}
	// 3. 输出提取结果
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		log.Fatal(err)
	}
}
```

# 授权协议
本项目使用 [Apache License 2.0](https://github.com/xiaoqidun/ofdgo/blob/main/LICENSE) 授权协议
