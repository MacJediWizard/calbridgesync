package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/macjediwizard/calbridgesync/internal/caldavproxy"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	listen := envOr("CALDAV_PROXY_LISTEN", ":8080")
	backendStr := envOr("CALDAV_PROXY_BACKEND", "https://mail.macjediwizard.com")
	skipVerify := envOr("CALDAV_PROXY_TLS_SKIP_VERIFY", "false") == "true"

	backendURL, err := url.Parse(backendStr)
	if err != nil {
		log.Fatalf("Invalid backend URL %q: %v", backendStr, err)
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: skipVerify},
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = backendURL.Scheme
			req.URL.Host = backendURL.Host
			req.Host = backendURL.Host
		},
		Transport: transport,
		ModifyResponse: func(resp *http.Response) error {
			if resp.Request.Method != "PROPFIND" || resp.StatusCode != http.StatusMultiStatus {
				return nil
			}

			body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
			resp.Body.Close()
			if err != nil {
				return err
			}

			cleaned := caldavproxy.RewriteMultistatus(body)

			resp.Body = io.NopCloser(bytes.NewReader(cleaned))
			resp.ContentLength = int64(len(cleaned))
			resp.Header.Set("Content-Length", strconv.Itoa(len(cleaned)))
			resp.Header.Del("Transfer-Encoding")

			return nil
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.Handle("/", proxy)

	server := &http.Server{
		Addr:         listen,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("CalDAV proxy starting on %s → %s", listen, backendStr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
