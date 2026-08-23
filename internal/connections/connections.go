// Package connections persists named credentials for remote storage
// backends that godl can pull files from — WebDAV today. It's
// deliberately shaped as a list of typed connections (rather than a
// single "webdav config") so future backends (Google Drive, OneDrive,
// other cloud storage providers) can be added as additional Types
// alongside WebDAV ones without changing the on-disk format or the
// commands built on top of it.
package connections

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"godl/internal/paths"
)

type Type string

const (
	TypeWebDAV Type = "webdav"
	// Future providers (Google Drive, OneDrive, ...) get their own Type
	// values here, plus whatever extra fields they need on Connection.
)

// Connection is one saved set of credentials for a remote storage
// backend, addressed by its unique Name (e.g. "godl webdav mynas /docs"
// uses the connection named "mynas").
type Connection struct {
	Name     string `json:"name"`
	Type     Type   `json:"type"`
	URL      string `json:"url,omitempty"` // base URL, WebDAV connections: includes the http(s) scheme
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// Insecure skips TLS certificate verification, for self-signed
	// https WebDAV servers. Off by default.
	Insecure  bool      `json:"insecure,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type file struct {
	Connections []Connection `json:"connections"`
}

func load() (file, error) {
	path, err := paths.ConnectionsPath()
	if err != nil {
		return file{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return file{}, nil
		}
		return file{}, err
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return file{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return f, nil
}

// save writes via a temp file + rename so a crash mid-write can't leave
// connections.json truncated/corrupt.
func save(f file) error {
	path, err := paths.ConnectionsPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// List returns every saved connection, of any type.
func List() ([]Connection, error) {
	f, err := load()
	if err != nil {
		return nil, err
	}
	return f.Connections, nil
}

// Get looks up a saved connection by name.
func Get(name string) (Connection, error) {
	f, err := load()
	if err != nil {
		return Connection{}, err
	}
	for _, c := range f.Connections {
		if c.Name == name {
			return c, nil
		}
	}
	return Connection{}, fmt.Errorf("connection %q not found (see \"godl connection list\")", name)
}

// Add saves a connection, overwriting any existing one with the same
// name (preserving its original CreatedAt).
func Add(c Connection) error {
	f, err := load()
	if err != nil {
		return err
	}
	for i, existing := range f.Connections {
		if existing.Name == c.Name {
			c.CreatedAt = existing.CreatedAt
			f.Connections[i] = c
			return save(f)
		}
	}
	c.CreatedAt = time.Now()
	f.Connections = append(f.Connections, c)
	return save(f)
}

// Remove deletes a saved connection by name.
func Remove(name string) error {
	f, err := load()
	if err != nil {
		return err
	}
	for i, c := range f.Connections {
		if c.Name == name {
			f.Connections = append(f.Connections[:i], f.Connections[i+1:]...)
			return save(f)
		}
	}
	return fmt.Errorf("connection %q not found", name)
}
