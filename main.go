package main

import (
	"archive/zip"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/getlantern/systray"
	"golang.org/x/sys/windows/registry"
)

//go:embed favicon.ico
var iconData []byte

//go:embed *.zip
var serverArchives embed.FS

//go:embed default_config.json
var defaultConfigData []byte

type Config struct {
	ModelDir          string              `json:"modelDir"`
	AutoOpenWeb       bool                `json:"autoOpenWebEnabled"`
	AutoStartEnabled  bool                `json:"autoStartEnabled"`
	BasePort          int                 `json:"basePort"`
	LlamaServerPort   int                 `json:"llamaServerPort"`
	DefaultArgs       []string            `json:"defaultArgs"`
	ModelSpecificArgs map[string][]string `json:"modelSpecificArgs"`
}

var config Config

var (
	runningModel    *modelInstance
	runningModelsMu sync.RWMutex

	currentModels []modelEntry

	serverPath string
	apiServer  *http.Server

	menuItems struct {
		loadModel    *systray.MenuItem
		unloadModel  *systray.MenuItem
		webInterface *systray.MenuItem
		autoStart    *systray.MenuItem
		quit         *systray.MenuItem
		models       []*systray.MenuItem
	}
)

type modelEntry struct {
	Path     string `json:"path"`
	BaseName string `json:"baseName"`
}

type modelInstance struct {
	entry modelEntry
	cmd   *exec.Cmd
	port  int
}

type APIResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

type ModelStatus struct {
	Loaded     bool       `json:"loaded"`
	Model      modelEntry `json:"model,omitempty"`
	Port       int        `json:"port,omitempty"`
	ServerPort int        `json:"serverPort,omitempty"`
}

func main() {
	hideConsole()

	// 切换到可执行文件所在目录，确保能找到配置文件
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		if err := os.Chdir(exeDir); err != nil {
			log.Printf("Warning: Failed to change working directory to %s: %v", exeDir, err)
		} else {
			log.Printf("Working directory changed to: %s", exeDir)
		}
	} else {
		log.Printf("Warning: Failed to get executable path: %v", err)
	}

	if err := loadConfig(); err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 同步开机自启状态：以注册表状态为准
	if isAutoStartEnabled() != config.AutoStartEnabled {
		config.AutoStartEnabled = isAutoStartEnabled()
	}

	if err := extractServer(); err != nil {
		log.Fatalf("Failed to extract server: %v", err)
	}

	var err error
	currentModels, err = findGGUFFiles(config.ModelDir)
	if err != nil {
		log.Fatalf("Error scanning model files: %v", err)
	}
	if len(currentModels) == 0 {
		log.Fatalf("No .gguf files found in directory: %s", config.ModelDir)
	}

	startAPIServer()

	systray.Run(onReady, onExit)
}

func loadConfig() error {
	configFile := "lmgo.json"

	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		log.Printf("Config file %s does not exist, creating default config...", configFile)

		if err := json.Unmarshal(defaultConfigData, &config); err != nil {
			return fmt.Errorf("failed to parse embedded default config: %v", err)
		}

		if config.BasePort == 0 {
			config.BasePort = 8080
		}
		if config.LlamaServerPort == 0 {
			config.LlamaServerPort = 8081
		}

		if config.ModelSpecificArgs == nil {
			config.ModelSpecificArgs = make(map[string][]string)
		}

		if config.BasePort == config.LlamaServerPort {
			return fmt.Errorf("API port (%d) and llama-server port (%d) cannot be the same", config.BasePort, config.LlamaServerPort)
		}

		if err := saveConfig(); err != nil {
			return fmt.Errorf("failed to save default config: %v", err)
		}

		log.Printf("Created default config file: %s", configFile)
		return nil
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to read config file: %v", err)
	}

	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("failed to parse config file: %v", err)
	}

	if config.BasePort == 0 {
		config.BasePort = 8080
	}
	if config.LlamaServerPort == 0 {
		config.LlamaServerPort = 8081
	}

	if config.BasePort == config.LlamaServerPort {
		return fmt.Errorf("API port (%d) and llama-server port (%d) cannot be the same", config.BasePort, config.LlamaServerPort)
	}

	if config.ModelSpecificArgs == nil {
		config.ModelSpecificArgs = make(map[string][]string)
	}

	log.Printf("Config loaded: modelDir=%s, basePort=%d, llamaServerPort=%d", config.ModelDir, config.BasePort, config.LlamaServerPort)
	return nil
}

