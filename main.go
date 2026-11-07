// static-server serves a directory of static files: a single-page app, a prebuilt web UI, or a
// directory another container writes to. One configurable binary with no dependencies beyond the
// Go standard library, so the distroless image it ships in carries nothing else for a scanner to
// flag. See README.md for the configuration file.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	configPath := flag.String("config", "", "configuration file (default $STATIC_SERVER_CONFIG, else "+defaultConfigPath+" when it exists)")
	healthcheck := flag.Bool("healthcheck", false, "request / on the configured address and exit 0 if the server answers; for a container HEALTHCHECK, since the image has no shell or curl")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	explicit := true
	if *configPath == "" {
		*configPath = os.Getenv("STATIC_SERVER_CONFIG")
	}
	if *configPath == "" {
		*configPath, explicit = defaultConfigPath, false
	}
	cfg, err := loadConfig(*configPath, explicit)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if *healthcheck {
		if err := check(cfg.Listen); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	s, err := newServer(cfg)
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Printf("static-server %s: serving %s on %s (%d mounts, %d rules)", version, cfg.Root, cfg.Listen, len(cfg.Mounts), len(cfg.Rules))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func check(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Head("http://" + net.JoinHostPort(host, port) + "/")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("healthcheck: %s", resp.Status)
	}
	return nil
}
