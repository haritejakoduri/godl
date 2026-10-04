// Package torbox talks to TorBox (torbox.app), a paid service that
// downloads a torrent on its own servers and hands back plain HTTPS
// links to the files. For godl that turns a torrent — slow with few
// peers, and blocked on some networks — into ordinary chunked HTTP
// downloads at the line's full speed. A torrent someone else already
// fetched ("cached") is ready at once.
//
// Only the handful of calls godl needs are here: add a torrent, read
// its state, get a file's link, delete it, and check a key.
package torbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"godl/internal/httpx"
)

// BaseURL is TorBox's API. A variable so tests can point it at a fake;
// GODL_TORBOX_URL does the same for a whole godl process.
var BaseURL = func() string {
	if u := os.Getenv("GODL_TORBOX_URL"); u != "" {
		return strings.TrimSuffix(u, "/")
	}
	return "https://api.torbox.app/v1/api"
}()

// Client calls TorBox with one account's API key.
type Client struct {
	key  string
	base string
	hc   *http.Client
}

// New returns a client for key (from torbox.app → Settings → API key).
func New(key string) *Client {
	return &Client{key: strings.TrimSpace(key), base: BaseURL, hc: httpx.Client(httpx.MetadataTimeout)}
}

// Torrent is one torrent in the account, as mylist reports it.
type Torrent struct {
	ID            int64   `json:"id"`
	Hash          string  `json:"hash"`
	Name          string  `json:"name"`
	Size          int64   `json:"size"`
	DownloadState string  `json:"download_state"`
	Progress      float64 `json:"progress"` // 0..1
	DownloadSpeed float64 `json:"download_speed"`
	ETA           float64 `json:"eta"`
	Seeds         int     `json:"seeds"`
	Peers         int     `json:"peers"`
	// Finished: TorBox has the whole torrent. Present: its files can be
	// downloaded — there's a short gap between the two while TorBox
	// moves them into place, which is normal, not an error.
	DownloadFinished bool   `json:"download_finished"`
	DownloadPresent  bool   `json:"download_present"`
	Files            []File `json:"files"`
}

// File is one file of a Torrent. Name is its path including the
// torrent's own folder ("Show/S01/E01.mkv"); ShortName is the base.
type File struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	ShortName string `json:"short_name"`
	Size      int64  `json:"size"`
}

// Failed reports whether TorBox gave up on the torrent.
func (t *Torrent) Failed() bool {
	s := strings.ToLower(t.DownloadState)
	return strings.Contains(s, "error") || strings.Contains(s, "failed") || s == "missingfiles"
}

// Ready reports whether the files can be downloaded now.
func (t *Torrent) Ready() bool { return t.DownloadFinished && t.DownloadPresent }

// envelope is how every TorBox answer is wrapped.
type envelope struct {
	Success bool            `json:"success"`
	Error   json.RawMessage `json:"error"`
	Detail  string          `json:"detail"`
	Data    json.RawMessage `json:"data"`
}

// APIError is TorBox refusing a request, with its own explanation.
type APIError struct {
	Status int
	Code   string
	Detail string
}

func (e *APIError) Error() string {
	msg := e.Detail
	if msg == "" {
		msg = e.Code
	}
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return "TorBox: " + msg
}

// Unauthorized reports whether err is TorBox rejecting the API key.
func Unauthorized(err error) bool {
	e, ok := err.(*APIError)
	return ok && (e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden ||
		e.Code == "AUTH_ERROR" || e.Code == "BAD_TOKEN" || e.Code == "NO_AUTH")
}

func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		// A *url.Error quotes the whole URL, and requestdl's carries the
		// API key; keep only the cause, so the key never lands in a job's
		// error message.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("TorBox: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("TorBox: %w", err)
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		if resp.StatusCode >= 400 {
			return &APIError{Status: resp.StatusCode}
		}
		return fmt.Errorf("TorBox: unexpected answer (%s)", resp.Status)
	}
	if resp.StatusCode >= 400 || !env.Success {
		var code string
		json.Unmarshal(env.Error, &code) // a string, or null
		return &APIError{Status: resp.StatusCode, Code: code, Detail: env.Detail}
	}
	if out == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("TorBox: reading its answer: %w", err)
	}
	return nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

