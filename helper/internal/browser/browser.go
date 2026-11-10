// Package browser is used only to obtain or repair an official login.
// Audio playback and listening-progress reporting belong to native MyGo.
package browser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Session struct {
	Authorized   bool   `json:"authorized"`
	Country      string `json:"country"`
	ID           string `json:"id"`
	Offline      bool   `json:"offline,omitempty"`
	RouteCountry string `json:"routeCountry,omitempty"`
	Developer    string `json:"-"`
	User         string `json:"-"`
}

type Browser struct {
	mu                  sync.Mutex
	ctx                 context.Context
	cancel              context.CancelFunc
	allocatorCancel     context.CancelFunc
	profile, executable string
	headless            bool
}

func NewAuth(profile, executable string, interactive bool) *Browser {
	return &Browser{profile: profile, executable: executable, headless: !interactive}
}
func ChromePath() string {
	for _, root := range []string{os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("LOCALAPPDATA")} {
		if root != "" {
			path := filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe")
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	}
	return "google-chrome"
}
func (b *Browser) startLocked() error {
	if b.ctx != nil && b.ctx.Err() == nil {
		return nil
	}
	if os.MkdirAll(b.profile, 0700) != nil {
		return errors.New("cannot open the persistent Apple Podcasts login profile")
	}
	// Keep normal Chrome credential storage. Do not use chromedp's broad
	// headless defaults, which change password storage for this saved profile.
	opts := []chromedp.ExecAllocatorOption{chromedp.ExecPath(b.executable), chromedp.UserDataDir(b.profile), chromedp.NoFirstRun, chromedp.NoDefaultBrowserCheck, chromedp.VisibleWindow, chromedp.WindowSize(1080, 780), chromedp.Flag("mute-audio", true)}
	if b.headless {
		opts = append(opts, chromedp.Flag("headless", "new"))
	}
	alloc, allocatorCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(alloc)
	b.ctx, b.cancel, b.allocatorCancel = ctx, cancel, allocatorCancel
	if err := chromedp.Do(ctx); err != nil {
		cancel()
		allocatorCancel()
		b.ctx = nil
		return errors.New("cannot start Chrome for login; select its executable in plugin settings")
	}
	timed, done := context.WithTimeout(ctx, 40*time.Second)
	defer done()
	if err := chromedp.Do(timed, chromedp.Navigate("https://podcasts.apple.com/")); err != nil {
		return errors.New("cannot reach the official Apple Podcasts login; the existing login was kept")
	}
	return nil
}
func evaluate[T any](b *Browser, script string) (T, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var empty T
	if err := b.startLocked(); err != nil {
		return empty, err
	}
	ctx, done := context.WithTimeout(b.ctx, 30*time.Second)
	defer done()
	value, err := chromedp.Run(ctx, chromedp.Evaluate[T](script, chromedp.EvalAwaitPromise))
	if err != nil {
		return empty, errors.New("the official Apple Podcasts login page did not respond; the existing login was kept")
	}
	return value, nil
}
func (b *Browser) Session() (Session, error) {
	type tokens struct{ Developer, User, Country string }
	r, err := evaluate[tokens](b, `(async()=>{const deadline=Date.now()+20000;let m;while(Date.now()<deadline){m=globalThis.MusicKit?.getInstance?.();if(m?.developerToken)break;await new Promise(resolve=>setTimeout(resolve,200));}if(m?.updateUserTokenFromStorage)await m.updateUserTokenFromStorage();const cookie=document.cookie.split(';').map(x=>x.trim()).find(x=>x.startsWith('media-user-token='));return {Developer:m?.developerToken||'',User:cookie?.slice('media-user-token='.length)||'',Country:location.pathname.split('/')[1]||'us'};})()`)
	if err != nil {
		return Session{}, err
	}
	if len(r.Country) != 2 {
		r.Country = "us"
	}
	hash := sha256.Sum256([]byte(r.User))
	return Session{Authorized: r.User != "", Country: strings.ToLower(r.Country), ID: hex.EncodeToString(hash[:12]), Developer: r.Developer, User: r.User}, nil
}
func (b *Browser) SignIn() error {
	b.mu.Lock()
	if err := b.startLocked(); err != nil {
		b.mu.Unlock()
		return err
	}
	ctx, done := context.WithTimeout(b.ctx, 5*time.Second)
	window, err := chromedp.Call(ctx, cdpbrowser.GetWindowForTarget, cdpbrowser.GetWindowForTargetParams{})
	if err == nil {
		_, _ = chromedp.Call(ctx, cdpbrowser.SetWindowBounds, cdpbrowser.SetWindowBoundsParams{WindowID: window.WindowID, Bounds: &cdpbrowser.Bounds{WindowState: cdpbrowser.WindowStateNormal}})
	}
	done()
	b.mu.Unlock()
	_, err = evaluate[bool](b, `(()=>{if(globalThis.MusicKit?.getInstance?.()?.isAuthorized)return true;const button=document.querySelector('button[data-testid="sign-in-button"]');if(button)button.click();return !!button;})()`)
	return err
}
func (b *Browser) SignOut() error {
	_, err := evaluate[bool](b, `(async()=>{await globalThis.MusicKit?.getInstance?.()?.unauthorize();return true;})()`)
	return err
}
func (b *Browser) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel != nil {
		if b.ctx != nil && b.ctx.Err() == nil {
			_ = chromedp.Cancel(b.ctx)
		}
		b.cancel()
	}
	if b.allocatorCancel != nil {
		b.allocatorCancel()
	}
	b.ctx = nil
}
