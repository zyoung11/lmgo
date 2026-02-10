package main

import (
	"archive/zip"
	"bytes"
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
	BasePort          int                 `json:"basePort"`
	DefaultArgs       []string            `json:"defaultArgs"`
	ModelSpecificArgs map[string][]string `json:"modelSpecificArgs"`
}

var config Config

var (
	runningModel    *modelInstance
	runningModelsMu sync.RWMutex

	currentModels []modelEntry

	serverPath string

	menuItems struct {
		loadModel    *systray.MenuItem
		unloadModel  *systray.MenuItem
		webInterface *systray.MenuItem
		quit         *systray.MenuItem
		models       []*systray.MenuItem
	}
)

type modelEntry struct {
	path     string
	baseName string
}

type modelInstance struct {
	entry modelEntry
	cmd   *exec.Cmd
	port  int
}

func main() {
	hideConsole()

	if err := loadConfig(); err != nil {
		log.Fatalf("Failed to load config: %v", err)
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

	systray.Run(onReady, onExit)
}

func loadConfig() error {
	configFile := "lmgo.json"

	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		log.Printf("Config file %s does not exist, creating default config...", configFile)

		if err := json.Unmarshal(defaultConfigData, &config); err != nil {
			return fmt.Errorf("failed to parse embedded default config: %v", err)
		}

		if config.ModelSpecificArgs == nil {
			config.ModelSpecificArgs = make(map[string][]string)
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

	if config.ModelSpecificArgs == nil {
		config.ModelSpecificArgs = make(map[string][]string)
	}

	log.Printf("Config loaded: modelDir=%s, basePort=%d", config.ModelDir, config.BasePort)
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

func getModelArgs(entry modelEntry) []string {
	if args, exists := config.ModelSpecificArgs[entry.baseName]; exists && len(args) > 0 {
		log.Printf("Using model-specific config for %s", entry.baseName)
		return args
	}
	log.Printf("Using default config for %s", entry.baseName)
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

	log.Printf("Started. Found %d models.", len(currentModels))
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
			title := filepath.Base(m.path)

			runningModelsMu.RLock()
			isCurrent := hasRunningModel && runningModel.entry.path == m.path
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
		port:  config.BasePort,
	}

	runningModel = instance
	runningModelsMu.Unlock()

	go runLlamaServer(instance)
	refreshMenuState()
}

func unloadModel() {
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
				filepath.Base(instance.entry.path), instance.port, pid, processState.ExitCode())
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
		"-m", instance.entry.path,
		"--port", strconv.Itoa(instance.port),
	}

	modelArgs := getModelArgs(instance.entry)
	args = append(args, modelArgs...)

	log.Printf("Starting model %s on port %d", filepath.Base(instance.entry.path), instance.port)

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
			path:     path,
			baseName: strings.TrimSuffix(name, ".gguf"),
		})
	}

	// Sort by filename
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[i].baseName > result[j].baseName {
				result[i], result[j] = result[j], result[i]
			}
		}
	}

	for _, entry := range result {
		log.Printf("Found model: %s", entry.baseName)
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

			log.Printf("Model %s finished loading on port %d", filepath.Base(instance.entry.path), instance.port)
			return

		case <-timeout:
			log.Printf("Timeout waiting for model to load on port %d", instance.port)
			return
		}
	}
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
