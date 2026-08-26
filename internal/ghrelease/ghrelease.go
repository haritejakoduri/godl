// Package ghrelease verifies files godl auto-downloads from GitHub
// Releases (yt-dlp, ffmpeg) against the SHA-256 digest GitHub itself
// computed for that asset at upload time, fetched over a separate HTTPS
// call to the GitHub API rather than trusted from the download response
// itself. That catches network corruption, a compromised/misconfigured
// CDN edge, or a MITM tampering with the asset bytes in transit — the
// same class of protection package managers give you via checksums.
//
// It does not protect against a compromised upstream account publishing
// a malicious release in the first place (GPG-signature verification
// against a pinned key would be needed for that); see the callers for
// how they treat a verification failure or an unreachable API.
package ghrelease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// AssetDigest fetches the sha256 digest GitHub computed for assetName in
// the named release. releasesURL is the full GitHub API URL for a single
// release object, e.g. "https://api.github.com/repos/OWNER/REPO/releases/latest".
func AssetDigest(ctx context.Context, releasesURL, assetName string) (sha256Hex string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching release metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching release metadata: unexpected status: %s", resp.Status)
	}

	var release struct {
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("parsing release metadata: %w", err)
	}

	for _, a := range release.Assets {
		if a.Name != assetName {
			continue
		}
		hex, ok := strings.CutPrefix(a.Digest, "sha256:")
		if !ok || len(hex) != 64 {
			return "", fmt.Errorf("release metadata for %s has no sha256 digest", assetName)
		}
		return hex, nil
	}
	return "", fmt.Errorf("asset %s not found in release metadata", assetName)
}

// TagName fetches the git tag name of the release at releasesURL (e.g.
// "v0.3.0" for godl's own releases, which internal/version.Version
// compares against with the "v" trimmed off). Used by internal/selfupdate
// to decide whether a newer godl release exists at all, separately from
// AssetDigest's per-asset checksum lookup.
func TagName(ctx context.Context, releasesURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching release metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching release metadata: unexpected status: %s", resp.Status)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("parsing release metadata: %w", err)
	}
	if release.TagName == "" {
		return "", fmt.Errorf("release metadata has no tag_name")
	}
	return release.TagName, nil
}

// HashingCopy copies from r to w (as io.Copy does) while computing the
// sha256 of everything read, so a caller can verify a download's
// integrity without a second pass over the data.
func HashingCopy(w io.Writer, r io.Reader) (sha256Hex string, n int64, err error) {
	h := sha256.New()
	n, err = io.Copy(io.MultiWriter(w, h), r)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// Verify reports whether gotHex matches wantHex (case-insensitive).
func Verify(gotHex, wantHex string) error {
	if !strings.EqualFold(gotHex, wantHex) {
		return fmt.Errorf("sha256 mismatch: downloaded file doesn't match GitHub's published digest (got %s, want %s) — refusing to use it", gotHex, wantHex)
	}
	return nil
}

// HashFile computes the sha256 of a local file — used to compare an
// already-installed binary against a release's published digest
// without needing to separately persist what was installed.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
