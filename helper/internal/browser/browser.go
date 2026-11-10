// Package browser owns a dedicated official Chrome profile and Apple Podcasts page.
package browser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

type Session struct {
	Authorized bool   `json:"authorized"`
	Country    string `json:"country"`
	ID         string `json:"id"`
	Offline    bool   `json:"offline,omitempty"`
	Developer  string `json:"-"`
	User       string `json:"-"`
}

type Browser struct {
	mu                  sync.Mutex
	ctx                 context.Context
	cancel              context.CancelFunc
	allocatorCancel     context.CancelFunc
	profile, executable string
	muted               bool
	parked              bool
	lastAccount         string
	netMu               sync.Mutex
	netLog              []map[string]any
	requestOrigins      map[string]string
}

func New(profile, executable string, muted ...bool) *Browser {
	b := &Browser{profile: profile, executable: executable}
	if len(muted) > 0 {
		b.muted = muted[0]
	}
	return b
}

func ChromePath() string {
	for _, root := range []string{os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("LOCALAPPDATA")} {
		if root == "" {
			continue
		}
		path := filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return "google-chrome"
}

func (b *Browser) startLocked() error {
	if b.ctx != nil && b.ctx.Err() == nil {
		return nil
	}
	if err := os.MkdirAll(b.profile, 0700); err != nil {
		return errors.New("cannot create the dedicated browser profile")
	}
	// Do not use chromedp's headless defaults: they mute audio, change password
	// storage and disable component updates used by official media playback.
	alloc, ac := chromedp.NewExecAllocator(context.Background(),
		chromedp.ExecPath(b.executable), chromedp.UserDataDir(b.profile),
		chromedp.NoFirstRun, chromedp.NoDefaultBrowserCheck, chromedp.VisibleWindow,
		chromedp.WindowSize(1080, 780), chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-renderer-backgrounding", true), chromedp.Flag("disable-backgrounding-occluded-windows", true), chromedp.Flag("mute-audio", b.muted))
	ctx, cancel := chromedp.NewContext(alloc)
	b.ctx, b.cancel, b.allocatorCancel = ctx, cancel, ac
	b.parked = false
	if err := chromedp.Do(ctx); err != nil {
		if os.Getenv("APPLE_PODCASTS_DEBUG") == "1" {
			fmt.Fprintln(os.Stderr, "Chrome startup:", err)
		}
		cancel()
		ac()
		b.ctx = nil
		return errors.New("cannot start Google Chrome; install Chrome or select its executable")
	}
	b.netMu.Lock()
	b.requestOrigins = map[string]string{}
	b.netLog = nil
	b.netMu.Unlock()
	sent := chromedp.Events(ctx, network.RequestWillBeSent)
	failed := chromedp.Events(ctx, network.LoadingFailed)
	go func() {
		for event, err := range sent {
			if err != nil {
				return
			}
			u, e := url.Parse(event.Request.URL)
			if e != nil {
				continue
			}
			b.netMu.Lock()
			if len(b.requestOrigins) > 2000 {
				b.requestOrigins = map[string]string{}
			}
			b.requestOrigins[string(event.RequestID)] = u.Scheme + "://" + u.Host
			b.netMu.Unlock()
		}
	}()
	go func() {
		for event, err := range failed {
			if err != nil {
				return
			}
			b.netMu.Lock()
			b.netLog = append(b.netLog, map[string]any{"origin": b.requestOrigins[string(event.RequestID)], "error": event.ErrorText, "type": event.Type, "blocked": event.BlockedReason, "cors": event.CorsErrorStatus})
			if len(b.netLog) > 15 {
				b.netLog = b.netLog[len(b.netLog)-15:]
			}
			b.netMu.Unlock()
		}
	}()
	_, _ = chromedp.Call(ctx, network.Enable, network.EnableParams{})
	timed, done := context.WithTimeout(ctx, 40*time.Second)
	defer done()
	if err := chromedp.Do(timed, chromedp.Navigate("https://podcasts.apple.com/")); err != nil {
		return errors.New("cannot open Apple Podcasts; check the network or browser window")
	}
	return nil
}

func evaluate[T any](b *Browser, script string, options ...chromedp.EvaluateOption) (T, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var zero T
	if err := b.startLocked(); err != nil {
		return zero, err
	}
	ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
	defer cancel()
	options = append(options, chromedp.EvalAwaitPromise)
	value, err := chromedp.Run(ctx, chromedp.Evaluate[T](script, options...))
	if err != nil {
		return zero, errors.New("Apple Podcasts page did not complete the operation; reopen the official window and retry")
	}
	return value, nil
}

func gesture(p *cdpruntime.EvaluateParams) { enabled := true; p.UserGesture = &enabled }

type PlaybackError struct{ Message string }

func (e *PlaybackError) Error() string { return e.Message }

func (b *Browser) playerAction(script string) error {
	r, err := evaluate[struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}](b, `(async()=>{try{const m=globalThis.MusicKit?.getInstance?.();if(!m)throw Error('Player is still loading');`+script+`;return {ok:true};}catch(e){return {ok:false,message:String(e.message||e.name||'Playback failed').replace(/https?:\/\/[^\s]+/g,'[media URL]').slice(0,250)};}})()`, gesture)
	if err != nil {
		return err
	}
	if !r.OK {
		return &PlaybackError{Message: r.Message}
	}
	return nil
}

func (b *Browser) Session() (Session, error) {
	type tokens struct {
		Developer, User, Country, Consumer string
		Authorized                         bool
	}
	r, err := evaluate[tokens](b, `(async() => {
	 const deadline=Date.now()+20000;let m;
	 while(Date.now()<deadline){m=globalThis.MusicKit?.getInstance?.();if(m?.developerToken)break;await new Promise(resolve=>setTimeout(resolve,200));}
	 if(m?.updateUserTokenFromStorage)await m.updateUserTokenFromStorage();
	 const cookie = document.cookie.split(';').map(x=>x.trim()).find(x=>x.startsWith('media-user-token='));
	 return {Developer:m?.developerToken||'',User:cookie?.slice('media-user-token='.length)||'',
	 Country:location.pathname.split('/')[1]||'us',Consumer:String(m?.cid||''),Authorized:!!m?.isAuthorized};
	})()`)
	if err != nil {
		return Session{}, err
	}
	if len(r.Country) != 2 {
		r.Country = "us"
	}
	// Consumer/client IDs may identify the browser rather than its signed-in
	// account. A token fingerprint safely separates accounts without persisting
	// the credential. A rotated token starts a fresh cache namespace.
	hash := sha256.Sum256([]byte(r.User))
	id := hex.EncodeToString(hash[:12])
	b.mu.Lock()
	if b.lastAccount != "" && b.lastAccount != id && b.ctx != nil && b.ctx.Err() == nil {
		ctx, done := context.WithTimeout(b.ctx, 5*time.Second)
		_, _ = chromedp.Run(ctx, chromedp.Evaluate[bool](`(async()=>{await globalThis.MusicKit?.getInstance?.()?.stop();return true;})()`, chromedp.EvalAwaitPromise))
		done()
	}
	b.lastAccount = id
	if r.User != "" && !b.parked {
		b.windowLocked(cdpbrowser.WindowStateMinimized)
		b.parked = true
	}
	b.mu.Unlock()
	return Session{Authorized: r.User != "", Country: strings.ToLower(r.Country), ID: id, Developer: r.Developer, User: r.User}, nil
}

func (b *Browser) windowLocked(state cdpbrowser.WindowState) {
	if b.ctx == nil {
		return
	}
	ctx, done := context.WithTimeout(b.ctx, 5*time.Second)
	defer done()
	window, err := chromedp.Call(ctx, cdpbrowser.GetWindowForTarget, cdpbrowser.GetWindowForTargetParams{})
	if err == nil {
		_, _ = chromedp.Call(ctx, cdpbrowser.SetWindowBounds, cdpbrowser.SetWindowBoundsParams{WindowID: window.WindowID, Bounds: &cdpbrowser.Bounds{WindowState: state}})
	}
}
func (b *Browser) ShowWindow() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.startLocked(); err != nil {
		return err
	}
	b.windowLocked(cdpbrowser.WindowStateNormal)
	return nil
}

