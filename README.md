# OFDGo [![PkgGoDev](https://pkg.go.dev/badge/github.com/xiaoqidun/ofdgo)](https://pkg.go.dev/github.com/xiaoqidun/ofdgo)
首个原生、全平台、纯 Go 语言高性能 OFD 引擎

# 主要能力
- 文档处理：创建、读写、拆分、合并、压缩
- 渲染转换：导入、导出、矢量、位图、批量
- 文档安全：加密、解密、签名、签章、验签
- 页面管理：添加、导入、复制、排序、删除
- 文字处理：插入、编辑、排版、搜索、提取
- 图片处理：插入、替换、缩放、裁剪、提取
- 图形处理：绘制、组合、变换、对齐、分布
- 注解管理：批注、高亮、链接、水印、印章
- 资源管理：字体、色彩、图片、媒体、附件

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
go build -o assets/webui/ofdgo.wasm -trimpath -ldflags "-s -w -buildid=" ./cmd/webui/wasm.go
:: 2. 编译OFDGo WebUI
set GOOS=windows
set GOARCH=amd64
go build -o ofdgo_webui.exe -trimpath -ldflags "-s -w -buildid=" ./cmd/webui/webui.go
```

# 安装引擎
```shell
go get -u github.com/xiaoqidun/ofdgo
```

# 创建文档
```go
package main

import (
	"log"
	"os"

	"github.com/xiaoqidun/ofdgo"
)

func main() {
	// 1. 创建文档
	editor := ofdgo.NewEditor()
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
	ofdFile, err := os.Create("test.ofd")
	if err != nil {
		log.Fatal(err)
	}
	defer ofdFile.Close()
	if _, err := editor.WriteTo(ofdFile); err != nil {
		log.Fatal(err)
	}
}
```

# 编辑文档
```go
package main

import (
	"log"
	"os"

	"github.com/xiaoqidun/ofdgo"
)

func main() {
	// 1. 打开文档
	reader, err := ofdgo.Open("test.ofd")
	if err != nil {
		log.Fatal(err)
	}
	defer reader.Close()
	// 2. 编辑标题
	editor, err := reader.Editor()
	if err != nil {
		log.Fatal(err)
	}
	editor.Info.Title = "编辑后的"
	// 3. 另存文档
	ofdFile, err := os.Create("edited.ofd")
	if err != nil {
		log.Fatal(err)
	}
	defer ofdFile.Close()
	if _, err := editor.WriteTo(ofdFile); err != nil {
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
	pdfFile, err := os.Open("test.pdf")
	if err != nil {
		log.Fatal(err)
	}
	defer pdfFile.Close()
	info, err := pdfFile.Stat()
	if err != nil {
		log.Fatal(err)
	}
	// 2. 创建文件
	ofdFile, err := os.Create("test.ofd")
	if err != nil {
		log.Fatal(err)
	}
	defer ofdFile.Close()
	// 3. 转换文档
	report, err := ofdgo.ConvertPDF(context.Background(), pdfFile, info.Size(), ofdFile, ofdgo.PDFImportOptions{})
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
	"log"
	"os"

	"github.com/xiaoqidun/ofdgo"
)

func main() {
	// 1. 打开OFD文件
	reader, err := ofdgo.Open("test.ofd")
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
	"log"
	"os"

	"github.com/xiaoqidun/ofdgo"
)

func main() {
	// 1. 打开OFD文件
	data, err := os.ReadFile("test.ofd")
	if err != nil {
		log.Fatal(err)
	}
	// 2. 验证OFD签名
	reports, err := ofdgo.VerifySignaturesBytes(data)
	if err != nil {
		log.Fatal(err)
	}
	// 3. 判断验证结果
	if len(reports) == 0 {
		log.Println("文件未发现签名")
		return
	}
	valid := true
	for _, report := range reports {
		if report.IntegrityValid() {
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

# 授权协议
本项目使用 [Apache License 2.0](https://github.com/xiaoqidun/ofdgo/blob/main/LICENSE) 授权协议
