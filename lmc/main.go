package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type ModelInfo struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Path  string `json:"path"`
}

type ModelsResponse struct {
	Success bool        `json:"success"`
	Data    []ModelInfo `json:"data"`
}

type StatusData struct {
	Loaded bool `json:"loaded"`
	Model  struct {
		BaseName string `json:"baseName"`
		Path     string `json:"path"`
	} `json:"model"`
}

type StatusResponse struct {
	Success bool       `json:"success"`
	Data    StatusData `json:"data"`
}

type HealthStatus struct {
	Status string `json:"status"`
}

type SimpleResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type AppState int

const (
	StateLoading AppState = iota
	StateReady
	StateModelSelected
	StateLoadingModel
	StateUnloadingModel
	StateSuccess
	StateError
)

type Model struct {
	state   AppState
	baseURL string

	models      []ModelInfo
	selectedIdx int

	health      string
	loadedModel string
	lastStatus  time.Time
	statusError bool

	message       string
	messageTime   time.Time
	operationTime time.Duration

	loadingDots  int
	windowWidth  int
	windowHeight int
	showHelp     bool
}

type tickMsg time.Time
type modelsMsg ModelsResponse
type statusMsg StatusResponse
type healthMsg HealthStatus
type loadMsg SimpleResponse
type unloadMsg SimpleResponse
type errorMsg string
type successMsg struct {
	message string
	time    time.Duration
}

func fetchModels(baseURL string) tea.Cmd {
	return func() tea.Msg {
		resp, err := http.Get(baseURL + "/api/models")
		if err != nil {
			return errorMsg(fmt.Sprintf("Failed to fetch models: %v", err))
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return errorMsg(fmt.Sprintf("Failed to read response: %v", err))
		}

		var data ModelsResponse
		if err := json.Unmarshal(body, &data); err != nil {
			return errorMsg(fmt.Sprintf("Failed to parse models list: %v", err))
		}

		return modelsMsg(data)
	}
}

func fetchStatus(baseURL string) tea.Cmd {
	return func() tea.Msg {
		resp, err := http.Get(baseURL + "/api/status")
		if err != nil {
			return errorMsg(fmt.Sprintf("Failed to fetch status: %v", err))
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return errorMsg(fmt.Sprintf("Failed to read status: %v", err))
		}

		var data StatusResponse
		if err := json.Unmarshal(body, &data); err != nil {
			return errorMsg(fmt.Sprintf("Failed to parse status: %v", err))
		}

		return statusMsg(data)
	}
}

func fetchHealth(baseURL string) tea.Cmd {
	return func() tea.Msg {
		resp, err := http.Get(baseURL + "/api/health")
		if err != nil {
			return errorMsg(fmt.Sprintf("Health check failed: %v", err))
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return errorMsg(fmt.Sprintf("Failed to read health status: %v", err))
		}

		var data HealthStatus
		if err := json.Unmarshal(body, &data); err != nil {
			return errorMsg(fmt.Sprintf("Failed to parse health status: %v", err))
		}

		return healthMsg(data)
	}
}

func loadModel(baseURL string, index int) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		url := fmt.Sprintf("%s/api/load?index=%d", baseURL, index)
		resp, err := http.Post(url, "application/json", nil)
		if err != nil {
			return errorMsg(fmt.Sprintf("Failed to load model: %v", err))
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return errorMsg(fmt.Sprintf("Failed to read response: %v", err))
		}

		var data SimpleResponse
		if err := json.Unmarshal(body, &data); err != nil {
			return errorMsg(fmt.Sprintf("Failed to parse response: %v", err))
		}

		if !data.Success {
			return errorMsg(fmt.Sprintf("Load failed: %s", data.Message))
		}

		elapsed := time.Since(start)
		return successMsg{message: data.Message, time: elapsed}
	}
}

func unloadModel(baseURL string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		url := baseURL + "/api/unload"
		resp, err := http.Post(url, "application/json", nil)
		if err != nil {
			return errorMsg(fmt.Sprintf("Failed to unload model: %v", err))
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return errorMsg(fmt.Sprintf("Failed to read response: %v", err))
		}

		var data SimpleResponse
		if err := json.Unmarshal(body, &data); err != nil {
			return errorMsg(fmt.Sprintf("Failed to parse response: %v", err))
		}

		if !data.Success {
			return errorMsg(fmt.Sprintf("Unload failed: %s", data.Message))
		}

		elapsed := time.Since(start)
		return successMsg{message: data.Message, time: elapsed}
	}
}

