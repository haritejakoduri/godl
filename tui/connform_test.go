package tui

import (
	"testing"

	"godl/internal/connections"
)

// TestOpenWebDAVBrowserAllowsZeroConnections is a regression test: the
// picker used to refuse to open at all with no saved connections
// (showing a "run godl connection add" message instead), which meant
// the TUI could show a WebDAV connection but never create the first
// one — 'a' from an empty picker must still work.
func TestOpenWebDAVBrowserAllowsZeroConnections(t *testing.T) {
	t.Setenv("GODL_DATA_DIR", t.TempDir())
	m := statusModel{}
	mm, _ := m.openWebDAVBrowser()
	m = mm.(statusModel)
	if m.webdavBrowse == nil {
		t.Fatal("openWebDAVBrowser with zero saved connections should still open the picker")
	}
	if len(m.webdavBrowse.conns) != 0 {
		t.Fatalf("conns = %v, want empty", m.webdavBrowse.conns)
	}
}

func openConnForm(t *testing.T) statusModel {
	t.Helper()
	t.Setenv("GODL_DATA_DIR", t.TempDir())
	return statusModel{webdavBrowse: &webdavBrowseState{step: webdavPickConn, form: newConnForm()}}
}

// editConnField opens field for editing, types text, then presses
// enter to commit it — the same round trip a real keypress sequence
// drives through updateConnForm.
func editConnField(m statusModel, field connFormField, text string) statusModel {
	m.webdavBrowse.form.cursor = field
	mm, _ := m.updateConnForm(key("enter")) // opens the field for editing
	m = mm.(statusModel)
	for _, r := range text {
		mm, _ = m.updateConnForm(key(string(r)))
		m = mm.(statusModel)
	}
	mm, _ = m.updateConnForm(key("enter")) // commits the field
	return mm.(statusModel)
}

// TestConnFormSavesAValidConnection drives the whole form the way a
// user would — name, URL, username, password — then submits it, and
// checks the result actually landed in internal/connections, readable
// by "godl webdav"/"godl connection list" exactly like one added via
// "godl connection add".
func TestConnFormSavesAValidConnection(t *testing.T) {
	m := openConnForm(t)
	m = editConnField(m, connFieldName, "mynas")
	m = editConnField(m, connFieldURL, "https://dav.example.com/")
	m = editConnField(m, connFieldUsername, "alice")
	m = editConnField(m, connFieldPassword, "secret")

	m.webdavBrowse.form.cursor = connFieldSave
	mm, _ := m.updateConnForm(key("enter"))
	m = mm.(statusModel)

	if f := m.webdavBrowse.form; f != nil {
		t.Fatalf("form should close on a successful save, err=%q", f.err)
	}
	c, err := connections.Get("mynas")
	if err != nil {
		t.Fatalf("saved connection not found: %v", err)
	}
	if c.URL != "https://dav.example.com/" || c.Username != "alice" || c.Password != "secret" {
		t.Errorf("saved connection = %+v, fields don't match what was entered", c)
	}
	if len(m.webdavBrowse.conns) != 1 {
		t.Errorf("picker's conns list should refresh to include the new connection, got %v", m.webdavBrowse.conns)
	}
}

// TestConnFormRejectsMissingURL is a regression test: saving with no
// URL used to be indistinguishable from a network failure once it hit
// the daemon — it should be caught right in the form instead.
func TestConnFormRejectsMissingURL(t *testing.T) {
	m := openConnForm(t)
	m = editConnField(m, connFieldName, "mynas")

	m.webdavBrowse.form.cursor = connFieldSave
	mm, _ := m.updateConnForm(key("enter"))
	m = mm.(statusModel)

	if m.webdavBrowse.form == nil || m.webdavBrowse.form.err == "" {
		t.Fatal("saving without a URL should keep the form open with an error")
	}
	if _, err := connections.Get("mynas"); err == nil {
		t.Fatal("an invalid form should not have saved anything")
	}
}

// TestConnFormRejectsBadNameAndScheme checks the same validation "godl
// connection add" applies (cmd/connection.go) is applied here too.
func TestConnFormRejectsBadNameAndScheme(t *testing.T) {
	m := openConnForm(t)
	m = editConnField(m, connFieldName, "my nas!")
	m = editConnField(m, connFieldURL, "https://dav.example.com/")
	m.webdavBrowse.form.cursor = connFieldSave
	mm, _ := m.updateConnForm(key("enter"))
	m = mm.(statusModel)
	if m.webdavBrowse.form == nil || m.webdavBrowse.form.err == "" {
		t.Fatal("a name with spaces/punctuation should be rejected")
	}

	m2 := openConnForm(t)
	m2 = editConnField(m2, connFieldName, "mynas")
	m2 = editConnField(m2, connFieldURL, "ftp://dav.example.com/")
	m2.webdavBrowse.form.cursor = connFieldSave
	mm2, _ := m2.updateConnForm(key("enter"))
	m2 = mm2.(statusModel)
	if m2.webdavBrowse.form == nil || m2.webdavBrowse.form.err == "" {
		t.Fatal("a non-http(s) URL scheme should be rejected")
	}
}

// TestConnFormInsecureToggles checks the checkbox-style field: enter
// flips it immediately, no edit mode, matching the Settings tab's own
// bool fields.
func TestConnFormInsecureToggles(t *testing.T) {
	m := openConnForm(t)
	m.webdavBrowse.form.cursor = connFieldInsecure
	mm, _ := m.updateConnForm(key("enter"))
	m = mm.(statusModel)
	if !m.webdavBrowse.form.insecure {
		t.Fatal("enter on the Insecure field should toggle it on")
	}
	mm, _ = m.updateConnForm(key("enter"))
	m = mm.(statusModel)
	if m.webdavBrowse.form.insecure {
		t.Fatal("enter again should toggle it back off")
	}
}

// TestRemoveConnectionAsksForConfirmation drives the picker's 'd' ->
// y/N flow end to end against a real saved connection.
func TestRemoveConnectionAsksForConfirmation(t *testing.T) {
	t.Setenv("GODL_DATA_DIR", t.TempDir())
	if err := connections.Add(connections.Connection{Name: "mynas", Type: connections.TypeWebDAV, URL: "https://dav.example.com/"}); err != nil {
		t.Fatal(err)
	}
	conns, err := connections.List()
	if err != nil {
		t.Fatal(err)
	}
	m := statusModel{webdavBrowse: &webdavBrowseState{step: webdavPickConn, conns: conns}}

	mm, _ := m.webdavPickConnKey(key("d"))
	m = mm.(statusModel)
	if m.webdavBrowse.confirmRemoveConn != "mynas" {
		t.Fatalf("confirmRemoveConn = %q, want the connection under the cursor armed for removal", m.webdavBrowse.confirmRemoveConn)
	}

	// Anything but y/Y cancels, leaving the connection in place.
	mm, _ = m.webdavPickConnKey(key("n"))
	m = mm.(statusModel)
	if _, err := connections.Get("mynas"); err != nil {
		t.Fatal("declining the confirmation should not remove the connection")
	}

	mm, _ = m.webdavPickConnKey(key("d"))
	m = mm.(statusModel)
	mm, _ = m.webdavPickConnKey(key("y"))
	m = mm.(statusModel)
	if _, err := connections.Get("mynas"); err == nil {
		t.Fatal("confirming with y should remove the connection")
	}
	if len(m.webdavBrowse.conns) != 0 {
		t.Errorf("picker's conns list should refresh after removal, got %v", m.webdavBrowse.conns)
	}
}
