package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"

	"github.com/btwiuse/claude-code-go/config"
	"github.com/btwiuse/claude-code-go/constants"
	"github.com/btwiuse/claude-code-go/tools"
	"github.com/btwiuse/claude-code-go/ui"
)

// appState tracks the current state of the interactive TUI.
type appState int

const (
	stateInput      appState = iota // Waiting for user input
	stateProcessing                 // Engine is running
)

// Custom messages for streaming engine output into the bubbletea event loop.
type (
	streamTextMsg       string
	streamToolUseMsg    string
	streamToolResultMsg struct {
		name    string
		content string
		isError bool
	}
	streamThinkingMsg string
	streamErrorMsg    struct{ err error }
	streamDoneMsg     struct{ err error }
)

// appModel is the bubbletea Model for the interactive Claude Code TUI.
type appModel struct {
	app       *App
	state     appState
	textInput textinput.Model
	spinner   spinner.Model
	lineBuf   string // Buffer for current incomplete output line
	ctx       context.Context
	cancel    context.CancelFunc
	quitting  bool
}

// newAppModel creates a new bubbletea model for the interactive TUI.
func newAppModel(app *App) appModel {
	ti := textinput.New()
	ti.Prompt = "claude> "
	ti.CharLimit = 0 // no limit

	sp := spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
	)

	ctx, cancel := context.WithCancel(context.Background())

	return appModel{
		app:       app,
		state:     stateInput,
		textInput: ti,
		spinner:   sp,
		ctx:       ctx,
		cancel:    cancel,
	}
}

func (m appModel) Init() tea.Cmd {
	return tea.Batch(
		m.textInput.Focus(),
		tea.Println(ui.FormatHeader(constants.Version, m.app.model)),
		tea.Println(ui.FormatWelcome(m.app.cwd)),
	)
}

func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			m.cancel()
			return m, tea.Sequence(
				tea.Println(ui.FormatCostSummary(m.app.costTracker.GetSummary())),
				func() tea.Msg { return tea.Quit() },
			)
		case "enter":
			if m.state == stateInput {
				input := strings.TrimSpace(m.textInput.Value())
				if input == "" {
					return m, nil
				}
				m.textInput.SetValue("")

				// Echo the user prompt above the TUI
				cmds = append(cmds, tea.Printf("%sclaude>%s %s", ui.Bold, ui.Reset, input))

				// Handle slash commands
				if strings.HasPrefix(input, "/") {
					cmd := m.handleCommand(input)
					if cmd != nil {
						cmds = append(cmds, cmd)
					}
					return m, tea.Batch(cmds...)
				}

				// Submit to engine
				m.state = stateProcessing
				m.textInput.Blur()
				cmds = append(cmds, m.submitCmd(input))
				return m, tea.Batch(cmds...)
			}
		}

	case streamTextMsg:
		m.lineBuf += string(msg)
		// Flush complete lines via tea.Println
		for {
			idx := strings.IndexByte(m.lineBuf, '\n')
			if idx < 0 {
				break
			}
			cmds = append(cmds, tea.Println(m.lineBuf[:idx]))
			m.lineBuf = m.lineBuf[idx+1:]
		}

	case streamToolUseMsg:
		// Flush any pending text first
		if m.lineBuf != "" {
			cmds = append(cmds, tea.Println(m.lineBuf))
			m.lineBuf = ""
		}
		cmds = append(cmds, tea.Println(ui.FormatToolUse(string(msg))))

	case streamToolResultMsg:
		cmds = append(cmds, tea.Println(ui.FormatToolResult(msg.name, msg.content, msg.isError)))

	case streamThinkingMsg:
		m.lineBuf += string(msg)
		// Flush complete lines for thinking output
		for {
			idx := strings.IndexByte(m.lineBuf, '\n')
			if idx < 0 {
				break
			}
			cmds = append(cmds, tea.Println(ui.FormatThinking(m.lineBuf[:idx])))
			m.lineBuf = m.lineBuf[idx+1:]
		}

	case streamErrorMsg:
		cmds = append(cmds, tea.Println(ui.FormatError(msg.err.Error())))

	case streamDoneMsg:
		// Flush remaining buffer
		if m.lineBuf != "" {
			cmds = append(cmds, tea.Println(m.lineBuf))
			m.lineBuf = ""
		}
		if msg.err != nil {
			cmds = append(cmds, tea.Println(ui.FormatError(msg.err.Error())))
		}
		cmds = append(cmds, tea.Println("")) // blank line after response
		cmds = append(cmds, tea.Println(ui.FormatCostSummary(m.app.costTracker.GetSummary())))

		// Save session
		m.app.session.Messages = m.app.engine.GetMessages()
		if err := m.app.session.Save(); err != nil && m.app.verbose {
			cmds = append(cmds, tea.Println(ui.FormatError(fmt.Sprintf("Failed to save session: %v", err))))
		}

		// Switch back to input mode
		m.state = stateInput
		cmds = append(cmds, m.textInput.Focus())

	case spinner.TickMsg:
		if m.state == stateProcessing {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	// Update textinput when in input state
	if m.state == stateInput && !m.quitting {
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m appModel) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}

	var s strings.Builder

	switch m.state {
	case stateInput:
		s.WriteString(m.textInput.View())
	case stateProcessing:
		if m.lineBuf != "" {
			s.WriteString(m.lineBuf)
		}
		s.WriteString("\n")
		s.WriteString(m.spinner.View())
		s.WriteString(" Thinking...")
	}

	return tea.NewView(s.String())
}

