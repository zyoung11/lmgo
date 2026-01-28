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
	runningModels   = make(map[string]*modelInstance)
	runningModelsMu sync.RWMutex
	modelCounter    int

	currentModels []modelEntry

	tempLlamaServerPath string
	fixedTempDir        string
	iconTempPath        string

	menuItems struct {
		selectModel  *systray.MenuItem
		unloadModel  *systray.MenuItem
		unloadItems  []*systray.MenuItem
		webInterface *systray.MenuItem
		webItems     []*systray.MenuItem
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
	entry       modelEntry
	cmd         *exec.Cmd
	port        int
	instanceNum int
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
	if !config.Notifications {
		log.Printf("Error: %s", message)
		os.Exit(1)
	}

	if err := extractIconForNotification(); err != nil {
		log.Printf("Warning: Failed to extract icon: %v", err)
	}

	notification := toast.Notification{
		AppID:   "lmgo Server",
		Title:   "lmgo Server Error",
		Message: message,
		Icon:    iconTempPath,
	}

	if err := notification.Push(); err != nil {
		log.Printf("Failed to send error notification: %v", err)
	}

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
			log.Printf("Failed to clean up temp icon file: %v", err)
		} else {
			log.Printf("Cleaned up temp icon file: %s", iconTempPath)
		}
		iconTempPath = ""
	}

	if fixedTempDir == "" {
		return
	}

	if err := os.RemoveAll(fixedTempDir); err != nil {
		log.Printf("Failed to clean up temp server directory: %v", err)

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
				for _, instance := range runningModels {
					if instance.entry.firstPart == model.firstPart {
						alreadyLoaded = true
						break
					}
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
					log.Printf("Warning: Failed to extract icon: %v", err)
				}

				notification := toast.Notification{
					AppID:   "lmgo Server",
					Title:   "Auto-load model not found",
					Message: fmt.Sprintf("Model specified in config not found: %s\nThis model will not be loaded", modelName),
					Icon:    iconTempPath,
				}

				if notifyErr := notification.Push(); notifyErr != nil {
					log.Printf("Failed to send model not found notification: %v", notifyErr)
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
		log.Printf("Warning: %v", err)
	}

	notification := toast.Notification{
		AppID:   "lmgo Server",
		Title:   "lmgo Server Started",
		Message: fmt.Sprintf("Found %d models, click tray icon to load", len(currentModels)),
		Icon:    iconTempPath,
	}

	go func() {
		if err := notification.Push(); err != nil {
			log.Printf("Failed to send notification: %v", err)
		}
	}()
}

func buildMenuOnce() {
	menuItems.selectModel = systray.AddMenuItem("Load Model", "Select model to load")

	maxModels := 100
	for i := 0; i < maxModels; i++ {
		item := menuItems.selectModel.AddSubMenuItem("", "")
		item.Hide()
		menuItems.models = append(menuItems.models, item)

		go func(idx int, menuItem *systray.MenuItem) {
			for range menuItem.ClickedCh {
				loadModel(idx)
			}
		}(i, item)
	}

	menuItems.unloadModel = systray.AddMenuItem("Unload Model", "Select model to unload")
	menuItems.unloadModel.Disable()

	maxRunning := 20
	for i := 0; i < maxRunning; i++ {
		item := menuItems.unloadModel.AddSubMenuItem("", "")
		item.Hide()
		menuItems.unloadItems = append(menuItems.unloadItems, item)

		go func(idx int, menuItem *systray.MenuItem) {
			for range menuItem.ClickedCh {
				unloadModelByMenuIndex(idx)
			}
		}(i, item)
	}
	menuItems.webInterface = systray.AddMenuItem("Web Interface", "Open web interface for loaded models")
	menuItems.webInterface.Disable()

	for i := 0; i < maxRunning; i++ {
		item := menuItems.webInterface.AddSubMenuItem("", "")
		item.Hide()
		menuItems.webItems = append(menuItems.webItems, item)

		go func(idx int, menuItem *systray.MenuItem) {
			for range menuItem.ClickedCh {
				openModelWebInterfaceByMenuIndex(idx)
			}
		}(i, item)
	}

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
			stopAllModels()
			systray.Quit()
		}
	}()
}