func (b *Browser) HideWindow() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.windowLocked(cdpbrowser.WindowStateMinimized)
	b.parked = true
}

func (b *Browser) SignIn() error {
	if err := b.ShowWindow(); err != nil {
		return err
	}
	_, err := evaluate[bool](b, `(() => {if(globalThis.MusicKit?.getInstance?.()?.isAuthorized)return true;
	 const button=document.querySelector('button[data-testid="sign-in-button"]'); if(button)button.click(); return !!button;})()`)
	return err
}

func (b *Browser) SignOut() error {
	_, err := evaluate[bool](b, `(async()=>{const m=globalThis.MusicKit?.getInstance?.();if(m){await m.stop();await m.unauthorize();}return true;})()`)
	return err
}

func (b *Browser) State() (map[string]any, error) {
	return evaluate[map[string]any](b, `(async()=>{const m=globalThis.MusicKit?.getInstance?.();if(!m)return {playing:false,position:0,duration:0,volume:100};
	 const position=await m.currentPlaybackTime,duration=await m.currentPlaybackDuration;
	 const media=[...document.querySelectorAll('audio,video')].find(x=>x.currentSrc&&x.readyState>=1);
	 const actualPosition=media&&Number.isFinite(media.currentTime)?media.currentTime:position;
	 const actualDuration=media&&Number.isFinite(media.duration)?media.duration:duration;
	 return {playing:m.playbackState===2&&(!media||(!media.paused&&!media.ended)),trackId:String(m.nowPlayingItem?.id||''),position:Math.round((Number.isFinite(actualPosition)?actualPosition:0)*1000),
	 duration:Math.round((Number.isFinite(actualDuration)?actualDuration:0)*1000),volume:Math.round((m.volume??1)*100),device:'Apple Podcasts · Chrome'};})()`)
}