func saveConfig() error {
	configFile := "lmgo.json"
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode config: %v", err)
	}

	if err := os.WriteFile(configFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %v", err)
	}

	log.Printf("Config saved to: %s", configFile)
	return nil
}

func extractServer() error {
	serverDir := "server"
	serverPath = filepath.Join(serverDir, "llama-server.exe")

	if _, err := os.Stat(serverPath); err == nil {
		log.Printf("Server already exists at: %s", serverPath)
		return nil
	}

	if err := os.MkdirAll(serverDir, 0755); err != nil {
		return fmt.Errorf("failed to create server directory: %v", err)
	}

	entries, err := serverArchives.ReadDir(".")
	if err != nil {
		return fmt.Errorf("failed to read embedded archives: %v", err)
	}
	if len(entries) != 1 {
		return fmt.Errorf("expected exactly one embedded zip file, found %d", len(entries))
	}

	zipData, err := serverArchives.ReadFile(entries[0].Name())
	if err != nil {
		return fmt.Errorf("failed to read embedded zip: %v", err)
	}

	if err := extractZip(zipData, serverDir); err != nil {
		return fmt.Errorf("failed to extract server: %v", err)
	}

	log.Printf("Server extracted to: %s", serverPath)
	return nil
}

func extractZip(data []byte, dest string) error {
	zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}

	for _, file := range zipReader.File {
		target := filepath.Join(dest, file.Name)

		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}

		dstFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, file.Mode())
		if err != nil {
			return err
		}

		srcFile, err := file.Open()
		if err != nil {
			dstFile.Close()
			return err
		}

		if _, err := io.Copy(dstFile, srcFile); err != nil {
			srcFile.Close()
			dstFile.Close()
			return err
		}

		srcFile.Close()
		dstFile.Close()
	}

	return nil
}

func startAPIServer() {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/models", handleModels)
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/load", handleLoad)
	mux.HandleFunc("/api/unload", handleUnload)
	mux.HandleFunc("/api/health", handleHealth)

	apiServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", config.BasePort),
		Handler: corsMiddleware(mux),
	}

	go func() {
		log.Printf("API server starting on port %d", config.BasePort)
		if err := apiServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("API server error: %v", err)
		}
	}()
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, APIResponse{
			Success: false,
			Message: "Method not allowed",
		})
		return
	}

	models := make([]map[string]interface{}, len(currentModels))
	for i, m := range currentModels {
		models[i] = map[string]interface{}{
			"index":    i,
			"name":     m.BaseName,
			"path":     m.Path,
			"filename": filepath.Base(m.Path),
		}
	}

	writeJSON(w, http.StatusOK, APIResponse{
		Success: true,
		Data:    models,
	})
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, APIResponse{
			Success: false,
			Message: "Method not allowed",
		})
		return
	}

	runningModelsMu.RLock()
	defer runningModelsMu.RUnlock()

	status := ModelStatus{
		Loaded:     runningModel != nil,
		ServerPort: config.BasePort,
		Port:       0,
	}

	if runningModel != nil {
		status.Model = runningModel.entry
		status.Port = runningModel.port
	}

	writeJSON(w, http.StatusOK, APIResponse{
		Success: true,
		Data:    status,
	})
}

