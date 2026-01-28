# lmgo

[English README](README.md)

lmgo 是一个 Windows 系统托盘应用程序，提供易于使用的界面，用于通过 llama.cpp 服务器和 **ROCm** GPU 加速运行本地大语言模型。该软件专门针对配备 **AMD RYZEN AI MAX+ 395 / Radeon 8060S**的系统进行了优化。

## 系统要求

**此应用程序仅适用于：**

- **操作系统：** Windows 11
- **处理器：** AMD RYZEN AI MAX+ 395
- **显卡：** Radeon 8060S
- **架构：** x86_64

内置的 llama-server 专门为 ROCm GFX1151 架构编译，在其他硬件配置上无法工作。

## 功能特性

- **系统托盘界面**：在 Windows 系统托盘中运行，便于访问
- **自动模型发现**：扫描目录中的 .gguf 模型文件
- **多模型支持**：在不同端口上同时加载和运行多个模型
- **Web 界面**：每个加载的模型都有内置的 Web 界面
- **开机自启**：可选择随 Windows 自动启动
- **通知功能**：Windows 通知显示模型状态
- **模型特定配置**：为不同模型提供自定义参数
- **自动浏览器启动**：模型加载时自动打开 Web 界面

## 快速开始

### 安装

1. **下载可执行文件**：`lmgo.exe` 是独立可执行文件
2. **创建模型目录**：在 `lmgo.exe` 所在目录创建 `models` 文件夹
3. **放置模型文件**：将您的 .gguf 模型文件复制到 `models` 目录

### 首次运行

1. **运行 lmgo.exe**：双击可执行文件
2. **配置**：首次运行时将创建默认的 `lmgo.json` 配置文件
3. **系统托盘**：应用程序将出现在系统托盘（通知区域）

### 使用应用程序

1. **右键单击托盘图标** 访问菜单
2. **加载模型**：选择"Load Model" → 从列表中选择模型
3. **访问 Web 界面**：加载后，选择"Web Interface" → 选择模型
4. **卸载模型**：选择"Unload Model" → 选择要停止的模型

## 配置

应用程序创建 `lmgo.json` 配置文件，结构如下：

```json
{
  "modelDir": "./models",
  "autoOpenWebEnabled": true,
  "notifications": true,
  "basePort": 8080,
  "autoLoadModels": [],
  "defaultArgs": [
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
- **notifications**：启用 Windows 通知
- **basePort**：模型的起始端口号（8080、8081、8082 等）
- **autoLoadModels**：启动时自动加载的模型名称数组
- **defaultArgs**：传递给 llama-server 的默认参数
- **modelSpecificArgs**：特定模型的自定义参数

## 菜单选项

### 加载模型
- 列出模型目录中发现的所有 .gguf 文件
- 将分片模型显示为单个条目
- 用"[Loaded xN]"表示已加载的模型

### 卸载模型
- 列出所有当前运行的模型
- 显示每个实例的端口号
- 允许停止单个模型实例

### Web 界面
- 列出已加载模型的 Web 界面
- 打开浏览器访问模型的 Web UI
- 显示端口号以便导航

### 开机自启
- 切换 Windows 自动启动
- 添加/删除自动启动的注册表项

### 退出
- 停止所有运行中的模型
- 清理临时文件
- 退出应用程序

## 技术细节

### 内置组件
- **llama-server**：为 ROCm GFX1151 自定义编译的版本
- **图标**：嵌入的 favicon.ico 用于托盘和通知
- **配置**：针对 AMD 硬件优化的默认设置

### 模型处理
- 支持单文件和分片 (.gguf) 模型
- 从 basePort 开始的自动端口分配
- 同一模型多次加载的实例计数
- 退出时的优雅清理

### 系统集成
- Windows 注册表用于自动启动配置
- 通过 systray 库实现系统托盘集成
- Windows 通知
- 默认隐藏控制台窗口

## 故障排除

### 常见问题

1. **"未找到 .gguf 文件"**
   - 确保模型在正确的目录中（默认：`./models`）
   - 检查文件扩展名是否为 `.gguf`
2. **模型加载失败**
   - 验证模型与 llama.cpp 的兼容性
   - 检查可用磁盘空间和内存
3. **Web 界面无法访问**
   - 检查防火墙设置
   - 验证端口是否被阻止
4. **应用程序无法启动**
   - 验证系统要求（Windows 11、AMD RYZEN AI MAX+ 395 / Radeon 8060S）

## 从源代码构建

```bash
go mod tidy
go build -ldflags "-s -w -H windowsgui" -buildvcs=false .
```

### 嵌入式资源
- `favicon.ico`：使用 `//go:embed` 嵌入
- `default_config.json`：嵌入式默认配置
- `llama_cpp_rocm_gfx1151.tar.gz`：嵌入式 llama-server 二进制文件
