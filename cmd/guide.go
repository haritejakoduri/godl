package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"godl/internal/guide"
)

var guideCmd = &cobra.Command{
	Use:   "guide",
	Short: "Open the illustrated user guide in your browser",
	Long: `Open godl's user guide — what it can do and the commands for each
task, on one page — in your browser. The guide is built into godl, so
this works offline.

  godl guide                  # open it
  godl guide -o guide.html    # save it as a file instead

The page is served from this machine only (127.0.0.1) for as long as
the command runs, rather than opened as a file: sandboxed browsers
(Snap, Flatpak) often can't read files outside a few folders. Press
Ctrl+C once the page has loaded.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if output, _ := cmd.Flags().GetString("output"); output != "" {
			if err := os.WriteFile(output, guide.HTML, 0o644); err != nil {
				return err
			}
			fmt.Printf("Saved the guide to %s\n", output)
			return nil
		}

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		url := "http://" + ln.Addr().String() + "/"
		srv := &http.Server{Handler: guideHandler()}
		serveErr := make(chan error, 1)
		go func() { serveErr <- srv.Serve(ln) }()

		fmt.Printf("godl guide: %s\n", url)
		if err := guide.OpenInBrowser(url); err != nil {
			fmt.Println("Couldn't open a browser by itself — open that address in one.")
		}
		fmt.Println("Press Ctrl+C when you're done.")

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		select {
		case <-ctx.Done():
			return srv.Close()
		case err := <-serveErr:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		}
	},
}

// guideHandler serves the embedded guide at / and nothing else.
func guideHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(guide.HTML)
	})
}

func init() {
	guideCmd.Flags().StringP("output", "o", "", "save the guide to this file instead of opening it")
}
