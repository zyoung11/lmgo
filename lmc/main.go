package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

const baseURL = "http://192.168.31.170:9696"

var (
	primaryColor   = lipgloss.Color("#7C3AED")
	successColor   = lipgloss.Color("#10B981")
	warningColor   = lipgloss.Color("#F59E0B")
	errorColor     = lipgloss.Color("#EF4444")
	textColor      = lipgloss.Color("#E5E7EB")
	dimColor       = lipgloss.Color("#6B7280")
	highlightColor = lipgloss.Color("#3B82F6")
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(primaryColor).
			Padding(0, 2).
			MarginBottom(1)

	statusStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(primaryColor).
			Padding(1, 2).
			Width(50)

	modelListStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(dimColor).
			Padding(1)

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(highlightColor).
			Bold(true)

	normalStyle = lipgloss.NewStyle().
			Foreground(textColor)

	healthGoodStyle = lipgloss.NewStyle().
			Foreground(successColor).
			Bold(true)

	healthBadStyle = lipgloss.NewStyle().
			Foreground(errorColor).
			Bold(true)

	loadingStyle = lipgloss.NewStyle().
			Foreground(warningColor)

	helpStyle = lipgloss.NewStyle().
			Foreground(dimColor).
			MarginTop(1)

	successStyle   = lipgloss.NewStyle().Foreground(successColor)
	highlightStyle = lipgloss.NewStyle().Foreground(highlightColor)
	dimStyle       = lipgloss.NewStyle().Foreground(dimColor)
)

type ModelInfo struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	Filename string `json:"path"`
}

type ServerStatus struct {
	Loaded bool `json:"loaded"`
	Model  struct {
		BaseName string `json:"baseName"`
		Path     string `json:"path"`
	} `json:"model"`
}

type HealthStatus struct {
	Status string `json:"status"`
}

type appState int

const (
	stateIdle appState = iota
	stateLoading
	stateUnloading
)

type mainModel struct {
	state         appState
	models        []ModelInfo
	currentStatus ServerStatus
	health        HealthStatus
	selectedIndex int
	cursor        int
	err           error
	spinner       spinner.Model
	help          help.Model
	keys          keyMap
	lastUpdate    time.Time
	loadingStart  time.Time
	width         int
	height        int
	client        *http.Client
	message       string
	messageType   string
}

type keyMap struct {
	Up     key.Binding
	Down   key.Binding
	Load   key.Binding
	Unload key.Binding
	Quit   key.Binding
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Load, k.Unload, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down},
		{k.Load, k.Unload},
		{k.Quit},
	}
}

func newKeyMap() keyMap {
	return keyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "Move up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "Move down"),
		),
		Load: key.NewBinding(
			key.WithKeys("enter", "l"),
			key.WithHelp("enter/l", "Load selected model"),
		),
		Unload: key.NewBinding(
			key.WithKeys("u", "backspace"),
			key.WithHelp("u/⌫", "Unload model"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q/ctrl+c", "Exit"),
		),
	}
}

type tickMsg time.Time
type modelsMsg []ModelInfo
type statusMsg ServerStatus
type healthMsg HealthStatus
type errMsg error
type operationDoneMsg struct {
	success bool
	message string
}

func initialModel() mainModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = loadingStyle

	return mainModel{
		state:         stateIdle,
		spinner:       s,
		help:          help.New(),
		keys:          newKeyMap(),
		client:        &http.Client{Timeout: 10 * time.Second},
		health:        HealthStatus{Status: "checking"},
		selectedIndex: -1,
		cursor:        0,
	}
}