func handleLoad(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, APIResponse{
			Success: false,
			Message: "Method not allowed",
		})
		return
	}

	idxStr := r.URL.Query().Get("index")
	if idxStr == "" {
		writeJSON(w, http.StatusBadRequest, APIResponse{
			Success: false,
			Message: "Missing index parameter",
		})
		return
	}

	idx, err := strconv.Atoi(idxStr)
	if err != nil || idx < 0 || idx >= len(currentModels) {
		writeJSON(w, http.StatusBadRequest, APIResponse{
			Success: false,
			Message: "Invalid index",
		})
		return
	}

	runningModelsMu.RLock()
	alreadyLoaded := runningModel != nil && runningModel.entry.Path == currentModels[idx].Path
	runningModelsMu.RUnlock()

	if alreadyLoaded {
		writeJSON(w, http.StatusOK, APIResponse{
			Success: true,
			Message: "Model already loaded",
			Data:    currentModels[idx],
		})
		return
	}

	loadModel(idx)

	writeJSON(w, http.StatusOK, APIResponse{
		Success: true,
		Message: "Model loading started",
		Data:    currentModels[idx],
	})
}

func handleUnload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, APIResponse{
			Success: false,
			Message: "Method not allowed",
		})
		return
	}

	runningModelsMu.RLock()
	isLoaded := runningModel != nil
	runningModelsMu.RUnlock()

	if !isLoaded {
		writeJSON(w, http.StatusOK, APIResponse{
			Success: true,
			Message: "No model currently loaded",
		})
		return
	}

	unloadModel()

	writeJSON(w, http.StatusOK, APIResponse{
		Success: true,
		Message: "Model unloaded",
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
	})
}

func getModelArgs(entry modelEntry) []string {
	if args, exists := config.ModelSpecificArgs[entry.BaseName]; exists && len(args) > 0 {
		log.Printf("Using model-specific config for %s", entry.BaseName)
		return args
	}
	log.Printf("Using default config for %s", entry.BaseName)
	return config.DefaultArgs
}

func openBrowser(url string) error {
	return exec.Command("cmd", "/c", "start", url).Start()
}

func getConsoleWindow() syscall.Handle {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetConsoleWindow")
	ret, _, _ := proc.Call()
	return syscall.Handle(ret)
}

func hideConsole() {
	hwnd := getConsoleWindow()
	if hwnd == 0 {
		return
	}
	user32 := syscall.NewLazyDLL("user32.dll")
	showWindow := user32.NewProc("ShowWindow")
	showWindow.Call(uintptr(hwnd), uintptr(0))
}

func onReady() {
	systray.SetIcon(iconData)
	systray.SetTitle("lmgo Server")
	systray.SetTooltip("lmgo Model Server")

	buildMenuOnce()
	refreshMenuState()

	log.Printf("Started. Found %d models. API available at http://localhost:%d/api", len(currentModels), config.BasePort)
}

func buildMenuOnce() {
	menuItems.loadModel = systray.AddMenuItem("Load Model", "Select a model to load")

	for i := 0; i < len(currentModels); i++ {
		item := menuItems.loadModel.AddSubMenuItem("", "")
		menuItems.models = append(menuItems.models, item)

		go func(idx int, menuItem *systray.MenuItem) {
			for range menuItem.ClickedCh {
				loadModel(idx)
			}
		}(i, item)
	}

	menuItems.unloadModel = systray.AddMenuItem("Unload Model", "Unload the model")
	menuItems.unloadModel.Disable()
	go func() {
		for range menuItems.unloadModel.ClickedCh {
			unloadModel()
		}
	}()

	menuItems.webInterface = systray.AddMenuItem("Web Interface", "Open web interface")
	menuItems.webInterface.Disable()
	go func() {
		for range menuItems.webInterface.ClickedCh {
			openCurrentModelWebInterface()
		}
	}()

	menuItems.autoStart = systray.AddMenuItem("Auto Startup", "Toggle auto-start on boot")
	go func() {
		for range menuItems.autoStart.ClickedCh {
			config.AutoStartEnabled = !config.AutoStartEnabled

			if err := setAutoStart(config.AutoStartEnabled); err != nil {
				log.Printf("Failed to update auto-start: %v", err)
				config.AutoStartEnabled = !config.AutoStartEnabled
			} else {
				if err := saveConfig(); err != nil {
					log.Printf("Failed to save config: %v", err)
				}
				refreshMenuState()
			}
		}
	}()

	systray.AddSeparator()

	menuItems.quit = systray.AddMenuItem("Exit", "Exit program")
	go func() {
		for range menuItems.quit.ClickedCh {
			systray.Quit()
		}
	}()
}

