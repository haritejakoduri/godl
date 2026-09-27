package daemon

import "godl/internal/store"

// Request is one client->daemon message, newline-delimited JSON over the
// Unix socket.
type Request struct {
	Cmd string `json:"cmd"`

	// add_url / add_social / add_torrent / add_webdav
	// add_webdav encodes Source as "<connection-name>:<remote-path>",
	// looked up against internal/connections at start time rather than
	// carrying credentials over this protocol.
	Source      string `json:"source,omitempty"` // URL, magnet link, .torrent path, or webdav "conn:/path"
	Output      string `json:"output,omitempty"`
	Concurrency int    `json:"concurrency,omitempty"`
	Format      string `json:"format,omitempty"`
	// LimitRate caps the new job's transfer at this many bytes/second
	// (0 = unlimited). See store.Job.LimitRate for the torrent caveat.
	LimitRate int64 `json:"limit_rate,omitempty"`
	// Sha256 is the expected hex digest for add_url (empty = skip
	// verification). See store.Job.Sha256.
	Sha256 string `json:"sha256,omitempty"`
	// Options carries the per-job extras (headers, cookies, torrent
	// file selection, seeding limits). See store.JobOptions.
	Options store.JobOptions `json:"options,omitempty"`

	// pause / resume / retry / cancel / remove / stream_torrent
	JobID string `json:"job_id,omitempty"`
	// stream_torrent: which file to stream, 1-based as --list-files
	// numbers them; 0 picks the largest media file.
	FileIndex int `json:"file_index,omitempty"`
	// remove: also delete the downloaded file(s), not just the list entry.
	Purge bool `json:"purge,omitempty"`

	// set_settings: the settings to save. Nil for get_settings (nothing
	// to send) and every other command.
	Settings *store.Settings `json:"settings,omitempty"`
}

const (
	CmdAddURL      = "add_url"
	CmdAddSocial   = "add_social"
	CmdAddTorrent  = "add_torrent"
	CmdAddWebDAV   = "add_webdav"
	CmdPause       = "pause"
	CmdResume      = "resume"
	CmdRetry       = "retry"
	CmdCancel      = "cancel"
	CmdRemove      = "remove"
	CmdList        = "list"
	CmdSubscribe   = "subscribe"
	CmdPing        = "ping"
	CmdGetSettings = "get_settings"
	CmdSetSettings = "set_settings"
	// torrent_files fetches a torrent's metadata (from peers, for a
	// magnet link) and lists its files without downloading anything.
	CmdTorrentFiles = "torrent_files"
	// stream_torrent returns a loopback URL a media player can stream a
	// running torrent job's file from while it downloads.
	CmdStreamTorrent = "stream_torrent"
)

// TorrentFile is one entry of a torrent_files / stream_torrent reply.
type TorrentFile struct {
	Index  int    `json:"index"` // 0-based
	Path   string `json:"path"`
	Length int64  `json:"length"`
	Done   int64  `json:"done,omitempty"`
}

// JobView is a store.Job plus the runtime stats (speed, ETA) the daemon
// tracks in memory but doesn't persist.
type JobView struct {
	*store.Job
	SpeedBps   float64 `json:"speed_bps"`
	ETASeconds int64   `json:"eta_seconds"` // -1 if unknown
	// Only set while Status is seeding.
	UploadBps float64 `json:"upload_bps,omitempty"`
	Ratio     float64 `json:"ratio,omitempty"`
}

// Response is one daemon->client message. Most commands get exactly one;
// "add_social" is followed by a stream of Type:"log" messages until the
// job finishes or the client disconnects; "subscribe" is a stream of
// Type:"snapshot" messages until the client disconnects.
type Response struct {
	Type  string `json:"type"` // "result" | "log" | "snapshot" | "error"
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`

	Job  *JobView   `json:"job,omitempty"`
	Jobs []*JobView `json:"jobs,omitempty"`

	// get_settings / set_settings result: the settings now in effect
	// (set_settings echoes back what was actually saved, after any
	// clamping — see daemon.applySettings).
	Settings *store.Settings `json:"settings,omitempty"`

	// torrent_files / stream_torrent
	Name      string        `json:"name,omitempty"`
	Files     []TorrentFile `json:"files,omitempty"`
	StreamURL string        `json:"stream_url,omitempty"`

	// log streaming (add_social)
	JobIDForLog string `json:"job_id_for_log,omitempty"`
	Line        string `json:"line,omitempty"`
	LogDone     bool   `json:"log_done,omitempty"`
}
