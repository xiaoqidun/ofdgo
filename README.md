# OFDGo [![PkgGoDev](https://pkg.go.dev/badge/github.com/xiaoqidun/ofdgo)](https://pkg.go.dev/github.com/xiaoqidun/ofdgo)
首个原生、全平台兼容的纯 Go 语言 OFD 渲染库


# 在线体验
[OFDGo WebUI](https://ofdgo.aite.me/)，将OFDGo编译为WASM提供服务

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

# 安装为库
```shell
go get -u github.com/xiaoqidun/ofdgo
```

# 渲染输出
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

# 签名验证
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
		if report.Valid {
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
