package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/getlantern/systray"
	"github.com/go-toast/toast"
	"golang.org/x/sys/windows/registry"
)

//go:embed favicon.ico
var iconData []byte

//go:embed llama_cpp_rocm_gfx1151.tar.gz
var serverArchive []byte

//go:embed default_config.json
var defaultConfigData []byte

type Config struct {
	ModelDir          string              `json:"modelDir"`
	AutoOpenWeb       bool                `json:"autoOpenWebEnabled"`
	BasePort          int                 `json:"basePort"`
	DefaultArgs       []string            `json:"defaultArgs"`
	ModelSpecificArgs map[string][]string `json:"modelSpecificArgs"`
	Notifications     bool                `json:"notifications"`
	AutoLoadModels    []string            `json:"autoLoadModels"`
}

var config Config

var (
	runningModel    *modelInstance
	runningModelsMu sync.RWMutex

	currentModels []modelEntry

	tempLlamaServerPath string
	fixedTempDir        string
	iconTempPath        string

	menuItems struct {
		loadModel    *systray.MenuItem
		unloadModel  *systray.MenuItem
		webInterface *systray.MenuItem
		autostart    *systray.MenuItem
		quit         *systray.MenuItem
		models       []*systray.MenuItem
		modelIndexes []int
	}
)

type modelEntry struct {
	display   string
	firstPart string
	pattern   string
	baseName  string
}

type modelInstance struct {
	entry modelEntry
	cmd   *exec.Cmd
	port  int
}

var shardRe = regexp.MustCompile(`^(.+)-(\d{5})-of-(\d{5})\.gguf$`)

func main() {
	hideConsole()

	if err := loadConfig(); err != nil {
		sendErrorNotificationAndExit(fmt.Sprintf("Failed to load config: %v", err))
	}

	if err := initializeEmbeddedServer(); err != nil {
		sendErrorNotificationAndExit(fmt.Sprintf("Failed to initialize embedded server: %v", err))
	}
	defer cleanupEmbeddedServer()

	var err error
	currentModels, err = findGGUFFiles(config.ModelDir)
	if err != nil {
		sendErrorNotificationAndExit(fmt.Sprintf("Error scanning model files: %v", err))
	}
	if len(currentModels) == 0 {
		sendErrorNotificationAndExit(fmt.Sprintf("No .gguf files found in directory: %s", config.ModelDir))
	}

	systray.Run(onReady, onExit)
}

func sendErrorNotificationAndExit(message string) {
	log.Printf("Fatal error: %s", message)
	sendNotification("lmgo Server Error", message)
	time.Sleep(2 * time.Second)
	os.Exit(1)
}

func sendNotification(title, message string) {
	if !config.Notifications {
		return
	}

	if err := extractIconForNotification(); err != nil {
		handleWarning(err, "Failed to extract icon")
	}

	notification := toast.Notification{
		AppID:   "lmgo Server",
		Title:   title,
		Message: message,
		Icon:    iconTempPath,
	}

	if err := notification.Push(); err != nil {
		handleWarning(err, "Failed to send notification")
	}
}

func handleWarning(err error, context ...string) {
	if err == nil {
		return
	}

	message := err.Error()
	if len(context) > 0 {
		message = fmt.Sprintf("%s: %v", context[0], err)
	}

	log.Printf("Warning: %s", message)
}

func handleError(err error, context ...string) {
	if err == nil {
		return
	}

	message := err.Error()
	if len(context) > 0 {
		message = fmt.Sprintf("%s: %v", context[0], err)
	}

	log.Printf("Error: %s", message)
	sendNotification("lmgo Server Error", message)
}

func handleFatalError(err error, context ...string) {
	if err == nil {
		os.Exit(1)
	}

	message := err.Error()
	if len(context) > 0 {
		message = fmt.Sprintf("%s: %v", context[0], err)
	}

	log.Printf("Fatal error: %s", message)
	sendNotification("lmgo Server Error", message)
	time.Sleep(2 * time.Second)
	os.Exit(1)
}