func refreshMenuState() {
	runningModelsMu.RLock()
	hasRunningModel := runningModel != nil
	runningModelsMu.RUnlock()

	if hasRunningModel {
		menuItems.unloadModel.Enable()
		menuItems.webInterface.Enable()
	} else {
		menuItems.unloadModel.Disable()
		menuItems.webInterface.Disable()
	}

	for i, item := range menuItems.models {
		if i < len(currentModels) {
			m := currentModels[i]
			title := filepath.Base(m.Path)

			runningModelsMu.RLock()
			isCurrent := hasRunningModel && runningModel.entry.Path == m.Path
			runningModelsMu.RUnlock()

			if isCurrent {
				title = "● " + title
			} else {
				title = "○ " + title
			}

			item.SetTitle(title)
			item.SetTooltip(fmt.Sprintf("Load %s", title))
			item.Show()
		} else {
			item.Hide()
		}
	}

	if config.AutoStartEnabled {
		menuItems.autoStart.SetTitle("✓ Auto Startup")
	} else {
		menuItems.autoStart.SetTitle("Auto Startup")
	}
}

func openCurrentModelWebInterface() {
	runningModelsMu.RLock()
	defer runningModelsMu.RUnlock()

	if runningModel == nil {
		return
	}

	url := fmt.Sprintf("http://127.0.0.1:%d", runningModel.port)
	if err := openBrowser(url); err != nil {
		log.Printf("Failed to open browser: %v", err)
	}
}

func loadModel(idx int) {
	if idx < 0 || idx >= len(currentModels) {
		return
	}

	if err := loadConfig(); err != nil {
		log.Printf("Warning: Failed to reload config: %v", err)
	}

	entry := currentModels[idx]

	runningModelsMu.Lock()

	if runningModel != nil {
		stopModelInstance(runningModel)
		runningModelsMu.Unlock()
		time.Sleep(1 * time.Second)
		runningModelsMu.Lock()
	}

	instance := &modelInstance{
		entry: entry,
		port:  config.LlamaServerPort,
	}

	runningModel = instance
	runningModelsMu.Unlock()

	go runLlamaServer(instance)
	refreshMenuState()
}

func unloadModel() {
	if err := loadConfig(); err != nil {
		log.Printf("Warning: Failed to reload config: %v", err)
	}

	runningModelsMu.Lock()

	if runningModel != nil {
		stopModelInstance(runningModel)
		runningModel = nil
	}

	runningModelsMu.Unlock()
	refreshMenuState()
}

func stopModelInstance(instance *modelInstance) {
	if instance.cmd != nil && instance.cmd.Process != nil {
		pid := instance.cmd.Process.Pid

		if err := instance.cmd.Process.Kill(); err != nil {
			log.Printf("Failed to kill process (port %d): %v", instance.port, err)
		} else {
			processState, _ := instance.cmd.Process.Wait()
			log.Printf("Stopped model %s (port %d), PID: %d, Exit Code: %v",
				filepath.Base(instance.entry.Path), instance.port, pid, processState.ExitCode())
		}
		instance.cmd = nil
	}

	waitForModelShutdown(instance)
	time.Sleep(500 * time.Millisecond)
}

func stopAllModels() {
	runningModelsMu.Lock()
	if runningModel != nil {
		stopModelInstance(runningModel)
		runningModel = nil
	}
	runningModelsMu.Unlock()
}

