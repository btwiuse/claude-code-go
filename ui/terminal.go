// Package ui provides terminal UI formatting for Claude Code.
package ui

import (
	"fmt"
	"strings"
)

// ANSI color and formatting constants.
const (
	Reset     = "\033[0m"
	Bold      = "\033[1m"
	Dim       = "\033[2m"
	Italic    = "\033[3m"
	Underline = "\033[4m"

	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	White   = "\033[37m"
	Gray    = "\033[90m"

	BgBlue    = "\033[44m"
	BgMagenta = "\033[45m"
)

// FormatHeader returns the formatted application header string.
func FormatHeader(version, model string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s%s╭─────────────────────────────────────────╮%s\n", Bold, Blue, Reset)
	fmt.Fprintf(&b, "%s%s│%s %s%sClaude Code%s (Go) v%s %s%s│%s\n", Bold, Blue, Reset, Bold, White, Reset, version, Bold, Blue, Reset)
	fmt.Fprintf(&b, "%s%s│%s Model: %-33s%s%s│%s\n", Bold, Blue, Reset, model, Bold, Blue, Reset)
	fmt.Fprintf(&b, "%s%s╰─────────────────────────────────────────╯%s", Bold, Blue, Reset)
	return b.String()
}

// FormatWelcome returns the formatted welcome message string.
func FormatWelcome(cwd string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%sTip:%s Use %s/help%s to see available commands, %s/quit%s to exit.\n", Dim, Reset, Bold, Reset, Bold, Reset)
	fmt.Fprintf(&b, "%sCWD:%s %s", Dim, Reset, cwd)
	return b.String()
}

// FormatToolUse returns the formatted tool usage notification string.
func FormatToolUse(name string) string {
	return fmt.Sprintf("\n%s%s  %s%s", Yellow, Bold, name, Reset)
}

// FormatToolResult returns the formatted tool execution result string.
func FormatToolResult(name string, content string, isError bool) string {
	var b strings.Builder
	if isError {
		fmt.Fprintf(&b, "%s%sError:%s %s", Red, Bold, Reset, content)
	} else {
		lines := strings.Split(content, "\n")
		maxLines := 20
		if len(lines) > maxLines {
			for _, line := range lines[:maxLines] {
				fmt.Fprintf(&b, "%s%s%s\n", Gray, line, Reset)
			}
			fmt.Fprintf(&b, "%s... (%d more lines)%s", Dim, len(lines)-maxLines, Reset)
		} else {
			for i, line := range lines {
				fmt.Fprintf(&b, "%s%s%s", Gray, line, Reset)
				if i < len(lines)-1 {
					b.WriteString("\n")
				}
			}
		}
	}
	return b.String()
}

// FormatThinking returns the formatted thinking indicator string.
func FormatThinking(text string) string {
	if text == "" {
		return ""
	}
	return fmt.Sprintf("%s%s%s", Dim, text, Reset)
}

// FormatCostSummary returns the formatted cost summary string.
func FormatCostSummary(summary string) string {
	return fmt.Sprintf("%s%s%s", Dim, summary, Reset)
}

// FormatError returns the formatted error message string.
func FormatError(msg string) string {
	return fmt.Sprintf("%s%sError: %s%s", Red, Bold, msg, Reset)
}

// FormatDivider returns the formatted horizontal divider string.
func FormatDivider() string {
	return fmt.Sprintf("%s%s%s", Dim, strings.Repeat("─", 50), Reset)
}

// FormatHelp returns the formatted help text string.
func FormatHelp() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s%sAvailable Commands:%s\n\n", Bold, Cyan, Reset)
	commands := []struct{ cmd, desc string }{
		{"/help, /h", "Show this help message"},
		{"/quit, /exit, /q", "Exit Claude Code"},
		{"/clear", "Clear conversation history"},
		{"/compact", "Compact conversation to save context"},
		{"/cost", "Show session cost summary"},
		{"/model", "Show current model"},
		{"/version", "Show version"},
		{"/session", "Show session info"},
		{"/config", "Show configuration info"},
		{"/doctor", "Run diagnostics"},
	}
	for _, c := range commands {
		fmt.Fprintf(&b, "  %s%-20s%s %s\n", Bold, c.cmd, Reset, c.desc)
	}
	return b.String()
}

// FormatDoctor returns the formatted doctor diagnostics string.
func FormatDoctor(apiKeyOk, configDirOk, gitRepoOk, ripgrepOk bool, configDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s%sClaude Code Doctor%s\n\n", Bold, Cyan, Reset)

	if apiKeyOk {
		fmt.Fprintf(&b, "  %s✓%s API key configured\n", Green, Reset)
	} else {
		fmt.Fprintf(&b, "  %s✗%s API key not configured\n", Red, Reset)
	}

	if configDirOk {
		fmt.Fprintf(&b, "  %s✓%s Config directory exists: %s\n", Green, Reset, configDir)
	} else {
		fmt.Fprintf(&b, "  %s✗%s Config directory missing: %s\n", Red, Reset, configDir)
	}

	if gitRepoOk {
		fmt.Fprintf(&b, "  %s✓%s Git repository detected\n", Green, Reset)
	} else {
		fmt.Fprintf(&b, "  %s·%s Not in a git repository\n", Yellow, Reset)
	}

	if ripgrepOk {
		fmt.Fprintf(&b, "  %s✓%s ripgrep (rg) available\n", Green, Reset)
	} else {
		fmt.Fprintf(&b, "  %s·%s ripgrep (rg) not found (will fall back to grep)\n", Yellow, Reset)
	}

	return b.String()
}

// PrintAssistantText prints assistant text directly to stdout (for non-interactive mode).
func PrintAssistantText(text string) {
	fmt.Print(text)
}

// PrintToolUse prints a tool usage notification to stdout (for non-interactive mode).
func PrintToolUse(name string) {
	fmt.Println(FormatToolUse(name))
}

// PrintToolResult prints a tool result to stdout (for non-interactive mode).
func PrintToolResult(name string, content string, isError bool) {
	fmt.Println(FormatToolResult(name, content, isError))
}

// PrintThinking prints thinking text to stdout (for non-interactive mode).
func PrintThinking(text string) {
	if text != "" {
		fmt.Print(FormatThinking(text))
	}
}

// PrintError prints an error message to stdout (for non-interactive mode).
func PrintError(msg string) {
	fmt.Println(FormatError(msg))
}

// PrintCostSummary prints a cost summary to stdout (for non-interactive mode).
func PrintCostSummary(summary string) {
	fmt.Println("\n" + FormatCostSummary(summary))
}
