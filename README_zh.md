# lmgo

[English README](README.md)

lmgo 是一个用于运行本地大语言模型的工具套件，使用 llama.cpp 服务器和 **ROCm** GPU 加速。它包含：

- **lmgo**: Windows 系统托盘应用程序，提供模型管理
- **lmc**: 基于 BubbleTea 的终端控制界面

该软件专门针对配备 **AMD RYZEN AI MAX+ 395 / Radeon 8060S**的系统进行了优化。

## 系统要求

**此应用程序仅适用于：**

- **操作系统：** Windows 11
- **处理器：** AMD RYZEN AI MAX+ 395
- **显卡：** Radeon 8060S
- **架构：** x86_64

内置的 llama-server 专门为 ROCm GFX1151 架构编译，在其他硬件配置上无法工作。

## 功能特性

### lmgo (系统托盘)

- **系统托盘界面**：在 Windows 系统托盘中运行，便于访问
- **自动模型发现**：扫描目录中的 .gguf 模型文件
- **单模型支持**：一次只能加载和运行一个模型
- **Web 界面**：每个加载的模型都有内置的 Web 界面
- **开机自启**：可选择随 Windows 自动启动
- **通知功能**：Windows 通知显示模型状态
- **模型特定配置**：为不同模型提供自定义参数
- **自动浏览器启动**：模型加载时自动打开 Web 界面

### lmc (终端 UI)

- **终端界面**：基于 TUI 的模型管理，支持键盘快捷键
- **实时状态**：实时显示模型加载/卸载状态
- **API 集成**：与 lmgo 的 REST API 通信进行模型控制
- **键盘绑定**：直观的键盘控制（方向键、Enter、U、Q）

## 配置

应用程序创建 `lmgo.json` 配置文件，结构如下：

```json
{
  "modelDir": "./models",
  "autoOpenWebEnabled": true,
  "basePort": 8080,
  "llamaServerPort": 8081,
  "defaultArgs": [
    "--host", "0.0.0.0",
    "--prio-batch", "3",
    "--no-host",
    "--ctx-size", "131072",
    "--batch-size", "4096",
    "--ubatch-size", "4096",
    "--threads", "0",
    "--threads-batch", "0",
    "-ngl", "999",
    "--flash-attn", "on",
    "--cache-type-k", "f16",
    "--cache-type-v", "f16",
    "--kv-offload",
    "--no-mmap",
    "--no-repack",
    "--direct-io",
    "--mlock",
    "--split-mode", "layer",
    "--main-gpu", "0"
  ],
  "modelSpecificArgs": {}
}
```

### 配置选项

- **modelDir**：包含 .gguf 模型文件的目录
- **autoOpenWebEnabled**：模型加载时自动打开浏览器
- **basePort**：API 服务器端口（默认：8080）- 由 lmc 和 HTTP API 使用
- **llamaServerPort**：llama-server 端口（默认：8081）- 模型运行端口
- **defaultArgs**：传递给 llama-server 的默认参数
- **modelSpecificArgs**：特定模型的自定义参数

### API 端点

- `GET /api/models` - 列出所有可用模型
- `GET /api/status` - 获取当前模型状态
- `POST /api/load?index=N` - 加载索引为 N 的模型
- `POST /api/unload` - 卸载当前模型
- `GET /api/health` - 健康检查

## 从源代码构建 lmgo (系统托盘)

需要先下载最新的 [`llama-b*-windows-rocm-gfx1151-x64.zip`](https://github.com/zyoung11/lmgo/releases) 文件从 [releases](https://github.com/zyoung11/lmgo/releases) 然后

```bash
go mod tidy
go build -ldflags "-s -w -H windowsgui" -buildvcs=false .
```

## 从源代码构建 lmc (终端 UI)

```bash
cd lmc
go mod tidy
go build -buildvcs=false .
```