func (m mainModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.fetchModels(),
		m.fetchStatus(),
		m.fetchHealth(),
		tickCmd(),
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m mainModel) fetchModels() tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.Get(baseURL + "/api/models")
		if err != nil {
			return errMsg(err)
		}
		defer resp.Body.Close()

		var r struct {
			Success bool            `json:"success"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return errMsg(err)
		}

		var models []ModelInfo
		if err := json.Unmarshal(r.Data, &models); err != nil {
			return errMsg(err)
		}

		return modelsMsg(models)
	}
}

func (m mainModel) fetchStatus() tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.Get(baseURL + "/api/status")
		if err != nil {
			return errMsg(err)
		}
		defer resp.Body.Close()

		var r struct {
			Success bool            `json:"success"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return errMsg(err)
		}

		var status ServerStatus
		if err := json.Unmarshal(r.Data, &status); err != nil {
			return errMsg(err)
		}

		return statusMsg(status)
	}
}

func (m mainModel) fetchHealth() tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.Get(baseURL + "/api/health")
		if err != nil {
			return healthMsg{Status: "error"}
		}
		defer resp.Body.Close()

		var h HealthStatus
		if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
			return healthMsg{Status: "error"}
		}

		return healthMsg(h)
	}
}

func (m mainModel) loadModel(index int) tea.Cmd {
	return func() tea.Msg {
		url := fmt.Sprintf("%s/api/load?index=%d", baseURL, index)
		req, _ := http.NewRequest("POST", url, strings.NewReader(""))

		resp, err := m.client.Do(req)
		if err != nil {
			return operationDoneMsg{success: false, message: "Load failed: " + err.Error()}
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		var r struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
		}
		json.Unmarshal(body, &r)

		if r.Success {
			return operationDoneMsg{success: true, message: "Loading model..."}
		}
		return operationDoneMsg{success: false, message: r.Message}
	}
}

func (m mainModel) unloadModel() tea.Cmd {
	return func() tea.Msg {
		req, _ := http.NewRequest("POST", baseURL+"/api/unload", strings.NewReader(""))

		resp, err := m.client.Do(req)
		if err != nil {
			return operationDoneMsg{success: false, message: "Unload failed: " + err.Error()}
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		var r struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
		}
		json.Unmarshal(body, &r)

		if r.Success {
			return operationDoneMsg{success: true, message: "Model unloaded"}
		}
		return operationDoneMsg{success: false, message: r.Message}
	}
}

func (m mainModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.help.Width = msg.Width

	case tea.KeyMsg:
		if m.state != stateIdle {
			switch {
			case key.Matches(msg, m.keys.Quit):
				return m, tea.Quit
			}
			return m, nil
		}

		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit

		case key.Matches(msg, m.keys.Up):
			if m.cursor > 0 {
				m.cursor--
			}

		case key.Matches(msg, m.keys.Down):
			if m.cursor < len(m.models)-1 {
				m.cursor++
			}

		case key.Matches(msg, m.keys.Load):
			if len(m.models) > 0 && !m.currentStatus.Loaded {
				m.state = stateLoading
				m.loadingStart = time.Now()
				m.message = "Loading model..."
				m.messageType = "info"
				return m, tea.Batch(
					m.loadModel(m.models[m.cursor].Index),
					m.spinner.Tick,
				)
			} else if m.currentStatus.Loaded {
				m.message = "Please unload current model first"
				m.messageType = "error"
			}

		case key.Matches(msg, m.keys.Unload):
			if m.currentStatus.Loaded {
				m.state = stateUnloading
				m.message = "Unloading..."
				m.messageType = "info"
				return m, tea.Batch(
					m.unloadModel(),
					m.spinner.Tick,
				)
			} else {
				m.message = "No loaded model"
				m.messageType = "error"
			}
		}

	case tickMsg:
		m.lastUpdate = time.Time(msg)
		return m, tea.Batch(
			tickCmd(),
			m.fetchStatus(),
			m.fetchHealth(),
		)

	case modelsMsg:
		m.models = msg
		if len(m.models) > 0 && m.cursor >= len(m.models) {
			m.cursor = len(m.models) - 1
		}

	case statusMsg:
		prevLoaded := m.currentStatus.Loaded
		m.currentStatus = ServerStatus(msg)

		if m.state == stateLoading && m.currentStatus.Loaded {
			m.state = stateIdle
			m.message = fmt.Sprintf("✓ Model [%s] loaded successfully", m.currentStatus.Model.BaseName)
			m.messageType = "success"
		} else if m.state == stateUnloading && !m.currentStatus.Loaded && prevLoaded {
			m.state = stateIdle
			m.message = "✓ Model unloaded"
			m.messageType = "success"
		}

	case healthMsg:
		m.health = HealthStatus(msg)

	case operationDoneMsg:
		if !msg.success {
			m.state = stateIdle
			m.message = "✗ " + msg.message
			m.messageType = "error"
		}

	case errMsg:
		m.err = msg
		m.message = "✗ " + msg.Error()
		m.messageType = "error"

	default:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m mainModel) View() string {
	if m.width == 0 {
		return "Initializing..."
	}

	var b strings.Builder

	b.WriteString("\n")
	title := titleStyle.Render("lmgo Control")
	b.WriteString(title)
	b.WriteString("\n\n")

	b.WriteString(m.renderStatusPanel())
	b.WriteString("\n\n")

	b.WriteString(m.renderModelList())
	b.WriteString("\n\n")

	if m.message != "" {
		b.WriteString(m.renderMessage())
		b.WriteString("\n\n")
	}

	b.WriteString(helpStyle.Render(m.help.View(m.keys)))

	b.WriteString("\n")

	return b.String()
}

