package cmd

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/store"
)

var urlCmd = &cobra.Command{
	Use:   "url <link>",
	Short: "Download a direct HTTP(S) link, resumable and concurrently chunked",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		link := args[0]
		output, _ := cmd.Flags().GetString("output")
		concurrency, _ := cmd.Flags().GetInt("concurrency")

		if output == "" {
			output = filenameFromURL(link)
		}
		abs, err := resolveOutputPath(output)
		if err != nil {
			return err
		}
		output = abs

		if err := daemon.EnsureRunning(); err != nil {
			return err
		}
		resp, err := daemon.Call(daemon.Request{
			Cmd:         daemon.CmdAddURL,
			Source:      link,
			Output:      output,
			Concurrency: concurrency,
		})
		if err != nil {
			return err
		}
		if resp.Job.Status == store.StatusFailed {
			return fmt.Errorf("job %s failed immediately: %s", resp.Job.ID, resp.Job.ErrorMsg)
		}
		fmt.Printf("Started job %s -> %s\n", resp.Job.ID, output)
		fmt.Println(`Track it with "godl status" or "godl list".`)
		return nil
	},
}

// filenameFromURL picks a destination filename when the user didn't pass
// -o. The URL path is tried first; if that doesn't yield anything with a
// usable extension (many links are just an opaque ID or token, e.g. a CDN
// serving /dld/<uuid>?token=...), it asks the server via a HEAD request
// and reads Content-Disposition / Content-Type instead of silently saving
// as a bare, extension-less UUID.
func filenameFromURL(link string) string {
	base := "download"
	if u, err := url.Parse(link); err == nil {
		if b := filepath.Base(u.Path); b != "" && b != "." && b != "/" {
			base = b
		}
	}
	if filepath.Ext(base) != "" {
		return base
	}

	name, ext, ok := probeFilename(link)
	if !ok {
		return base
	}
	if name != "" {
		base = name
	}
	if ext != "" && filepath.Ext(base) == "" {
		base += ext
	}
	return base
}

// probeFilename asks the server for a filename hint via HEAD (falling back
// to a ranged GET for servers that reject HEAD). ok reports whether the
// request succeeded at all, so callers can fall back to the URL-derived
// name on network errors instead of failing the whole command.
//
// If neither Content-Disposition nor Content-Type yields a usable
// extension — common with CDNs that respond with a generic or missing
// Content-Type — a last-resort magic-byte sniff of the first bytes of
// the body is tried before giving up, rather than saving the file with
// no extension at all.
func probeFilename(link string) (name, ext string, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := &http.Client{}
	resp, err := doProbeRequest(ctx, client, http.MethodHead, link)
	usedGet := false
	if err != nil || resp.StatusCode >= 400 {
		if resp != nil {
			resp.Body.Close()
		}
		resp, err = doProbeRequest(ctx, client, http.MethodGet, link)
		usedGet = true
	}
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", false
	}

	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		name = filenameFromContentDisposition(cd)
	}
	if name == "" && resp.Request != nil && resp.Request.URL != nil {
		if b := filepath.Base(resp.Request.URL.Path); b != "" && b != "." && b != "/" {
			name = b
		}
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		ext = extByContentType(ct)
	}

	if ext == "" {
		if usedGet {
			ext = sniffExt(resp.Body)
		} else if sniffResp, serr := doProbeRequest(ctx, client, http.MethodGet, link); serr == nil {
			defer sniffResp.Body.Close()
			if sniffResp.StatusCode < 400 {
				ext = sniffExt(sniffResp.Body)
			}
		}
	}
	return name, ext, true
}

func doProbeRequest(ctx context.Context, client *http.Client, method, link string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, link, nil)
	if err != nil {
		return nil, err
	}
	if method == http.MethodGet {
		// 512 bytes is enough for http.DetectContentType's magic-byte
		// sniffing (see sniffExt) while still asking for far less than
		// the whole file just to read headers/a content sample.
		req.Header.Set("Range", "bytes=0-511")
	}
	return client.Do(req)
}

