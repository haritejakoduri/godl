package daemon

import "godl/internal/store"

// Request is one client->daemon message, newline-delimited JSON over the
// Unix socket.
type Request struct {
	Cmd string `json:"cmd"`

	// add_url / add_social / add_torrent
	Source      string `json:"source,omitempty"` // URL, magnet link, or .torrent path
	Output      string `json:"output,omitempty"`
	Concurrency int    `json:"concurrency,omitempty"`
	Format      string `json:"format,omitempty"`

	// pause / resume / retry / cancel / remove
	JobID string `json:"job_id,omitempty"`
	// remove: also delete the downloaded file(s), not just the list entry.
	Purge bool `json:"purge,omitempty"`
}

const (
	CmdAddURL     = "add_url"
	CmdAddSocial  = "add_social"
	CmdAddTorrent = "add_torrent"
	CmdPause      = "pause"
	CmdResume     = "resume"
	CmdRetry      = "retry"
	CmdCancel     = "cancel"
	CmdRemove     = "remove"
	CmdList       = "list"
	CmdSubscribe  = "subscribe"
	CmdPing       = "ping"
)

// JobView is a store.Job plus the runtime stats (speed, ETA) the daemon
// tracks in memory but doesn't persist.
type JobView struct {
	*store.Job
	SpeedBps   float64 `json:"speed_bps"`
	ETASeconds int64   `json:"eta_seconds"` // -1 if unknown
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

	// log streaming (add_social)
	JobIDForLog string `json:"job_id_for_log,omitempty"`
	Line        string `json:"line,omitempty"`
	LogDone     bool   `json:"log_done,omitempty"`
}