func loadConfig() error {
	configFile := "lmgo.json"

	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		log.Printf("Config file %s does not exist, creating default config...", configFile)

		if err := json.Unmarshal(defaultConfigData, &config); err != nil {
			return fmt.Errorf("Failed to parse embedded default config: %v", err)
		}

		if config.ModelSpecificArgs == nil {
			config.ModelSpecificArgs = make(map[string][]string)
		}

		if err := saveConfig(); err != nil {
			return fmt.Errorf("Failed to save default config: %v", err)
		}

		log.Printf("Created default config file: %s", configFile)
		log.Printf("Default model directory: %s", config.ModelDir)
		log.Printf("Default port: %d", config.BasePort)

		return nil
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("Failed to read config file: %v", err)
	}

	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("Failed to parse config file: %v", err)
	}

	if config.ModelSpecificArgs == nil {
		config.ModelSpecificArgs = make(map[string][]string)
	}

	log.Printf("Config loaded successfully: modelDir=%s, basePort=%d, autoOpenWeb=%v",
		config.ModelDir, config.BasePort, config.AutoOpenWeb)
	log.Printf("Default arguments: %v", config.DefaultArgs)
	log.Printf("Model-specific config count: %d", len(config.ModelSpecificArgs))

	return nil
}

func saveConfig() error {
	configFile := "lmgo.json"
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("Failed to encode config: %v", err)
	}

	if err := os.WriteFile(configFile, data, 0644); err != nil {
		return fmt.Errorf("Failed to write config file: %v", err)
	}

	log.Printf("Config saved to: %s", configFile)
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

func extractBaseName(filePath string) string {
	fileName := filepath.Base(filePath)

	if m := shardRe.FindStringSubmatch(fileName); m != nil {
		return m[1]
	}

	return strings.TrimSuffix(fileName, ".gguf")
}

func initializeEmbeddedServer() error {
	tempBase := filepath.Join(os.TempDir(), "llama_server_fixed")

	if _, err := os.Stat(tempBase); err == nil {
		os.RemoveAll(tempBase)
	}

	if err := os.MkdirAll(tempBase, 0755); err != nil {
		return fmt.Errorf("Failed to create temp directory: %v", err)
	}

	if err := extractTarGz(serverArchive, tempBase); err != nil {
		os.RemoveAll(tempBase)
		return fmt.Errorf("Failed to extract server files: %v", err)
	}

	tempLlamaServerPath = filepath.Join(tempBase, "llama_cpp_rocm_gfx1151", "llama-server.exe")
	fixedTempDir = tempBase

	log.Printf("Server extracted to: %s", tempLlamaServerPath)
	return nil
}

func cleanupEmbeddedServer() {
	stopAllModels()
	time.Sleep(100 * time.Millisecond)

	if iconTempPath != "" {
		if err := os.Remove(iconTempPath); err != nil {
			handleWarning(err, "Failed to clean up temp icon file")
		} else {
			log.Printf("Cleaned up temp icon file: %s", iconTempPath)
		}
		iconTempPath = ""
	}

	if fixedTempDir == "" {
		return
	}

	if err := os.RemoveAll(fixedTempDir); err != nil {
		handleWarning(err, "Failed to clean up temp server directory")

		tempName := fixedTempDir + ".old." + strconv.FormatInt(time.Now().Unix(), 10)
		if renameErr := os.Rename(fixedTempDir, tempName); renameErr == nil {
			log.Printf("Renamed to: %s", tempName)

			go func(path string) {
				for attempts := 0; attempts < 5; attempts++ {
					time.Sleep(time.Duration(attempts+1) * 500 * time.Millisecond)
					if err := os.RemoveAll(path); err == nil {
						log.Printf("Delayed cleanup successful: %s", path)
						return
					}
				}
				log.Printf("Unable to clean up, please delete manually: %s", path)
			}(tempName)
		} else {
			log.Printf("Rename also failed: %v", renameErr)
			log.Printf("Please delete directory manually: %s", fixedTempDir)
		}
	} else {
		log.Printf("Cleaned up temp server directory: %s", fixedTempDir)
	}

	fixedTempDir = ""
}

