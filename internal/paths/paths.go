// Package paths centralizes the on-disk locations godl uses: the sqlite
// job database, the daemon's Unix socket, its log file, and the default
// torrent data directory.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// DataDir returns the directory godl stores its state in, creating it if
// necessary. It honors GODL_DATA_DIR for overrides (tests, containers).
func DataDir() (string, error) {
	if v := os.Getenv("GODL_DATA_DIR"); v != "" {
		if err := os.MkdirAll(v, 0o700); err != nil {
			return "", err
		}
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".local", "share", "godl")
	// Job records (internal/store) include the full source URL of every
	// download, which can carry auth tokens in the query string (e.g. a
	// CDN link like ...?token=...) — keep the whole tree owner-only so
	// other local users can't read it via the sqlite file or daemon.log.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// DBPath returns the path to the sqlite job database.
func DBPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "godl.db"), nil
}

// SocketPath returns the path to the daemon's Unix domain socket. Unix
// socket paths are capped at ~108 bytes on Linux, so this deliberately
// does NOT live under DataDir() (which can be arbitrarily long, e.g. a
// deeply nested $HOME) — it prefers the short, per-user XDG runtime dir,
// falling back to the OS temp dir.
func SocketPath() (string, error) {
	if v := os.Getenv("GODL_SOCKET_PATH"); v != "" {
		return v, nil
	}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, "godl.sock"), nil
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("godl-%d.sock", os.Getuid())), nil
}

// LogPath returns the path to the daemon's log file.
func LogPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.log"), nil
}

// TorrentDataDir returns the default base directory for torrent downloads
// when the user doesn't pass -o.
func TorrentDataDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(dir, "torrents")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

// PIDPath returns the path to the daemon's pidfile.
func PIDPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.pid"), nil
}

// ConnectionsPath returns the path to the saved remote-storage
// connections file (WebDAV today; other providers can join it later —
// see internal/connections). It lives under DataDir, kept owner-only
// (0700), for the same reason as the job database: this file holds
// plaintext credentials.
func ConnectionsPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "connections.json"), nil
}

// WebDAVDataDir returns the default base directory for WebDAV downloads
// when the user doesn't pass -o.
func WebDAVDataDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(dir, "webdav")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}
