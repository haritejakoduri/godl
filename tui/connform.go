package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/connections"
)

// connNameRe mirrors cmd/connection.go's own validation, so a
// connection saved from the TUI is accepted or rejected under exactly
// the same rule "godl connection add" already enforces.
var connNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type connFormField int

const (
	connFieldName connFormField = iota
	connFieldURL
	connFieldUsername
	connFieldPassword
	connFieldInsecure
	connFieldSave
)

var connFormFieldLabels = map[connFormField]string{
	connFieldName:     "Name",
	connFieldURL:      "URL",
	connFieldUsername: "Username",
	connFieldPassword: "Password",
	connFieldInsecure: "Insecure (skip TLS verify)",
	connFieldSave:     "Save connection",
}

// connFormState is the TUI's "add a WebDAV connection" form, opened
// with 'a' from the connection picker (browse.go) — the TUI equivalent
// of "godl connection add <name> --url ... --username ... --password
// ...". Unlike the Settings tab, a field here isn't saved the instant
// it's edited: a half-filled form isn't a valid connection, so nothing
// is persisted until "Save connection" is actually chosen.
type connFormState struct {
	cursor  connFormField
	editing bool
	input   textinput.Model

	name, url, username, password string
	insecure                      bool
	err                           string
}

func newConnForm() *connFormState {
	return &connFormState{cursor: connFieldName}
}

func connFormValue(f *connFormState, field connFormField) string {
	switch field {
	case connFieldName:
		return f.name
	case connFieldURL:
		return f.url
	case connFieldUsername:
		return f.username
	case connFieldPassword:
		return f.password
	default:
		return ""
	}
}

func connFormSetValue(f *connFormState, field connFormField, v string) {
	switch field {
	case connFieldName:
		f.name = v
	case connFieldURL:
		f.url = v
	case connFieldUsername:
		f.username = v
	case connFieldPassword:
		f.password = v
	}
}

// updateConnForm handles a keypress while the add-connection form is
// focused — routed from webdavPickConnKey when wb.form != nil.
func (m statusModel) updateConnForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.webdavBrowse.form

	if f.editing {
		switch msg.String() {
		case "esc":
			f.editing = false
			return m, nil
		case "enter":
			connFormSetValue(f, f.cursor, f.input.Value())
			f.editing = false
			return m, nil
		default:
			var cmd tea.Cmd
			f.input, cmd = f.input.Update(msg)
			return m, cmd
		}
	}

	switch msg.String() {
	case "esc":
		m.webdavBrowse.form = nil
		return m, nil
	case "up", "k":
		if f.cursor > connFieldName {
			f.cursor--
			f.err = ""
		}
		return m, nil
	case "down", "j":
		if f.cursor < connFieldSave {
			f.cursor++
			f.err = ""
		}
		return m, nil
	case "enter", " ":
		switch f.cursor {
		case connFieldInsecure:
			f.insecure = !f.insecure
			return m, nil
		case connFieldSave:
			return m.submitConnForm()
		default:
			ti := textinput.New()
			ti.SetValue(connFormValue(f, f.cursor))
			ti.CursorEnd()
			ti.Focus()
			ti.CharLimit = 512
			ti.Width = 50
			if f.cursor == connFieldPassword {
				ti.EchoMode = textinput.EchoPassword
				ti.EchoCharacter = '•'
			}
			f.input = ti
			f.editing = true
			f.err = ""
			return m, nil
		}
	}
	return m, nil
}

// submitConnForm validates and saves the form, mirroring "godl
// connection add"'s own validation (cmd/connection.go) so the same
// input is accepted or rejected the same way from either front end.
func (m statusModel) submitConnForm() (tea.Model, tea.Cmd) {
	wb := m.webdavBrowse
	f := wb.form
	name := strings.TrimSpace(f.name)
	url := strings.TrimSpace(f.url)
	switch {
	case name == "":
		f.err = "name is required"
		return m, nil
	case !connNameRe.MatchString(name):
		f.err = "name must contain only letters, digits, - and _"
		return m, nil
	case url == "":
		f.err = "URL is required"
		return m, nil
	case !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://"):
		f.err = "URL must start with http:// or https://"
		return m, nil
	}

	c := connections.Connection{
		Name:     name,
		Type:     connections.TypeWebDAV,
		URL:      url,
		Username: strings.TrimSpace(f.username),
		Password: f.password,
		Insecure: f.insecure,
	}
	if err := connections.Add(c); err != nil {
		f.err = err.Error()
		return m, nil
	}
	conns, err := connections.List()
	if err != nil {
		m.webdavBrowse = nil
		m.statusMsg = "error: " + err.Error()
		return m, nil
	}
	wb.conns = conns
	wb.form = nil
	for i, cn := range conns {
		if cn.Name == name {
			wb.connIndex = i
		}
	}
	wb.connMsg = fmt.Sprintf("saved connection %q", name)
	return m, nil
}

func (m statusModel) viewConnForm() string {
	f := m.webdavBrowse.form
	var b strings.Builder
	b.WriteString(m.wrapped(statStyle).Render("Add a WebDAV connection:"))
	b.WriteString("\n")

	fields := []connFormField{connFieldName, connFieldURL, connFieldUsername, connFieldPassword, connFieldInsecure, connFieldSave}
	for _, field := range fields {
		cursor := "  "
		if field == f.cursor {
			cursor = "> "
		}
		var value string
		switch field {
		case connFieldInsecure:
			value = boolLabel(f.insecure)
		case connFieldSave:
			value = ""
		case connFieldPassword:
			if f.password != "" {
				value = strings.Repeat("•", len(f.password))
			}
		default:
			value = connFormValue(f, field)
		}
		if field == f.cursor && f.editing {
			value = f.input.View()
		}
		fmt.Fprintf(&b, "%s%-28s %s\n", cursor, connFormFieldLabels[field], value)
	}

	if f.err != "" {
		b.WriteString(m.wrapped(errStyle).Render("error: " + f.err))
		b.WriteString("\n")
	}
	if f.editing {
		b.WriteString(m.helpView("enter save field  esc cancel edit"))
	} else {
		b.WriteString(m.helpView("↑/↓ select  enter edit/toggle/save  esc cancel"))
	}
	return b.String()
}
