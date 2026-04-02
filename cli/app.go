// Package cli implements the command-line interface for Claude Code.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/btwiuse/claude-code-go/api"
	"github.com/btwiuse/claude-code-go/cost"
	"github.com/btwiuse/claude-code-go/query"
	"github.com/btwiuse/claude-code-go/session"
	"github.com/btwiuse/claude-code-go/tools"
	"github.com/btwiuse/claude-code-go/ui"
)

// App is the main Claude Code application.
type App struct {
	client      *api.Client
	registry    *tools.Registry
	costTracker *cost.Tracker
	session     *session.Session
	engine      *query.Engine
	cwd         string
	model       string
	verbose     bool
}

// RunConfig contains configuration for running the CLI.
type RunConfig struct {
	Model        string
	APIKey       string
	CWD          string
	SessionID    string
	Verbose      bool
	Prompt       string // Non-interactive mode: single prompt
	SystemPrompt string
	MaxTurns     int
}

// Run starts the CLI application.
func Run(cfg RunConfig) error {
	// Determine working directory
	cwd := cfg.CWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
	}

	// Create API client
	clientOpts := []api.ClientOption{
		api.WithSessionID(cfg.SessionID),
	}
	if cfg.Model != "" {
		clientOpts = append(clientOpts, api.WithModel(cfg.Model))
	}
	if cfg.APIKey != "" {
		clientOpts = append(clientOpts, api.WithAPIKey(cfg.APIKey))
	}

	client, err := api.NewClient(clientOpts...)
	if err != nil {
		return fmt.Errorf("creating API client: %w", err)
	}

	// Create tool registry
	registry := tools.DefaultRegistry

	// Create cost tracker
	costTracker := cost.NewTracker()

	// Create or load session
	sessionID := cfg.SessionID
	if sessionID == "" {
		sessionID = generateSessionID()
	}
	sess := session.NewSession(sessionID, cwd, client.GetModel())

	// Build system prompt
	systemPrompt := buildSystemPrompt(cwd, cfg.SystemPrompt)

	// Create tool context
	toolCtx := &tools.ToolContext{
		CWD:           cwd,
		AbortCtx:      context.Background(),
		ReadFileState: tools.NewFileStateCache(),
		Debug:         cfg.Verbose,
		SessionID:     sessionID,
	}

	app := &App{
		client:      client,
		registry:    registry,
		costTracker: costTracker,
		session:     sess,
		cwd:         cwd,
		model:       client.GetModel(),
		verbose:     cfg.Verbose,
	}

	// Create query engine (callbacks are set later based on mode)
	engine := query.NewEngine(query.EngineConfig{
		Client:       client,
		Registry:     registry,
		CostTracker:  costTracker,
		ToolCtx:      toolCtx,
		SystemPrompt: systemPrompt,
		MaxTurns:     cfg.MaxTurns,
	})
	app.engine = engine

	// Handle non-interactive mode
	if cfg.Prompt != "" {
		// Set direct stdout callbacks for non-interactive mode
		engine.SetOnText(ui.PrintAssistantText)
		engine.SetOnToolUse(func(name string, _ json.RawMessage) {
			ui.PrintToolUse(name)
		})
		engine.SetOnToolResult(func(name string, result *tools.ToolResult) {
			ui.PrintToolResult(name, result.Content, result.IsError)
		})
		engine.SetOnThinking(ui.PrintThinking)
		engine.SetOnError(func(err error) {
			ui.PrintError(err.Error())
		})
		return app.runNonInteractive(cfg.Prompt)
	}

	// Interactive mode (bubbletea handles callbacks)
	return app.runInteractive()
}

func (app *App) runNonInteractive(prompt string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	err := app.engine.Submit(ctx, prompt)
	fmt.Println() // Ensure final newline
	return err
}

func (app *App) runInteractive() error {
	m := newAppModel(app)
	p := tea.NewProgram(m)

	// Wire engine callbacks to send messages into the bubbletea event loop
	wireEngineCallbacks(app, p)

	_, err := p.Run()
	return err
}

func buildSystemPrompt(cwd string, custom string) string {
	if custom != "" {
		return custom
	}

	return fmt.Sprintf(`You are Claude Code, an AI assistant by Anthropic, running as a CLI tool.

You are an expert software engineer with deep knowledge of programming languages, frameworks, design patterns, and best practices.

Your current working directory is: %s

You have access to tools for reading/writing files, executing bash commands, searching code, and more.

Guidelines:
- Be concise and direct in your responses
- When editing files, make minimal targeted changes
- Always verify your changes work correctly
- Use the available tools to explore the codebase before making changes
- Explain your reasoning briefly before making changes
- If you're unsure about something, ask for clarification`, cwd)
}

func generateSessionID() string {
	return fmt.Sprintf("session_%d", time.Now().UnixNano())
}