func (b *Browser) Control(action string, value float64) error {
	var script string
	switch action {
	case "pause":
		script = `await m.pause();const deadline=Date.now()+5000;while(Date.now()<deadline&&[...document.querySelectorAll('audio,video')].some(x=>x.currentSrc&&!x.paused)){await new Promise(resolve=>setTimeout(resolve,100));}if([...document.querySelectorAll('audio,video')].some(x=>x.currentSrc&&!x.paused))throw Error('The official player has not paused. Retry.');`
	case "resume":
		script = `await m.play()`
	case "stop":
		script = `await m.stop()`
	case "seek":
		if value < 0 {
			return errors.New("position must be nonnegative")
		}
		data, _ := json.Marshal(value / 1000)
		script = `await m.seekToTime(` + string(data) + `);const deadline=Date.now()+7000;let confirmed=false;while(Date.now()<deadline){const media=[...document.querySelectorAll('audio,video')].find(x=>x.currentSrc&&x.readyState>=1);if(media&&!media.seeking&&Math.abs(media.currentTime-` + string(data) + `)<2){confirmed=true;break;}await new Promise(resolve=>setTimeout(resolve,100));}if(!confirmed)throw Error('The official player has not confirmed the seek. Retry.');`
	case "volume":
		if value < 0 || value > 100 {
			return errors.New("volume must be between 0 and 100")
		}
		data, _ := json.Marshal(value / 100)
		script = `m.volume=` + string(data)
	default:
		return errors.New("unknown playback action")
	}
	if err := b.ShowWindow(); err != nil {
		return err
	}
	err := b.playerAction(script)
	if err == nil {
		b.HideWindow()
	}
	return err
}

func (b *Browser) Play(items []any, position int64) error {
	data, err := json.Marshal(items)
	if err != nil {
		return err
	}
	pos, _ := json.Marshal(float64(position) / 1000)
	// The official player can leave a new media request pending while Chrome is
	// already minimized. Restore only for loading, then return it to the taskbar.
	if err = b.ShowWindow(); err != nil {
		return err
	}
	err = b.playerAction(`await m.stop();await m.setQueue({items:` + string(data) + `,startTime:` + string(pos) + `});await m.play();`)
	if err == nil {
		b.HideWindow()
	}
	return err
}

func (b *Browser) Inspect() (map[string]any, error) {
	// Diagnostics expose names/capabilities only, never cookies or access tokens.
	result, err := evaluate[map[string]any](b, `(()=>{const m=globalThis.MusicKit?.getInstance?.();return {
	 url:location.origin+location.pathname,title:document.title,ready:!!m,authorized:!!m?.isAuthorized,
	 methods:m?['setQueue','play','pause','stop','seekToTime','unauthorize'].filter(k=>typeof m[k]==='function'):[],
	 state:m?.playbackState,assetOrigin:(()=>{try{return new URL(m?.nowPlayingItem?.attributes?.assetUrl).origin}catch{return null}})(),
	 media:[...document.querySelectorAll('audio,video')].map(x=>({ready:x.readyState,paused:x.paused,time:x.currentTime,duration:Number.isFinite(x.duration)?x.duration:null,seeking:x.seeking,rate:x.playbackRate,sourceOrigin:(()=>{try{return new URL(x.currentSrc).origin}catch{return null}})(),error:x.error?{code:x.error.code,message:x.error.message?.replace(/https?:\/\/[^\s]+/g,'[media URL]')}:null})),
	 buttons:[...document.querySelectorAll('button')].slice(0,12).map(x=>({label:x.getAttribute('aria-label')||x.textContent?.trim(),testid:x.dataset.testid})),customTags:[...new Set([...document.querySelectorAll('*')].map(x=>x.tagName).filter(x=>x.startsWith('AMP-')))]};})()`)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.ctx != nil {
		ctx, done := context.WithTimeout(b.ctx, 5*time.Second)
		if window, e := chromedp.Call(ctx, cdpbrowser.GetWindowForTarget, cdpbrowser.GetWindowForTargetParams{}); e == nil && window.Bounds != nil {
			result["windowState"] = window.Bounds.WindowState
		}
		done()
	}
	b.mu.Unlock()
	b.netMu.Lock()
	result["networkFailures"] = append([]map[string]any{}, b.netLog...)
	b.netMu.Unlock()
	return result, nil
}

func (b *Browser) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel != nil {
		if b.ctx != nil && b.ctx.Err() == nil {
			// Pause while the official player is still alive so its ordinary
			// progress upload can complete before Chrome closes.
			ctx, done := context.WithTimeout(b.ctx, 8*time.Second)
			_, _ = chromedp.Run(ctx, chromedp.Evaluate[bool](`(async()=>{const m=globalThis.MusicKit?.getInstance?.();if(m?.nowPlayingItem){await m.pause();await new Promise(resolve=>setTimeout(resolve,5500));}return true;})()`, chromedp.EvalAwaitPromise))
			done()
			_ = chromedp.Cancel(b.ctx)
		}
		b.cancel()
	}
	if b.allocatorCancel != nil {
		b.allocatorCancel()
	}
	b.ctx = nil
}