func extractTarGz(data []byte, dest string) error {
	gzr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dest, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}

			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}

			f.Close()
		}
	}
	return nil
}

func openBrowser(url string) error {
	var cmd string
	var args []string

	cmd = "cmd"
	args = []string{"/c", "start"}

	args = append(args, url)

	return exec.Command(cmd, args...).Start()
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

	sendStartupNotification()
	go autoLoadModels()
}

func autoLoadModels() {
	if len(config.AutoLoadModels) == 0 {
		return
	}

	time.Sleep(1 * time.Second)

	for _, modelName := range config.AutoLoadModels {
		found := false
		for i, model := range currentModels {
			if strings.Contains(model.display, modelName) || model.display == modelName ||
				strings.Contains(filepath.Base(model.firstPart), modelName) {
				found = true

				alreadyLoaded := false
				runningModelsMu.RLock()
				if runningModel != nil && runningModel.entry.firstPart == model.firstPart {
					alreadyLoaded = true
				}
				runningModelsMu.RUnlock()

				if !alreadyLoaded {
					loadModel(i)
					log.Printf("Auto-loading model: %s", model.display)
				} else {
					log.Printf("Model already loaded, skipping: %s", model.display)
				}

				break
			}
		}

		if !found {
			if config.Notifications {
				if err := extractIconForNotification(); err != nil {
					handleWarning(err, "Failed to extract icon")
				}

				notification := toast.Notification{
					AppID:   "lmgo Server",
					Title:   "Auto-load model not found",
					Message: fmt.Sprintf("Model specified in config not found: %s\nThis model will not be loaded", modelName),
					Icon:    iconTempPath,
				}

				if notifyErr := notification.Push(); notifyErr != nil {
					handleWarning(notifyErr, "Failed to send model not found notification")
				}
			}

			log.Printf("Auto-load model not found: %s", modelName)
		}
	}
}

func extractIconForNotification() error {
	if iconTempPath != "" {
		return nil
	}

	iconTempPath = filepath.Join(os.TempDir(), "llama_server_icon.ico")
	if err := os.WriteFile(iconTempPath, iconData, 0644); err != nil {
		iconTempPath = ""
		return fmt.Errorf("Failed to extract icon: %v", err)
	}
	return nil
}

func sendStartupNotification() {
	if !config.Notifications {
		return
	}

	if err := extractIconForNotification(); err != nil {
		handleWarning(err, "Failed to extract icon")
	}

	notification := toast.Notification{
		AppID:   "lmgo Server",
		Title:   "lmgo Server Started",
		Message: fmt.Sprintf("Found %d models, click tray icon to load", len(currentModels)),
		Icon:    iconTempPath,
	}

	go func() {
		if err := notification.Push(); err != nil {
			handleWarning(err, "Failed to send notification")
		}
	}()
}