func (m mainModel) renderStatusPanel() string {

	healthStr := "● Service Status: "
	if m.health.Status == "ok" {
		healthStr += healthGoodStyle.Render("Healthy")
	} else {
		healthStr += healthBadStyle.Render("Unhealthy")
	}

	modelStr := "● Model Status: "
	if m.currentStatus.Loaded {
		modelStr += successStyle.Render("Loaded")
		modelStr += fmt.Sprintf(" [%s]", highlightStyle.Render(m.currentStatus.Model.BaseName))
	} else {
		modelStr += dimStyle.Render("Not Loaded")
	}

	opStr := ""
	switch m.state {
	case stateLoading:
		opStr = fmt.Sprintf("\n%s Loading model %s",
			m.spinner.View(),
			loadingStyle.Render(fmt.Sprintf("(%ds)", int(time.Since(m.loadingStart).Seconds()))))
	case stateUnloading:
		opStr = fmt.Sprintf("\n%s Unloading model...", m.spinner.View())
	}

	content := fmt.Sprintf("%s\n%s%s", healthStr, modelStr, opStr)

	return statusStyle.Render(content)
}

func (m mainModel) renderModelList() string {
	if len(m.models) == 0 {
		return modelListStyle.Render(dimStyle.Render("  No models available  "))
	}

	rows := [][]string{}

	for i, model := range m.models {
		cursor := "  "
		style := normalStyle

		if i == m.cursor {
			cursor = "▸ "
			style = selectedStyle
		}

		name := model.Name
		if m.currentStatus.Loaded && m.currentStatus.Model.BaseName == model.Name {
			name = successStyle.Render("✓ " + model.Name)
		} else if i == m.cursor {
			name = style.Render(model.Name)
		}

		rows = append(rows, []string{
			style.Render(cursor + strconv.Itoa(model.Index)),
			name,
			dimStyle.Render(truncateString(model.Filename, 30)),
		})
	}

	t := table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(dimColor)).
		Headers(
			lipgloss.NewStyle().Bold(true).Render("ID"),
			lipgloss.NewStyle().Bold(true).Render("Model Name"),
			lipgloss.NewStyle().Bold(true).Render("Filename"),
		).
		Rows(rows...).
		Width(70)

	return modelListStyle.Render(t.Render())
}

func (m mainModel) renderMessage() string {
	switch m.messageType {
	case "success":
		return lipgloss.NewStyle().
			Foreground(successColor).
			Bold(true).
			Render(m.message)
	case "error":
		return lipgloss.NewStyle().
			Foreground(errorColor).
			Bold(true).
			Render(m.message)
	default:
		return lipgloss.NewStyle().
			Foreground(warningColor).
			Render(m.message)
	}
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func main() {
	p := tea.NewProgram(
		initialModel(),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v", err)
		os.Exit(1)
	}
}
