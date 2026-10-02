// Command dvbhub is a TV tuner server for Jellyfin and Plex: it scans ATSC,
// QAM, DVB-T/T2, DVB-C and DVB-S/S2 tuners, maps channels, collects the
// programme guide and serves live streams through HDHomeRun emulation and
// M3U/XMLTV, passed through or transcoded on a GPU or the CPU.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"dvbhub/internal/config"
	"dvbhub/internal/guide"
	"dvbhub/internal/hardware"
	"dvbhub/internal/scan"
	"dvbhub/internal/server"
	"dvbhub/internal/transcode"
	"dvbhub/internal/tuners"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	home, _ := os.UserHomeDir()
	dataDir := flag.String("data", filepath.Join(home, ".dvbhub"), "folder for settings, channels and the guide")
	listen := flag.String("listen", ":9980", "HTTP address to listen on")
	password := flag.String("password", os.Getenv("DVBHUB_PASSWORD"), "password for the web UI and API (streams and lineups stay open for Jellyfin)")
	virtual := flag.Int("virtual", -1, "number of test tuners that play files (overrides the setting)")
	detect := flag.Bool("detect", false, "print a tuner hardware, driver and firmware report as JSON and exit")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if abs, err := filepath.Abs(*dataDir); err == nil {
		*dataDir = abs
	}
	if *showVersion {
		fmt.Println("dvbhub", version)
		return
	}
	if *detect {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(hardware.NewDetector(*dataDir).Report(true))
		return
	}

	logs := server.NewLogBuffer()
	log.SetOutput(io.MultiWriter(os.Stderr, logs))
	log.SetFlags(log.Ldate | log.Ltime)
	log.Printf("dvbhub %s starting (data in %s)", version, *dataDir)

	st, err := config.Open(*dataDir)
	if err != nil {
		log.Fatal(err)
	}
	if *virtual >= 0 {
		st.Update(func(s *config.State) error { s.Settings.VirtualTuners = *virtual; return nil })
	}
	log.Printf("startup: looking for tuners in the background")
	tm := tuners.NewManager(st)
	sc := scan.New(st, tm)
	g := guide.Open(*dataDir)
	xmltv := guide.NewImporter(st, g)
	grabber := guide.NewGrabber(st, tm, g)
	encoders := transcode.NewDetector()

	// Drop guide data of deleted channels.
	st.OnChange(func() {
		valid := map[string]bool{}
		st.View(func(s *config.State) {
			for id := range s.Channels {
				valid[id] = true
			}
		})
		g.Retain(valid)
	})

	var ffmpeg string
	var discovery bool
	st.View(func(s *config.State) { ffmpeg, discovery = s.Settings.FFmpeg, s.Settings.SSDP })
	go func() {
		time.Sleep(3 * time.Second)
		encoders.Probe(ffmpeg)
	}()

	srv := &http.Server{
		Handler: server.New(server.Deps{Store: st, Tuners: tm, Scanner: sc, Guide: g, XMLTV: xmltv, Grabber: grabber,
			Hardware: hardware.NewDetector(*dataDir), Encoders: encoders, Logs: logs, Password: *password, Version: version}),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: streams are long-lived.
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if discovery {
		server.RunDiscovery(st, tm.TunerCount, port)
	}
	log.Printf("dvbhub ready on port %d: web UI and Jellyfin tuner at http://<this computer>:%d", port, port)

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Printf("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	st.Save()
}
