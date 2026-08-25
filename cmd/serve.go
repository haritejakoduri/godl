package cmd

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"godl/internal/fileserver"
)

var serveCmd = &cobra.Command{
	Use:   "serve <dir>",
	Short: "Serve a local directory over HTTP(S)/WebDAV for other devices to browse and download from",
	Long: `Serves a local directory over HTTP(S): a full WebDAV endpoint at
/dav/ (so Windows Explorer, macOS Finder, Linux's file manager, rclone,
or godl's own "godl connection add" + TUI browser can mount or browse
it — including bulk multi-file download the same way as any other
WebDAV connection), plus a plain browser page at / for anyone who'd
rather just click links, with a "download selected as .zip" button for
grabbing several files or folders at once without mounting anything.

Read-only by default — pass --allow-write to also accept uploads/
deletes over WebDAV. Refuses to start on a non-loopback address
without either --username/--password or an explicit --insecure-no-auth,
since anyone who can reach the address can otherwise download
everything under <dir>.

  godl serve ~/Public -p 8080 --username alice
  godl serve ~/Public --self-signed --username alice   # https://, untrusted cert
  godl serve ~/Public --host 127.0.0.1                 # this machine only, no auth needed`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := args[0]
		host, _ := cmd.Flags().GetString("host")
		port, _ := cmd.Flags().GetInt("port")
		username, _ := cmd.Flags().GetString("username")
		password, _ := cmd.Flags().GetString("password")
		allowWrite, _ := cmd.Flags().GetBool("allow-write")
		tlsCert, _ := cmd.Flags().GetString("tls-cert")
		tlsKey, _ := cmd.Flags().GetString("tls-key")
		selfSigned, _ := cmd.Flags().GetBool("self-signed")
		insecureNoAuth, _ := cmd.Flags().GetBool("insecure-no-auth")

		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}

		if password == "" {
			password = os.Getenv("GODL_SERVE_PASSWORD")
		}
		if password == "" && username != "" {
			p, err := promptPassword("Password for incoming connections: ")
			if err != nil {
				return fmt.Errorf("reading password: %w (pass --password or set GODL_SERVE_PASSWORD for non-interactive use)", err)
			}
			password = p
		}

		hasAuth := username != "" && password != ""
		if err := validateServeFlags(abs, host, hasAuth, insecureNoAuth, tlsCert, tlsKey, selfSigned); err != nil {
			return err
		}

		handler, err := fileserver.New(fileserver.Config{
			Root:     abs,
			Username: username,
			Password: password,
			ReadOnly: !allowWrite,
		})
		if err != nil {
			return err
		}

		addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
		srv := &http.Server{Addr: addr, Handler: handler}

		useTLS := tlsCert != "" || selfSigned
		if selfSigned {
			cert, err := fileserver.SelfSignedCert([]string{host})
			if err != nil {
				return fmt.Errorf("generating self-signed certificate: %w", err)
			}
			srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
		}

		printServeBanner(abs, addr, useTLS, selfSigned, allowWrite, username, hasAuth)

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		errCh := make(chan error, 1)
		go func() {
			if useTLS {
				// Empty cert/key filenames: when srv.TLSConfig already
				// carries a certificate (the --self-signed case), Go
				// uses that instead of trying to load files; the
				// --tls-cert/--tls-key case passes real filenames.
				errCh <- srv.ListenAndServeTLS(tlsCert, tlsKey)
			} else {
				errCh <- srv.ListenAndServe()
			}
		}()

		select {
		case err := <-errCh:
			if err != nil && err != http.ErrServerClosed {
				return err
			}
		case <-ctx.Done():
			fmt.Println("\nShutting down...")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return srv.Shutdown(shutdownCtx)
		}
		return nil
	},
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validateServeFlags checks the flag combinations that would otherwise
// only surface as a confusing runtime failure (or, worse for the
// no-auth case, silently succeed and expose dir to anyone who can
// reach host). Pulled out of RunE so it's testable without actually
// starting a listener.
func validateServeFlags(dir, host string, hasAuth, insecureNoAuth bool, tlsCert, tlsKey string, selfSigned bool) error {
	if !hasAuth && !isLoopbackHost(host) && !insecureNoAuth {
		return fmt.Errorf("refusing to serve %s on %s without authentication — anyone who can reach this address could download everything under it. Pass --username/--password, bind to --host 127.0.0.1 instead, or pass --insecure-no-auth to override", dir, host)
	}
	if tlsCert != "" && selfSigned {
		return fmt.Errorf("pass either --tls-cert/--tls-key or --self-signed, not both")
	}
	if (tlsCert == "") != (tlsKey == "") {
		return fmt.Errorf("--tls-cert and --tls-key must be set together")
	}
	return nil
}

func printServeBanner(dir, addr string, useTLS, selfSigned, allowWrite bool, username string, hasAuth bool) {
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	fmt.Printf("Serving %s\n", dir)
	fmt.Printf("  Browse:  %s://%s/\n", scheme, addr)
	fmt.Printf("  WebDAV:  %s://%s/dav/\n", scheme, addr)
	if allowWrite {
		fmt.Println("  Read-write — uploads and deletes over WebDAV are allowed.")
	} else {
		fmt.Println("  Read-only — pass --allow-write to enable uploads/deletes.")
	}
	if hasAuth {
		fmt.Printf("  Auth: username %q required.\n", username)
	} else {
		fmt.Println("  No authentication — anyone who can reach this address can download everything above.")
	}
	if selfSigned {
		fmt.Println("  Self-signed certificate — browsers and WebDAV clients will warn until you trust it.")
	}
	fmt.Println("Add it as a godl connection with:")
	if hasAuth {
		fmt.Printf("  godl connection add <name> --url %s://%s/dav/ --username %s\n", scheme, addr, username)
	} else {
		fmt.Printf("  godl connection add <name> --url %s://%s/dav/\n", scheme, addr)
	}
	fmt.Println("Ctrl+C to stop.")
}

func init() {
	serveCmd.Flags().String("host", "0.0.0.0", "address to bind to (use 127.0.0.1 to only allow this machine)")
	serveCmd.Flags().IntP("port", "p", 8080, "port to listen on")
	serveCmd.Flags().String("username", "", "require this username for HTTP Basic Auth")
	serveCmd.Flags().String("password", "", "password for --username (prompted if omitted; or set GODL_SERVE_PASSWORD)")
	serveCmd.Flags().Bool("allow-write", false, "allow uploads/deletes over WebDAV (default: read-only)")
	serveCmd.Flags().String("tls-cert", "", "TLS certificate file (use with --tls-key) for https://")
	serveCmd.Flags().String("tls-key", "", "TLS private key file (use with --tls-cert)")
	serveCmd.Flags().Bool("self-signed", false, "serve https:// with a generated, untrusted self-signed certificate")
	serveCmd.Flags().Bool("insecure-no-auth", false, "allow serving on a non-loopback address without authentication (anyone who can reach it can download everything)")
}
