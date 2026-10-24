package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"trollvnc-wall/internal/api"
	"trollvnc-wall/internal/auth"
	"trollvnc-wall/internal/config"
	"trollvnc-wall/internal/ctrl"
	"trollvnc-wall/internal/device"
	"trollvnc-wall/internal/gop"
	"trollvnc-wall/internal/hdlease"
	"trollvnc-wall/internal/streamsync"
	"trollvnc-wall/internal/tsgateway"
)

func main() {
	cfgPath := flag.String("config", "../config.yaml", "config path")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		// try repo root relative to cwd
		alt := filepath.Join("..", "config.yaml")
		cfg, err = config.Load(alt)
		if err != nil {
			log.Fatalf("config: %v", err)
		}
		*cfgPath = alt
	}
	log.Printf("config loaded: %s", *cfgPath)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	snap, err := auth.ProbeAndFreeze(cfg)
	if err != nil {
		log.Fatalf("startup snapshot: %v", err)
	}

	dm := device.NewManager(cfg.PlayBaseURL())
	gt := gop.New(cfg.Encode.GOPSeconds, cfg.Encode.MinGOP)
	qp := api.Quality{Cfg: cfg}

	ctrlSrv := ctrl.NewServer(cfg, dm, qp)
	hd := hdlease.New(
		cfg.HD.MaxHD,
		time.Duration(cfg.HD.QueueTimeoutMs)*time.Millisecond,
		dm, ctrlSrv,
	)
	hub := api.NewHub(cfg, dm, hd, ctrlSrv, snap)
	ts := tsgateway.New(cfg, dm, gt)
	ts.SetIDRHook(hub.OnIDREmitted)

	dm.SetHooks(func(id string) {
		hd.OnCtrlLost(id)
		ts.Cancel(id)
	}, ts.Cancel)

	if err := ctrlSrv.Start(ctx); err != nil {
		log.Fatalf("ctrl: %v", err)
	}
	log.Printf("ctrl listen %s", cfg.Server.CtrlListen)

	streamsync.New(cfg, dm).Start(ctx)
	go switchTimeoutLoop(ctx, cfg, dm, ctrlSrv, hd)

	authMux := http.NewServeMux()
	auth.NewHandler(cfg, dm, snap).Mount(authMux)
	go func() {
		log.Printf("auth listen %s", cfg.Server.AuthListen)
		if err := http.ListenAndServe(cfg.Server.AuthListen, authMux); err != nil {
			log.Printf("auth server: %v", err)
		}
	}()

	tsMux := http.NewServeMux()
	ts.Mount(tsMux)
	go func() {
		log.Printf("ts listen %s  playBase=%s", cfg.Server.TSListen, cfg.PlayBaseURL())
		if err := http.ListenAndServe(cfg.Server.TSListen, tsMux); err != nil {
			log.Printf("ts server: %v", err)
		}
	}()

	apiMux := http.NewServeMux()
	hub.Mount(apiMux)
	apiMux.Handle("/", http.FileServer(http.Dir(frontendDir())))
	go func() {
		log.Printf("api+web listen %s", cfg.Server.APIListen)
		if err := http.ListenAndServe(cfg.Server.APIListen, apiMux); err != nil {
			log.Printf("api server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("shutting down")
}

func frontendDir() string {
	candidates := []string{
		filepath.Join("..", "frontend"),
		"frontend",
		filepath.Join("..", "..", "frontend"),
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return "../frontend"
}

func switchTimeoutLoop(ctx context.Context, cfg *config.Config, dm *device.Manager, c *ctrl.Server, hd *hdlease.Manager) {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	to := time.Duration(cfg.Ctrl.SwitchTimeoutMs) * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			dm.Range(func(d *device.Device) bool {
				id, sw, seq, since := d.SwitchTimeoutInfo()
				if sw && time.Since(since) > to {
					log.Printf("switch timeout %s seq=%d → preview", id, seq)
					_ = dm.CompleteSwitch(id, seq, false, "preview", 0, 0)
					c.ClearRetransmit(id)
					hd.Release(id)
					_ = c.SendSetQuality(id, device.NextSwitchSeq(), "preview")
					hd.ReevaluateOnce()
				}
				return true
			})
		}
	}
}