// Created is what adding a torrent gives back. TorrentID is 0 when
// TorBox queued it instead (every active slot of the plan in use); it
// appears in the list, under the same Hash, once a slot frees.
type Created struct {
	TorrentID int64  `json:"torrent_id"`
	QueuedID  int64  `json:"queued_id"`
	Hash      string `json:"hash"`
}

// Add adds a magnet link or a local .torrent file to the account. A
// torrent already there (or cached) comes back with its existing ID.
func (c *Client) Add(ctx context.Context, source string) (Created, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if strings.HasPrefix(source, "magnet:") {
		mw.WriteField("magnet", source)
	} else {
		data, err := os.ReadFile(source)
		if err != nil {
			return Created{}, err
		}
		fw, err := mw.CreateFormFile("file", filepath.Base(source))
		if err != nil {
			return Created{}, err
		}
		fw.Write(data)
	}
	// seed=3: don't seed on godl's behalf (TorBox's "1 auto, 2 seed,
	// 3 don't"); allow_zip=false: keep every file individually linkable.
	mw.WriteField("seed", "3")
	mw.WriteField("allow_zip", "false")
	if err := mw.Close(); err != nil {
		return Created{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/torrents/createtorrent", &body)
	if err != nil {
		return Created{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	var out Created
	err = c.do(req, &out)
	return out, err
}

// Torrent reads one torrent's current state, bypassing TorBox's own
// short cache so progress is live.
func (c *Client) Torrent(ctx context.Context, id int64) (*Torrent, error) {
	var t Torrent
	q := url.Values{"id": {strconv.FormatInt(id, 10)}, "bypass_cache": {"true"}}
	if err := c.get(ctx, "/torrents/mylist", q, &t); err != nil {
		return nil, err
	}
	if t.ID == 0 {
		return nil, &APIError{Status: http.StatusNotFound, Detail: "that torrent is no longer in your TorBox account"}
	}
	return &t, nil
}

// FindByHash looks a torrent up by info hash in the account's list —
// how a queued torrent is found once it starts.
func (c *Client) FindByHash(ctx context.Context, hash string) (*Torrent, error) {
	var list []Torrent
	if err := c.get(ctx, "/torrents/mylist", url.Values{"bypass_cache": {"true"}}, &list); err != nil {
		return nil, err
	}
	for i := range list {
		if strings.EqualFold(list[i].Hash, hash) {
			return &list[i], nil
		}
	}
	return nil, nil
}

// Link returns a direct HTTPS link to one file, good for a few hours
// and for ranged requests. Asked for once per file and then used for
// every chunk: TorBox's redirect form would cost an API call per
// request and trip its rate limit.
func (c *Client) Link(ctx context.Context, torrentID, fileID int64) (string, error) {
	var link string
	q := url.Values{
		"token":      {c.key},
		"torrent_id": {strconv.FormatInt(torrentID, 10)},
		"file_id":    {strconv.FormatInt(fileID, 10)},
	}
	if err := c.get(ctx, "/torrents/requestdl", q, &link); err != nil {
		return "", err
	}
	if !strings.HasPrefix(link, "https://") && !strings.HasPrefix(link, "http://") {
		return "", fmt.Errorf("TorBox: no download link for that file")
	}
	return link, nil
}

// Delete removes a torrent (and its files) from the account.
func (c *Client) Delete(ctx context.Context, id int64) error {
	payload, _ := json.Marshal(map[string]any{"torrent_id": id, "operation": "delete"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/torrents/controltorrent", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, nil)
}

// Cached reports which of hashes TorBox already has, so adding them is
// instant.
func (c *Client) Cached(ctx context.Context, hashes ...string) (map[string]bool, error) {
	var data map[string]json.RawMessage
	q := url.Values{"hash": {strings.Join(hashes, ",")}, "format": {"object"}}
	if err := c.get(ctx, "/torrents/checkcached", q, &data); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(hashes))
	for _, h := range hashes {
		_, ok := data[strings.ToLower(h)]
		if !ok {
			_, ok = data[h]
		}
		out[strings.ToLower(h)] = ok
	}
	return out, nil
}

// User is the account a key belongs to.
type User struct {
	Email string `json:"email"`
	Plan  int    `json:"plan"`
}

// Me checks the key and says whose it is.
func (c *Client) Me(ctx context.Context) (*User, error) {
	var u User
	if err := c.get(ctx, "/user/me", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}