func refreshMenuState() {
	runningModelsMu.RLock()
	count := len(runningModels)
	runningModelsMu.RUnlock()

	if count > 0 {
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
			instanceCount := 0
			for _, instance := range runningModels {
				if instance.entry.firstPart == m.firstPart {
					instanceCount++
				}
			}
			runningModelsMu.RUnlock()

			if instanceCount > 0 {
				title = fmt.Sprintf("● [Loaded x%d] %s", instanceCount, title)
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

	refreshWebInterfaceMenu()

	refreshUnloadMenu()
}

func refreshWebInterfaceMenu() {
	runningModelsMu.RLock()
	defer runningModelsMu.RUnlock()

	type kv struct {
		key string
		val *modelInstance
	}
	var instances []kv
	for k, v := range runningModels {
		instances = append(instances, kv{k, v})
	}

	for i := 0; i < len(instances); i++ {
		for j := i + 1; j < len(instances); j++ {
			if instances[i].val.port > instances[j].val.port {
				instances[i], instances[j] = instances[j], instances[i]
			}
		}
	}

	for i, item := range menuItems.webItems {
		if i < len(instances) {
			inst := instances[i].val
			displayName := inst.entry.display
			if inst.instanceNum > 1 {
				displayName = fmt.Sprintf("%s #%d", displayName, inst.instanceNum)
			}
			title := fmt.Sprintf("%s (Port:%d)", displayName, inst.port)

			item.SetTitle(title)
			item.SetTooltip(fmt.Sprintf("Open web interface for %s", displayName))
			item.Show()
		} else {
			item.Hide()
		}
	}
}

func openModelWebInterfaceByMenuIndex(menuIdx int) {
	runningModelsMu.RLock()

	type kv struct {
		key string
		val *modelInstance
	}
	var instances []kv
	for k, v := range runningModels {
		instances = append(instances, kv{k, v})
	}

	for i := 0; i < len(instances); i++ {
		for j := i + 1; j < len(instances); j++ {
			if instances[i].val.port > instances[j].val.port {
				instances[i], instances[j] = instances[j], instances[i]
			}
		}
	}

	if menuIdx >= len(instances) {
		runningModelsMu.RUnlock()
		return
	}

	instance := instances[menuIdx].val
	runningModelsMu.RUnlock()

	url := fmt.Sprintf("http://127.0.0.1:%d", instance.port)
	if err := openBrowser(url); err != nil {
		log.Printf("Failed to open browser: %v", err)
	}
}

func refreshUnloadMenu() {
	runningModelsMu.RLock()
	defer runningModelsMu.RUnlock()

	type kv struct {
		key string
		val *modelInstance
	}
	var instances []kv
	for k, v := range runningModels {
		instances = append(instances, kv{k, v})
	}

	for i := 0; i < len(instances); i++ {
		for j := i + 1; j < len(instances); j++ {
			if instances[i].val.port > instances[j].val.port {
				instances[i], instances[j] = instances[j], instances[i]
			}
		}
	}

	for i, item := range menuItems.unloadItems {
		if i < len(instances) {
			inst := instances[i].val
			displayName := inst.entry.display
			if inst.instanceNum > 1 {
				displayName = fmt.Sprintf("%s #%d", displayName, inst.instanceNum)
			}
			title := fmt.Sprintf("%s (Port:%d)", displayName, inst.port)

			item.SetTitle(title)
			item.SetTooltip(fmt.Sprintf("Unload %s", displayName))
			item.Show()
		} else {
			item.Hide()
		}
	}
}

func loadModel(idx int) {
	if idx < 0 || idx >= len(currentModels) {
		return
	}

	entry := currentModels[idx]

	runningModelsMu.Lock()
	instanceNum := 1
	for _, inst := range runningModels {
		if inst.entry.firstPart == entry.firstPart {
			if inst.instanceNum >= instanceNum {
				instanceNum = inst.instanceNum + 1
			}
		}
	}

	port := config.BasePort + modelCounter
	modelCounter++

	instance := &modelInstance{
		entry:       entry,
		port:        port,
		instanceNum: instanceNum,
	}

	key := fmt.Sprintf("%s#%d", entry.firstPart, instanceNum)
	runningModels[key] = instance
	runningModelsMu.Unlock()

	go runLlamaServer(instance)

	refreshMenuState()
}

func unloadModelByMenuIndex(menuIdx int) {
	runningModelsMu.Lock()

	type kv struct {
		key string
		val *modelInstance
	}
	var instances []kv
	for k, v := range runningModels {
		instances = append(instances, kv{k, v})
	}

	for i := 0; i < len(instances); i++ {
		for j := i + 1; j < len(instances); j++ {
			if instances[i].val.port > instances[j].val.port {
				instances[i], instances[j] = instances[j], instances[i]
			}
		}
	}

	if menuIdx >= len(instances) {
		runningModelsMu.Unlock()
		return
	}

	key := instances[menuIdx].key
	instance := instances[menuIdx].val
	runningModelsMu.Unlock()

	stopModelInstance(instance)

	runningModelsMu.Lock()
	delete(runningModels, key)
	runningModelsMu.Unlock()

	refreshMenuState()
}

func stopModelInstance(instance *modelInstance) {
	if instance.cmd != nil && instance.cmd.Process != nil {
		if err := instance.cmd.Process.Kill(); err != nil {
			log.Printf("Failed to kill llama-server process (port %d): %v", instance.port, err)
		} else {
			_, waitErr := instance.cmd.Process.Wait()
			if waitErr != nil {
				log.Printf("Error waiting for process to exit (port %d): %v", instance.port, waitErr)
			} else {
				log.Printf("Stopped model %s (port %d)", instance.entry.display, instance.port)
			}
		}
		instance.cmd = nil
	}
}

func stopAllModels() {
	runningModelsMu.Lock()
	instances := make([]*modelInstance, 0, len(runningModels))
	for _, v := range runningModels {
		instances = append(instances, v)
	}
	runningModelsMu.Unlock()

	for _, inst := range instances {
		stopModelInstance(inst)
	}

	runningModelsMu.Lock()
	runningModels = make(map[string]*modelInstance)
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
		log.Printf("Failed to start llama-server (port %d): %v", instance.port, err)

		if config.Notifications {
			if err := extractIconForNotification(); err != nil {
				log.Printf("Warning: Failed to extract icon: %v", err)
			}

			notification := toast.Notification{
				AppID:   "lmgo Server",
				Title:   "Model Load Failed",
				Message: fmt.Sprintf("Model '%s' failed to load\nPort: %d\nError: %v", instance.entry.display, instance.port, err),
				Icon:    iconTempPath,
			}

			if notifyErr := notification.Push(); notifyErr != nil {
				log.Printf("Failed to send load failure notification: %v", notifyErr)
			}
		}

		runningModelsMu.Lock()
		for k, v := range runningModels {
			if v == instance {
				delete(runningModels, k)
				break
			}
		}
		runningModelsMu.Unlock()

		go func() {
			time.Sleep(100 * time.Millisecond)
			refreshMenuState()
		}()
		return
	}

	go func() {
		time.Sleep(1 * time.Second)
		if config.Notifications {
			if err := extractIconForNotification(); err != nil {
				log.Printf("Warning: Failed to extract icon: %v", err)
			}

			notification := toast.Notification{
				AppID:   "lmgo Server",
				Title:   "Model Loaded Successfully",
				Message: fmt.Sprintf("Model '%s' loaded successfully\nPort: %d", instance.entry.display, instance.port),
				Icon:    iconTempPath,
			}

			if notifyErr := notification.Push(); notifyErr != nil {
				log.Printf("Failed to send load success notification: %v", notifyErr)
			}
		}
	}()

	err := cmd.Wait()
	if err != nil {
		log.Printf("llama-server (port %d) exited abnormally: %v", instance.port, err)

		if config.Notifications {
			if err := extractIconForNotification(); err != nil {
				log.Printf("Warning: Failed to extract icon: %v", err)
			}

			notification := toast.Notification{
				AppID:   "lmgo Server",
				Title:   "Model Stopped",
				Message: fmt.Sprintf("Model '%s' has stopped running\nPort: %d", instance.entry.display, instance.port),
				Icon:    iconTempPath,
			}

			if notifyErr := notification.Push(); notifyErr != nil {
				log.Printf("Failed to send model stopped notification: %v", notifyErr)
			}
		}

		runningModelsMu.Lock()
		for k, v := range runningModels {
			if v == instance {
				delete(runningModels, k)
				break
			}
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
		log.Printf("Failed to open registry: %v", err)
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
		log.Printf("Failed to open registry: %v", err)
		return
	}
	defer k.Close()

	exePath, err := os.Executable()
	if err != nil {
		log.Printf("Failed to get executable path: %v", err)
		return
	}

	if menuItems.autostart.Checked() {
		err = k.DeleteValue("LLMServerTray")
		if err != nil {
			log.Printf("Failed to delete registry value: %v", err)
		} else {
			menuItems.autostart.Uncheck()
			log.Println("Disabled auto-start on boot")
		}
	} else {
		err = k.SetStringValue("LLMServerTray", fmt.Sprintf(`"%s"`, exePath))
		if err != nil {
			log.Printf("Failed to set registry value: %v", err)
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
