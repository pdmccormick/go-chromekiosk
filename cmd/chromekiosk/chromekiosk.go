package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"

	"go.pdmccormick.com/chromekiosk"
)

func main() {
	var (
		ctx        = context.Background()
		httpFlag   = flag.String("http", ":8080", "run HTTP server on `address:port`")
		mountFlag  = flag.String("mount", "", "`path` to already mounted container image")
		imageFlag  = flag.String("image", "", "`path` to container squashfs image")
		rundirFlag = flag.String("rundir", "/run/chromekiosk", "`path` to rundir")
		urlFlag    = flag.String("url", "blank:yellow", "starting url")
		debugFlag  = flag.String("remotedebug", "127.0.0.1:9222", "`addr:port` for Chrome Remote Debugger")
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	var m = chromekiosk.Monitor{
		StartUrl:   *urlFlag,
		ImagePath:  *imageFlag,
		MountPoint: *mountFlag,
		RunDir:     *rundirFlag,
	}

	if err := m.Init(); err != nil {
		log.Fatalf("Init: %s", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("INDEX: %+v", r)

		fmt.Fprintf(w, "<pre>\n\n")
	})

	mux.HandleFunc("/console", func(w http.ResponseWriter, r *http.Request) {
		qs := r.URL.Query()
		if text := qs.Get("text"); text != "" {
			var text = struct{ Text string }{text}
			out, _ := json.Marshal(text)
			code := fmt.Sprintf("(function() { console.log(%s.Text); })();", string(out))
			if _, err := m.Browser.EvalJS(code); err != nil {
				log.Printf("EvalJS: %s", err)
			}
		} else {
			fmt.Fprintf(w, "missing ?text= param")
		}
	})

	mux.HandleFunc("/navigate", func(w http.ResponseWriter, r *http.Request) {
		qs := r.URL.Query()
		if urlStr := qs.Get("url"); urlStr != "" {
			if urlStr == "-" || urlStr == "about:blank" {
				urlStr = chromekiosk.DefaultStartUrl
			}
			if err := m.Browser.Navigate(urlStr); err != nil {
				log.Printf("Navigate `%s`: %s", urlStr, err)
			}
		} else {
			fmt.Fprintf(w, "missing ?url= param")
		}
	})

	mux.HandleFunc("/quit", func(w http.ResponseWriter, r *http.Request) {
		qs := r.URL.Query()
		if now := qs.Get("now"); now == "1" {
			cancel()
		} else {
			fmt.Fprintf(w, "missing ?now=1 param")
		}
	})

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()

		if err := m.Run(ctx); err != nil {
			log.Fatalf("Run: %s", err)
		}
	}()

	go func() {
		log.Fatal(http.ListenAndServe(*httpFlag, mux))
	}()

	if addr := *debugFlag; addr != "" {
		go func() {
			if err := m.ListenRemoteDebugger(addr); err != nil {
				log.Printf("ListenRemoteDebugger: %s", err)
			}
		}()
	}

RunLoop:
	for {
		select {
		case <-ctx.Done():
			stop()
			break RunLoop
		}
	}

	wg.Wait()
}
