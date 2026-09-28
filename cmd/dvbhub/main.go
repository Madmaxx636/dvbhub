// Command dvbhub is a DVB tuner server: it scans DVB-T/T2/C/S/S2 hardware,
// maps services to channels, collects EPG (EIT and XMLTV) and serves live
// streams to Jellyfin/Plex via HDHomeRun emulation and M3U/XMLTV, with
// optional NVIDIA (NVENC) transcoding.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"dvbhub/internal/epg"
	"dvbhub/internal/hw"
	"dvbhub/internal/scan"
	"dvbhub/internal/store"
	"dvbhub/internal/tuner"
	"dvbhub/internal/web"
)

func main() {
	home, _ := os.UserHomeDir()
	dataDir := flag.String("data", home+"/.dvbhub", "data directory for config, EPG and state")
	listen := flag.String("listen", ":9980", "HTTP listen address")
	password := flag.String("password", os.Getenv("DVBHUB_PASSWORD"), "admin UI/API password (streams and lineups stay open)")
	virtual := flag.Int("virtual", -1, "number of virtual file tuners (overrides setting; for testing)")
	detect := flag.Bool("detect", false, "print a tuner hardware / driver / firmware report as JSON and exit")
	flag.Parse()
	log.SetFlags(log.Ldate | log.Ltime)

	if *detect {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(hw.NewDetector(*dataDir).Report(true))
		return
	}

	log.Printf("dvbhub starting (data in %s)", *dataDir)
	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatal(err)
	}
	if *virtual >= 0 {
		st.Update(func(s *store.State) error { s.Settings.VirtualTuners = *virtual; return nil })
	}
	log.Printf("startup: looking for tuners")
	tm := tuner.NewManager(st)
	log.Printf("startup: %d tuner(s) ready; loading scanner and guide", tm.TunerCount())
	sc := scan.New(st, tm)
	guide := epg.Open(*dataDir)
	im := epg.NewImporter(st, guide)
	eit := epg.NewEITGrabber(st, tm, guide)

	// Drop guide data for deleted channels.
	st.OnChange(func() {
		valid := map[string]bool{}
		st.View(func(s *store.State) {
			for id := range s.Channels {
				valid[id] = true
			}
		})
		guide.Retain(valid)
	})

	srv := &http.Server{
		Addr:              *listen,
		Handler:           web.New(st, tm, sc, guide, im, eit, *password),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: streams are long-lived.
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	var ssdp bool
	st.View(func(s *store.State) { ssdp = s.Settings.SSDP })
	if ssdp {
		go web.RunSSDP(st, port)
	}
	log.Printf("dvbhub listening on %s (data in %s)", ln.Addr(), *dataDir)
	log.Printf("  admin UI:      http://<host>:%s/", strconv.Itoa(port))
	log.Printf("  HDHomeRun:     http://<host>:%d  (add as HDHomeRun tuner in Jellyfin/Plex)", port)
	log.Printf("  M3U / XMLTV:   http://<host>:%d/playlist.m3u  http://<host>:%d/xmltv.xml", port, port)

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
