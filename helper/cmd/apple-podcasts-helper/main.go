package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/apple"
	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/auth"
	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/browser"
	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/lifetime"
	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/server"
	"github.com/AceYKN/mygo-music-apple-podcasts/helper/internal/syncstate"
)

func main() {
	data := flag.String("data-dir", "", "Plugin private data directory")
	chrome := flag.String("chrome", browser.ChromePath(), "Google Chrome executable")
	mute := flag.Bool("mute", false, "Silence all browser audio during verification")
	flag.Parse()
	if err := lifetime.Guard(); err != nil {
		fmt.Fprintln(os.Stderr, "Cannot initialize child-process cleanup")
		os.Exit(1)
	}
	if *data == "" {
		fmt.Fprintln(os.Stderr, "--data-dir is required")
		os.Exit(2)
	}
	if err := os.MkdirAll(*data, 0700); err != nil {
		fmt.Fprintln(os.Stderr, "Cannot create private data directory")
		os.Exit(1)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		os.Exit(1)
	}
	token := hex.EncodeToString(secret)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Cannot open loopback listener")
		os.Exit(1)
	}
	_ = mute // Compatibility with the previous verification launcher; login is always muted.
	profile := filepath.Join(*data, "browser-profile")
	_, legacyErr := os.Stat(filepath.Join(profile, "Default", "Network", "Cookies"))
	public := auth.NewPublicDeveloper()
	manager := &auth.Manager{Vault: auth.FileVault{Path: filepath.Join(*data, "session.bin")}, Migrate: legacyErr == nil, Developer: public.Get, Browser: func(interactive bool) auth.LoginBrowser { return browser.NewAuth(profile, *chrome, interactive) }}
	defer manager.Close()
	client := &apple.Client{Session: manager.Session, Clock: &apple.RemoteClock{}, OnResponse: manager.AcceptResponse, HTTP: &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	rpc := &server.Server{Token: token, Auth: manager, Apple: client, Region: apple.NewRegion(), Store: syncstate.New(filepath.Join(*data, "sync"))}
	httpServer := &http.Server{Handler: rpc, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 3 * time.Minute, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		scan := bufio.NewScanner(os.Stdin)
		for scan.Scan() {
		}
		stop()
	}()
	if err = json.NewEncoder(os.Stdout).Encode(map[string]any{"protocol": 1, "url": "http://" + listener.Addr().String(), "token": token}); err != nil {
		os.Exit(1)
	}
	go func() {
		if e := httpServer.Serve(listener); e != nil && e != http.ErrServerClosed {
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdown)
	rpc.FlushProgress(7 * time.Second)
}
