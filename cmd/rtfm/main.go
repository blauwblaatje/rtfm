// Command rtfm runs RTFM, the Roller derby Tournament Fixture Maker: a
// website that turns a WFTDA sanctioning application (and an infopack) into
// pre-game statsbooks and CRG scoreboard game files.
package main

import (
	"cmp"
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

	"rtfm/internal/web"
)

// version is set by the build.
var version = "dev"

func main() {
	addr := flag.String("addr", cmp.Or(os.Getenv("RTFM_ADDR"), ":8080"), "address to listen on (RTFM_ADDR)")
	blank := flag.String("blank", os.Getenv("RTFM_BLANK"), "folder with WFTDA's blank statsbooks, .xlsx (RTFM_BLANK)")
	data := flag.String("data", os.Getenv("RTFM_DATA"), "folder to keep loaded tournaments in; empty: memory only (RTFM_DATA)")
	keep := flag.Duration("keep", envDuration("RTFM_KEEP", 60*24*time.Hour), "how long a loaded tournament is kept (RTFM_KEEP)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	health := flag.Bool("health", false, "check that the rtfm on -addr answers, exit 0 if so (the container's health check)")
	flag.Parse()
	if *showVersion {
		fmt.Println("rtfm", version)
		return
	}
	if *health {
		host, port, _ := net.SplitHostPort(*addr)
		c := &http.Client{Timeout: 3 * time.Second}
		resp, err := c.Get("http://" + net.JoinHostPort(cmp.Or(host, "127.0.0.1"), port) + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	site, err := web.New(web.Config{Blank: *blank, Data: *data, Keep: *keep})
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: *addr, Handler: site.Handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 2 * time.Minute, WriteTimeout: 5 * time.Minute}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				site.Clean()
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	log.Printf("rtfm %s: listening on %s, blank statsbooks in %q, data in %q", version, *addr, *blank, *data)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func envDuration(name string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(name)); err == nil {
		return d
	}
	return def
}