func NewModel() Model {
	return Model{
		baseURL:     "http://192.168.31.170:9696",
		state:       StateLoading,
		selectedIdx: 0,
		health:      "Checking...",
		loadedModel: "None",
		showHelp:    true,
		loadingDots: 0,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		fetchModels(m.baseURL),
		fetchStatus(m.baseURL),
		fetchHealth(m.baseURL),
		tickCmd(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		return handleKeyMsg(m, msg)

	case tea.WindowSizeMsg:
		m.windowWidth = msg.Width
		m.windowHeight = msg.Height
		return m, nil

	case tickMsg:
		m.loadingDots = (m.loadingDots + 1) % 4

		if time.Since(m.lastStatus) > 2*time.Second {
			m.lastStatus = time.Now()
			cmds = append(cmds, fetchStatus(m.baseURL), fetchHealth(m.baseURL))
		}

		if m.state == StateSuccess || m.state == StateError {
			if time.Since(m.messageTime) > 3*time.Second {
				m.state = StateReady
			}
		}
		return m, tea.Batch(append(cmds, tickCmd())...)

	case modelsMsg:
		m.models = msg.Data
		if len(m.models) > 0 {
			m.state = StateReady
		}
		return m, nil

	case statusMsg:
		if msg.Success {
			m.statusError = false
			if msg.Data.Loaded {
				m.loadedModel = msg.Data.Model.BaseName
			} else {
				m.loadedModel = "None"
			}
		}
		return m, nil

	case healthMsg:
		m.health = msg.Status
		return m, nil

	case loadMsg:
		if msg.Success {
			m.state = StateSuccess
			m.message = fmt.Sprintf("✓ Load successful: %s", msg.Message)
		} else {
			m.state = StateError
			m.message = fmt.Sprintf("✗ Load failed: %s", msg.Message)
		}
		m.messageTime = time.Now()
		return m, fetchStatus(m.baseURL)

	case unloadMsg:
		if msg.Success {
			m.state = StateSuccess
			m.message = fmt.Sprintf("✓ Unload successful: %s", msg.Message)
		} else {
			m.state = StateError
			m.message = fmt.Sprintf("✗ Unload failed: %s", msg.Message)
		}
		m.messageTime = time.Now()
		return m, fetchStatus(m.baseURL)

	case successMsg:
		m.state = StateSuccess
		m.message = fmt.Sprintf("✓ %s (took: %v)", msg.message, msg.time)
		m.operationTime = msg.time
		m.messageTime = time.Now()
		return m, fetchStatus(m.baseURL)

	case errorMsg:
		m.state = StateError
		m.message = fmt.Sprintf("✗ %s", string(msg))
		m.messageTime = time.Now()
		return m, nil
	}

	return m, nil
}

func handleKeyMsg(m Model, msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit

	case "h":
		m.showHelp = !m.showHelp
		return m, nil

	case "up", "k":
		if m.state == StateReady || m.state == StateModelSelected {
			m.selectedIdx = max(0, m.selectedIdx-1)
			if m.state == StateReady {
				m.state = StateModelSelected
			}
		}
		return m, nil

	case "down", "j":
		if m.state == StateReady || m.state == StateModelSelected {
			m.selectedIdx = min(len(m.models)-1, m.selectedIdx+1)
			if m.state == StateReady {
				m.state = StateModelSelected
			}
		}
		return m, nil

	case "enter":
		if m.state == StateReady || m.state == StateModelSelected {
			if m.selectedIdx >= 0 && m.selectedIdx < len(m.models) {
				m.state = StateLoadingModel
				return m, loadModel(m.baseURL, m.selectedIdx)
			}
		}
		return m, nil

	case "u":
		if m.state == StateReady || m.state == StateModelSelected {
			m.state = StateUnloadingModel
			return m, unloadModel(m.baseURL)
		}
		return m, nil

	case "r":
		m.state = StateLoading
		return m, tea.Batch(
			fetchModels(m.baseURL),
			fetchStatus(m.baseURL),
			fetchHealth(m.baseURL),
		)
	}

	return m, nil
}

