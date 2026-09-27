package cmd

import (
	"path/filepath"

	"github.com/spf13/cobra"

	"godl/internal/reqhdr"
	"godl/internal/store"
)

// addRequestFlags registers the header/cookie flags shared by url and
// social.
func addRequestFlags(cmd *cobra.Command) {
	cmd.Flags().StringArrayP("header", "H", nil, `extra request header, e.g. -H "Authorization: Bearer abc" (repeatable)`)
	cmd.Flags().String("user-agent", "", "User-Agent to send instead of the default")
	cmd.Flags().String("referer", "", "Referer header to send (some hosts refuse downloads without one)")
	cmd.Flags().String("cookie", "", `cookies to send, e.g. --cookie "session=abc; theme=dark"`)
	cmd.Flags().String("cookies", "", "Netscape-format cookies.txt file (as exported by a browser extension); only each site's own cookies are sent to it")
}

// requestOptions turns the flags from addRequestFlags into job options,
// validating every header now rather than when the job starts.
func requestOptions(cmd *cobra.Command) (store.JobOptions, error) {
	var o store.JobOptions
	headers, _ := cmd.Flags().GetStringArray("header")
	if ua, _ := cmd.Flags().GetString("user-agent"); ua != "" {
		headers = append(headers, "User-Agent: "+ua)
	}
	if ref, _ := cmd.Flags().GetString("referer"); ref != "" {
		headers = append(headers, "Referer: "+ref)
	}
	if c, _ := cmd.Flags().GetString("cookie"); c != "" {
		headers = append(headers, "Cookie: "+c)
	}
	for _, h := range headers {
		if _, _, err := reqhdr.ParseHeader(h); err != nil {
			return o, err
		}
	}
	o.Headers = headers

	if f, _ := cmd.Flags().GetString("cookies"); f != "" {
		// Absolute, since the daemon that reads it has its own cwd;
		// parsed once here so a wrong path or format fails immediately.
		abs, err := filepath.Abs(f)
		if err != nil {
			return o, err
		}
		if _, err := reqhdr.Build(nil, abs); err != nil {
			return o, err
		}
		o.CookiesFile = abs
	}
	return o, nil
}

// requestHeaders builds the header set a CLI-side probe (the filename
// lookup) should send, matching what the job itself will send.
func requestHeaders(o store.JobOptions) (*reqhdr.Set, error) {
	return reqhdr.Build(o.Headers, o.CookiesFile)
}