// submitCmd returns a tea.Cmd that runs the engine in a goroutine.
func (m appModel) submitCmd(input string) tea.Cmd {
	return func() tea.Msg {
		err := m.app.engine.Submit(m.ctx, input)
		return streamDoneMsg{err: err}
	}
}

// handleCommand processes slash commands and returns appropriate tea.Cmd.
func (m *appModel) handleCommand(input string) tea.Cmd {
	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/quit", "/exit", "/q":
		m.quitting = true
		m.cancel()
		return tea.Sequence(
			tea.Println(ui.FormatCostSummary(m.app.costTracker.GetSummary())),
			func() tea.Msg { return tea.Quit() },
		)

	case "/help", "/h":
		return tea.Println(ui.FormatHelp())

	case "/clear":
		m.app.engine.SetMessages(nil)
		return tea.Println("Conversation cleared.")

	case "/cost":
		return tea.Println(ui.FormatCostSummary(m.app.costTracker.GetSummary()))

	case "/model":
		return tea.Printf("Current model: %s", m.app.model)

	case "/version":
		return tea.Printf("Claude Code (Go) v%s", constants.Version)

	case "/session":
		return tea.Batch(
			tea.Printf("Session ID: %s", m.app.session.ID),
			tea.Printf("Messages: %d", len(m.app.engine.GetMessages())),
		)

	case "/config":
		return tea.Batch(
			tea.Printf("Config directory: %s", config.ConfigDir()),
			tea.Printf("API key configured: %v", config.GetAPIKey() != ""),
		)

	case "/compact":
		msgs := m.app.engine.GetMessages()
		if len(msgs) > 4 {
			m.app.engine.SetMessages(msgs[len(msgs)-4:])
			return tea.Printf("Compacted conversation: kept last %d messages.", 4)
		}
		return tea.Println("Conversation is already compact.")

	case "/doctor":
		return tea.Println(runDoctorCheck())

	default:
		return tea.Printf("Unknown command: %s. Type /help for available commands.", cmd)
	}
}

// runDoctorCheck runs diagnostics and returns the formatted result.
func runDoctorCheck() string {
	apiKeyOk := config.GetAPIKey() != ""
	configDir := config.ConfigDir()
	_, configErr := os.Stat(configDir)
	configDirOk := configErr == nil
	_, gitErr := os.Stat(".git")
	gitRepoOk := gitErr == nil
	_, rgErr := exec.LookPath("rg")
	ripgrepOk := rgErr == nil
	return ui.FormatDoctor(apiKeyOk, configDirOk, gitRepoOk, ripgrepOk, configDir)
}

// wireEngineCallbacks sets up the engine callbacks to send messages to
// the bubbletea program via p.Send().
func wireEngineCallbacks(app *App, p *tea.Program) {
	app.engine.SetOnText(func(text string) {
		p.Send(streamTextMsg(text))
	})
	app.engine.SetOnToolUse(func(name string, _ json.RawMessage) {
		p.Send(streamToolUseMsg(name))
	})
	app.engine.SetOnToolResult(func(name string, result *tools.ToolResult) {
		p.Send(streamToolResultMsg{
			name:    name,
			content: result.Content,
			isError: result.IsError,
		})
	})
	app.engine.SetOnThinking(func(text string) {
		p.Send(streamThinkingMsg(text))
	})
	app.engine.SetOnError(func(err error) {
		p.Send(streamErrorMsg{err: err})
	})
}
