package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/daemon"
)

// Run launches the full-screen dashboard and blocks until the user
// quits. It is this package's entire public surface — see
// boundary_test.go for the dependency rule that keeps it that way.
func Run() error {
	if err := daemon.EnsureRunning(); err != nil {
		return err
	}
	p := tea.NewProgram(newStatusModel(), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