// sniffExt reads a small prefix of body and magic-byte-sniffs its
// actual type, for servers whose Content-Type is missing or generic
// (e.g. application/octet-stream) — the last resort before a download
// ends up with no file extension at all.
func sniffExt(body io.Reader) string {
	buf := make([]byte, 512)
	n, _ := io.ReadFull(body, buf)
	if n == 0 {
		return ""
	}
	return extByContentType(http.DetectContentType(buf[:n]))
}

func filenameFromContentDisposition(cd string) string {
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		return ""
	}
	if v, ok := params["filename*"]; ok {
		if n := sanitizeFilename(decodeRFC5987(v)); n != "" {
			return n
		}
	}
	if v, ok := params["filename"]; ok {
		return sanitizeFilename(v)
	}
	return ""
}

// decodeRFC5987 decodes the extended-notation form of filename*, e.g.
// "UTF-8”some%20file.mp4" -> "some file.mp4".
func decodeRFC5987(v string) string {
	raw := v
	if _, rest, found := strings.Cut(v, "''"); found {
		raw = rest
	}
	if decoded, err := url.QueryUnescape(raw); err == nil {
		return decoded
	}
	return raw
}

// sanitizeFilename strips any directory components a (potentially
// malicious) server-supplied name might carry, keeping just the base name.
func sanitizeFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

// commonExtByType covers the content types actually likely to show up on
// links people paste into godl. mime.ExtensionsByType is the fallback for
// anything else, but its answers for a type like image/jpeg aren't always
// the conventional extension (e.g. ".jpe" before ".jpg").
var commonExtByType = map[string]string{
	"video/mp4":                    ".mp4",
	"video/webm":                   ".webm",
	"video/x-matroska":             ".mkv",
	"video/quicktime":              ".mov",
	"video/x-msvideo":              ".avi",
	"audio/mpeg":                   ".mp3",
	"audio/mp4":                    ".m4a",
	"audio/ogg":                    ".ogg",
	"audio/wav":                    ".wav",
	"audio/x-wav":                  ".wav",
	"audio/flac":                   ".flac",
	"image/jpeg":                   ".jpg",
	"image/png":                    ".png",
	"image/gif":                    ".gif",
	"image/webp":                   ".webp",
	"application/zip":              ".zip",
	"application/x-7z-compressed":  ".7z",
	"application/x-rar-compressed": ".rar",
	"application/x-tar":            ".tar",
	"application/gzip":             ".gz",
	"application/x-gzip":           ".gz",
	"application/pdf":              ".pdf",
	"application/json":             ".json",
	"text/plain":                   ".txt",
	"text/html":                    ".html",
	"application/octet-stream":     "",
	// The rest are net/http's http.DetectContentType sniff outputs (used
	// by sniffExt) rather than real Content-Type header values, plus a
	// couple of real-world header values seen from CDNs/streaming
	// endpoints that aren't in Go's builtin mime table.
	"audio/wave":                    ".wav",
	"audio/aiff":                    ".aiff",
	"audio/midi":                    ".mid",
	"application/ogg":               ".ogg",
	"video/avi":                     ".avi",
	"image/bmp":                     ".bmp",
	"font/woff":                     ".woff",
	"font/woff2":                    ".woff2",
	"application/wasm":              ".wasm",
	"video/mp2t":                    ".ts",
	"audio/x-m4a":                   ".m4a",
	"application/x-mpegurl":         ".m3u8",
	"application/vnd.apple.mpegurl": ".m3u8",
}

func extByContentType(contentType string) string {
	ct, _, _ := strings.Cut(contentType, ";")
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ext, ok := commonExtByType[ct]; ok {
		return ext
	}
	if exts, _ := mime.ExtensionsByType(ct); len(exts) > 0 {
		return exts[0]
	}
	return ""
}

func init() {
	urlCmd.Flags().StringP("output", "o", "", "output file path (default: derived from the URL, or from the server's Content-Disposition/Content-Type if the URL alone doesn't give us a usable name)")
	urlCmd.Flags().IntP("concurrency", "c", 4, "number of concurrent chunks (ignored if the server can't do ranges)")
}