func runLlamaServer(instance *modelInstance) {
	args := []string{
		"-m", instance.entry.Path,
		"--port", strconv.Itoa(instance.port),
	}

	modelArgs := getModelArgs(instance.entry)
	args = append(args, modelArgs...)

	log.Printf("Starting model %s on port %d", filepath.Base(instance.entry.Path), instance.port)

	if config.AutoOpenWeb {
		go func() {
			openBrowser(fmt.Sprintf("http://127.0.0.1:%d", instance.port))
		}()
	}

	cmd := exec.Command(serverPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	runningModelsMu.Lock()
	instance.cmd = cmd
	runningModelsMu.Unlock()

	if err := cmd.Start(); err != nil {
		log.Printf("Failed to start llama-server: %v", err)

		runningModelsMu.Lock()
		if runningModel == instance {
			runningModel = nil
		}
		runningModelsMu.Unlock()

		go func() {
			time.Sleep(100 * time.Millisecond)
			refreshMenuState()
		}()
		return
	}

	waitForModelLoad(instance)

	err := cmd.Wait()
	if err != nil {
		log.Printf("llama-server exited abnormally: %v", err)

		runningModelsMu.Lock()
		if runningModel == instance {
			runningModel = nil
		}
		runningModelsMu.Unlock()

		go func() {
			time.Sleep(100 * time.Millisecond)
			refreshMenuState()
		}()
	}
}

func onExit() {
	if apiServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		apiServer.Shutdown(ctx)
	}
	stopAllModels()
}

func findGGUFFiles(dir string) ([]modelEntry, error) {
	var result []modelEntry

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".gguf") {
			continue
		}

		path := filepath.Join(dir, name)
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}

		result = append(result, modelEntry{
			Path:     path,
			BaseName: strings.TrimSuffix(name, ".gguf"),
		})
	}

	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[i].BaseName > result[j].BaseName {
				result[i], result[j] = result[j], result[i]
			}
		}
	}

	for _, entry := range result {
		log.Printf("Found model: %s", entry.BaseName)
	}

	return result, nil
}

func waitForModelLoad(instance *modelInstance) {
	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/models", instance.port)

	timeout := time.After(5 * time.Minute)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			resp, err := client.Get(url)
			if err != nil {
				continue
			}
			defer resp.Body.Close()

			body, _ := io.ReadAll(resp.Body)

			var responseMap map[string]interface{}
			if err := json.Unmarshal(body, &responseMap); err == nil {
				if errorObj, ok := responseMap["error"].(map[string]interface{}); ok {
					if msg, msgOk := errorObj["message"].(string); msgOk && msg == "Loading model" {
						continue
					}
				}
			}

			log.Printf("Model %s finished loading on port %d", filepath.Base(instance.entry.Path), instance.port)
			return

		case <-timeout:
			log.Printf("Timeout waiting for model to load on port %d", instance.port)
			return
		}
	}
}

func setAutoStart(enabled bool) error {
	const regPath = "Software\\Microsoft\\Windows\\CurrentVersion\\Run"
	const regName = "lmgo"

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %v", err)
	}

	exeDir := filepath.Dir(exePath)
	// 构建命令：切换到程序目录并运行程序，确保能找到配置文件
	cmd := fmt.Sprintf("cd /d \"%s\" && \"%s\"", exeDir, exePath)

	key, err := registry.OpenKey(registry.CURRENT_USER, regPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("failed to open registry key: %v", err)
	}
	defer key.Close()

	if enabled {
		err = key.SetStringValue(regName, cmd)
		if err != nil {
			return fmt.Errorf("failed to set registry value: %v", err)
		}
	} else {
		err = key.DeleteValue(regName)
		if err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("failed to delete registry value: %v", err)
		}
	}
	return nil
}

func isAutoStartEnabled() bool {
	const regPath = "Software\\Microsoft\\Windows\\CurrentVersion\\Run"
	const regName = "lmgo"

	key, err := registry.OpenKey(registry.CURRENT_USER, regPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()

	_, _, err = key.GetStringValue(regName)
	return err == nil
}

func waitForModelShutdown(instance *modelInstance) {
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/models", instance.port)

	timeout := time.After(30 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			_, err := client.Get(url)
			if err != nil {
				log.Printf("Model confirmed shutdown on port %d", instance.port)
				return
			}

		case <-timeout:
			log.Printf("Timeout waiting for model shutdown on port %d", instance.port)
			return
		}
	}
}