func (m Model) View() string {
	if m.windowWidth == 0 || m.windowHeight == 0 {
		return "Initializing..."
	}

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(lipgloss.Color("#7C3AED")).
		Padding(0, 2).
		MarginBottom(1)

	sectionStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Margin(0, 1, 1, 0)

	statusGood := lipgloss.NewStyle().Foreground(lipgloss.Color("46")).Bold(true)
	statusBad := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	statusNeutral := lipgloss.NewStyle().Foreground(lipgloss.Color("220"))

	selectedStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("63")).
		Foreground(lipgloss.Color("255")).
		Bold(true).
		Padding(0, 1)

	modelItemStyle := lipgloss.NewStyle().
		Padding(0, 1).
		Margin(0, 0, 0, 0)

	messageSuccess := lipgloss.NewStyle().
		Foreground(lipgloss.Color("46")).
		Bold(true)

	messageError := lipgloss.NewStyle().
		Foreground(lipgloss.Color("196")).
		Bold(true)

	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Italic(true)

	title := titleStyle.Render("lmgo Control")

	var modelList string
	if m.state == StateLoading && len(m.models) == 0 {
		loadingText := "Loading models list"
		dots := ""
		for i := 0; i < m.loadingDots; i++ {
			dots += "."
		}
		modelList = fmt.Sprintf("%s%s", loadingText, dots)
	} else if len(m.models) == 0 {
		modelList = "No available models found"
	} else {
		for i, model := range m.models {
			item := fmt.Sprintf("%d. %s", i+1, model.Name)
			if i == m.selectedIdx {
				item = selectedStyle.Render(fmt.Sprintf("➤ %s", item))
			} else {
				item = modelItemStyle.Render(fmt.Sprintf("  %s", item))
			}
			modelList += item + "\n"
		}
	}

	modelPanel := sectionStyle.Width(m.windowWidth/2 - 4).
		Height(m.windowHeight/2 - 2).
		Render(fmt.Sprintf("Available Models (%d)\n\n%s", len(m.models), modelList))

	healthStatus := statusNeutral.Render(m.health)
	if m.health == "ok" {
		healthStatus = statusGood.Render("✓ Healthy")
	} else if m.statusError {
		healthStatus = statusBad.Render("✗ Error")
	}

	modelStatus := statusNeutral.Render(m.loadedModel)
	if m.loadedModel != "无" && m.loadedModel != "" {
		modelStatus = statusGood.Render("✓ " + m.loadedModel)
	}

	statusPanel := sectionStyle.Width(m.windowWidth/2 - 4).
		Height(m.windowHeight/2 - 2).
		Render(fmt.Sprintf(
			"Health Status: %s\n\n"+
				"Current Model: %s\n\n"+
				"Last Updated: %s",
			healthStatus,
			modelStatus,
			m.lastStatus.Format("15:04:05")))

	var actionPanel string
	switch m.state {
	case StateLoading:
		actionPanel = "Initializing..."
	case StateLoadingModel:
		loadingText := "Loading model"
		dots := ""
		for i := 0; i < m.loadingDots; i++ {
			dots += "."
		}
		actionPanel = fmt.Sprintf("%s%s", loadingText, dots)
	case StateUnloadingModel:
		loadingText := "Unloading model"
		dots := ""
		for i := 0; i < m.loadingDots; i++ {
			dots += "."
		}
		actionPanel = fmt.Sprintf("%s%s", loadingText, dots)
	case StateSuccess:
		actionPanel = messageSuccess.Render(m.message)
	case StateError:
		actionPanel = messageError.Render(m.message)
	default:
		if len(m.models) > 0 && m.selectedIdx >= 0 && m.selectedIdx < len(m.models) {
			selectedModel := m.models[m.selectedIdx]
			actionPanel = fmt.Sprintf("Selected: %s", selectedModel.Name)
		} else {
			actionPanel = "Use ↑↓ to select model | Enter to load | U to unload | R to refresh | Q to exit"
		}
	}

	actionPanel = sectionStyle.Width(m.windowWidth - 4).
		Height(1).
		Render(actionPanel)

	var helpPanel string
	if m.showHelp {
		helpText := "↑↓/kj: Select | Enter: Load selected model | U: Unload current model | R: Refresh data | Q/Ctrl+C: Exit"
		helpPanel = helpStyle.Render(helpText)
	}

	topRow := lipgloss.JoinHorizontal(lipgloss.Top, modelPanel, statusPanel)

	fullScreen := lipgloss.JoinVertical(lipgloss.Left,
		title,
		topRow,
		actionPanel,
		helpPanel,
	)

	return lipgloss.Place(m.windowWidth, m.windowHeight,
		lipgloss.Center, lipgloss.Center,
		fullScreen,
		lipgloss.WithWhitespaceChars(""),
		lipgloss.WithWhitespaceForeground(lipgloss.Color("238")),
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func main() {
	p := tea.NewProgram(
		NewModel(),
		tea.WithAltScreen(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Printf("Program error: %v\n", err)
		os.Exit(1)
	}
}