func buildMenuOnce() {
	menuItems.loadModel = systray.AddMenuItem("Load Model", "Select model to load")

	maxModels := 100
	for i := 0; i < maxModels; i++ {
		item := menuItems.loadModel.AddSubMenuItem("", "")
		item.Hide()
		menuItems.models = append(menuItems.models, item)

		go func(idx int, menuItem *systray.MenuItem) {
			for range menuItem.ClickedCh {
				loadModel(idx)
			}
		}(i, item)
	}

	menuItems.unloadModel = systray.AddMenuItem("Unload Current Model", "Unload the currently loaded model")
	menuItems.unloadModel.Disable()

	go func() {
		for range menuItems.unloadModel.ClickedCh {
			unloadModel()
		}
	}()

	menuItems.webInterface = systray.AddMenuItem("Web Interface", "Open web interface for current model")
	menuItems.webInterface.Disable()

	go func() {
		for range menuItems.webInterface.ClickedCh {
			openCurrentModelWebInterface()
		}
	}()

	systray.AddSeparator()

	checked := isAutostartEnabled()
	menuItems.autostart = systray.AddMenuItemCheckbox("Start on Boot", "Start with Windows", checked)

	if checked {
		menuItems.autostart.Check()
	} else {
		menuItems.autostart.Uncheck()
	}

	go func() {
		for range menuItems.autostart.ClickedCh {
			toggleAutostart()
		}
	}()

	systray.AddSeparator()

	menuItems.quit = systray.AddMenuItem("Exit", "Exit program")
	go func() {
		for range menuItems.quit.ClickedCh {
			unloadModel()
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
			title := m.display

			runningModelsMu.RLock()
			isCurrent := hasRunningModel && runningModel.entry.firstPart == m.firstPart
			runningModelsMu.RUnlock()

			if isCurrent {
				title = fmt.Sprintf("● %s", title)
			} else {
				title = fmt.Sprintf("○ %s", title)
			}

			item.SetTitle(title)
			item.SetTooltip(fmt.Sprintf("Load %s", m.display))
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
		handleError(err, "Failed to open browser")
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

	port := config.BasePort

	instance := &modelInstance{
		entry: entry,
		port:  port,
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
			handleWarning(err, fmt.Sprintf("Failed to kill llama-server process (port %d)", instance.port))
		} else {
			processState, waitErr := instance.cmd.Process.Wait()
			if waitErr != nil {
				handleWarning(waitErr, fmt.Sprintf("Error waiting for process to exit (port %d)", instance.port))
			} else {
				log.Printf("Stopped model %s (port %d), PID: %d, Exit Code: %v",
					instance.entry.display, instance.port, pid, processState.ExitCode())
			}
		}
		instance.cmd = nil
	}

	// Wait for server to completely shut down by polling the /models endpoint
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
		"-m", instance.entry.firstPart,
		"--port", strconv.Itoa(instance.port),
	}

	modelArgs := getModelArgs(instance.entry)
	args = append(args, modelArgs...)

	log.Printf("Starting model %s on port %d with args: %v",
		instance.entry.display, instance.port, args)

	if config.AutoOpenWeb {
		go func() {
			openBrowser(fmt.Sprintf("http://127.0.0.1:%d", instance.port))
		}()
	}

	cmd := exec.Command(tempLlamaServerPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
	}

	runningModelsMu.Lock()
	instance.cmd = cmd
	runningModelsMu.Unlock()

	if err := cmd.Start(); err != nil {
		handleWarning(err, fmt.Sprintf("Failed to start llama-server (port %d)", instance.port))

		if config.Notifications {
			if err := extractIconForNotification(); err != nil {
				handleWarning(err, "Failed to extract icon")
			}

			notification := toast.Notification{
				AppID:   "lmgo Server",
				Title:   "Model Load Failed",
				Message: fmt.Sprintf("Model '%s' failed to load\nPort: %d\nError: %v", instance.entry.display, instance.port, err),
				Icon:    iconTempPath,
			}

			if notifyErr := notification.Push(); notifyErr != nil {
				handleWarning(notifyErr, "Failed to send load failure notification")
			}
		}

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

	// Wait for model to finish loading by polling the /models endpoint
	waitForModelLoad(instance)

	if config.Notifications {
		if err := extractIconForNotification(); err != nil {
			handleWarning(err, "Failed to extract icon")
		}

		notification := toast.Notification{
			AppID:   "lmgo Server",
			Title:   "Model Loaded Successfully",
			Message: fmt.Sprintf("Model '%s' loaded successfully\nPort: %d", instance.entry.display, instance.port),
			Icon:    iconTempPath,
		}

		if notifyErr := notification.Push(); notifyErr != nil {
			handleWarning(notifyErr, "Failed to send load success notification")
		}
	}

	err := cmd.Wait()
	if err != nil {
		handleWarning(err, fmt.Sprintf("llama-server (port %d) exited abnormally", instance.port))

		if config.Notifications {
			if err := extractIconForNotification(); err != nil {
				handleWarning(err, "Failed to extract icon")
			}

			notification := toast.Notification{
				AppID:   "lmgo Server",
				Title:   "Model Stopped",
				Message: fmt.Sprintf("Model '%s' has stopped running\nPort: %d", instance.entry.display, instance.port),
				Icon:    iconTempPath,
			}

			if notifyErr := notification.Push(); notifyErr != nil {
				handleWarning(notifyErr, "Failed to send model stopped notification")
			}
		}

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

/* ---------- Auto-start on Boot ---------- */

func isAutostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	if err != nil {
		handleWarning(err, "Failed to open registry")
		return false
	}
	defer k.Close()

	val, _, err := k.GetStringValue("LLMServerTray")
	if err != nil {
		return false
	}

	exePath, _ := os.Executable()
	return val == fmt.Sprintf(`"%s"`, exePath) || val == exePath
}

func toggleAutostart() {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`,
		registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		handleWarning(err, "Failed to open registry")
		return
	}
	defer k.Close()

	exePath, err := os.Executable()
	if err != nil {
		handleWarning(err, "Failed to get executable path")
		return
	}

	if menuItems.autostart.Checked() {
		err = k.DeleteValue("LLMServerTray")
		if err != nil {
			handleWarning(err, "Failed to delete registry value")
		} else {
			menuItems.autostart.Uncheck()
			log.Println("Disabled auto-start on boot")
		}
	} else {
		err = k.SetStringValue("LLMServerTray", fmt.Sprintf(`"%s"`, exePath))
		if err != nil {
			handleWarning(err, "Failed to set registry value")
		} else {
			menuItems.autostart.Check()
			log.Println("Enabled auto-start on boot")
		}
	}
}

func onExit() {
	stopAllModels()
	cleanupEmbeddedServer()
}

func findGGUFFiles(dir string) ([]modelEntry, error) {
	var files []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".gguf") {
			if abs, e := filepath.Abs(path); e == nil {
				files = append(files, abs)
			}
		}
		return nil
	})

	group := make(map[string][]string)
	for _, f := range files {
		name := filepath.Base(f)
		if m := shardRe.FindStringSubmatch(name); m != nil {
			base := m[1]
			group[base] = append(group[base], f)
		} else {
			group[name] = append(group[name], f)
		}
	}

	var result []modelEntry
	for base, parts := range group {
		first := parts[0]
		for _, p := range parts {
			if p < first {
				first = p
			}
		}
		pattern := filepath.Join(filepath.Dir(first),
			strings.Replace(filepath.Base(first), "00001", "?????", 1))

		baseName := extractBaseName(first)

		entry := modelEntry{
			firstPart: first,
			pattern:   pattern,
			baseName:  baseName,
		}
		if len(parts) == 1 && !shardRe.MatchString(filepath.Base(first)) {
			entry.display = fmt.Sprintf("%s", filepath.Base(first))
		} else {
			entry.display = fmt.Sprintf("%s (%d shards)", base, len(parts))
		}
		result = append(result, entry)
	}

	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[i].display > result[j].display {
				result[i], result[j] = result[j], result[i]
			}
		}
	}

	for _, entry := range result {
		log.Printf("Found model: %s (baseName: %s)", entry.display, entry.baseName)
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

			log.Printf("Model %s finished loading on port %d", instance.entry.display, instance.port)
			return

		case <-timeout:
			log.Printf("Timeout waiting for model %s to load on port %d", instance.entry.display, instance.port)
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
				log.Printf("Model %s confirmed shutdown on port %d", instance.entry.display, instance.port)
				return
			}

		case <-timeout:
			log.Printf("Timeout waiting for model %s to shutdown on port %d", instance.entry.display, instance.port)
			return
		}
	}
}
