# lmgo

[中文版 README](README_zh.md)

lmgo is a Windows system tray application that provides an easy-to-use interface for running local LLM models using llama.cpp server with **ROCm** GPU acceleration. It's specifically optimized for systems with **AMD RYZEN AI MAX+ 395 / 8060S graphics**.

## System Requirements

**This application only works on:**
- **Operating System:** Windows 11
- **Processor:** AMD RYZEN AI MAX+ 395
- **Graphics:** Radeon 8060S
- **Architecture:** x86_64

The embedded llama-server is compiled specifically for ROCm GFX1151 architecture and will not work on other hardware configurations.

## Features

- **System Tray Interface**: Runs in the Windows system tray for easy access
- **Automatic Model Discovery**: Scans directories for .gguf model files
- **Single Model Support**: Load and run one model at a time
- **Web Interface**: Built-in web interface for each loaded model
- **Auto-start on Boot**: Option to start automatically with Windows
- **Notifications**: Windows toast notifications for model status
- **Model-specific Configuration**: Custom arguments for different models
- **Automatic Web Browser Launch**: Option to automatically open web interface when models load

## Quick Start

### Installation

1. **Download the executable**: [`lmgo.exe`](https://github.com/zyoung11/lmgo/releases) is a standalone executable
2. **Create a models directory**: Create a `models` folder in the same directory as `lmgo.exe`
3. **Place your models**: Copy your .gguf model files to the `models` directory

### First Run

1. **Run lmgo.exe**: Double-click the executable
2. **Configuration**: On first run, a default `lmgo.json` configuration file will be created
3. **System Tray**: The application will appear in your system tray (notification area)

### Using the Application

1. **Right-click the tray icon** to access the menu
2. **Load Model**: Select "Load Model" → choose a model from the list
3. **Access Web Interface**: Once loaded, select "Web Interface" to open the model's web UI
4. **Unload Model**: Select "Unload Current Model" to stop the currently loaded model

## Configuration

The application creates a `lmgo.json` configuration file with the following structure:

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

### Configuration Options

- **modelDir**: Directory containing .gguf model files
- **autoOpenWebEnabled**: Automatically open browser when model loads
- **notifications**: Enable Windows toast notifications
- **basePort**: Port number for the model (default: 8080)
- **autoLoadModels**: Model name to load automatically on startup (only one model supported)
- **defaultArgs**: Default arguments passed to llama-server
- **modelSpecificArgs**: Custom arguments for specific models

## Menu Options

### Load Model
- Lists all discovered .gguf files in the models directory
- Shows sharded models as single entries
- If a model is already loaded, it will be unloaded first

### Unload Current Model
- Stops the currently loaded model
- Menu item is enabled only when a model is running

### Web Interface
- Opens browser to the loaded model's web UI
- Menu item is enabled only when a model is running

### Start on Boot
- Toggle for automatic startup with Windows
- Adds/removes registry entry for auto-start

### Exit
- Stops all running models
- Cleans up temporary files
- Exits the application

## Technical Details

### Embedded Components
- **llama-server**: Custom compiled version for ROCm GFX1151
- **Icon**: Embedded favicon.ico for tray and notifications
- **Configuration**: Default settings optimized for AMD hardware

### Model Handling
- Supports both single-file and sharded (.gguf) models
- Single model at a time (loading new model unloads current one)
- Graceful cleanup on exit

### System Integration
- Windows registry for auto-start configuration
- System tray integration via systray library
- Windows toast notifications
- Console window hidden by default

## Troubleshooting

### Common Issues

1. **"No .gguf files found"**
   - Ensure models are in the correct directory (default: `./models`)
   - Check file extensions are `.gguf`
2. **Model fails to load**
   - Verify model compatibility with llama.cpp
   - Check available disk space and memory
3. **Web interface not accessible**
   - Check firewall settings
   - Verify port is not blocked
4. **Application doesn't start**
   - Verify system requirements (Windows 11, AMD RYZEN AI MAX+ 395 / Radeon 8060S)

## Building from Source

Download the latest [`llama-b*-windows-rocm-gfx1151-x64.zip`](https://github.com/zyoung11/lmgo/releases) file from [releases](https://github.com/zyoung11/lmgo/releases) first and then

```bash
go mod tidy
go build -ldflags "-s -w -H windowsgui" -buildvcs=false .
```

### Embedded Resources
- `favicon.ico`: Embedded using `//go:embed`
- `default_config.json`: Embedded default configuration
- `llama-b*-windows-rocm-gfx1151-x64.zip`: Embedded llama-server binary and dependencies
