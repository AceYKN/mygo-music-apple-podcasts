package apple

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

func NativeMedia(item map[string]any) (string, string, error) {
	a, _ := item["attributes"].(map[string]any)
	asset, _ := a["assetUrl"].(string)
	u, err := url.Parse(asset)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", "", &Error{Code: "unavailable", Message: "No directly playable audio is available for this episode"}
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(u.Path), "."))
	if ext == "m3u8" || ext == "mpd" {
		return "", "", &Error{Code: "unsupported", Message: "This episode needs a protected or segmented playback format that has not been verified with native MyGo playback"}
	}
	if ext == "m4a" || ext == "mp4" {
		return asset, "m4a", nil
	}
	for _, format := range []string{"mp3", "aac", "ogg", "flac", "wav"} {
		if ext == format {
			return asset, format, nil
		}
	}
	return asset, "", nil
}

// SniffMedia uses a fresh credential-free HTTP client. Apple account headers
// cannot reach the publisher or any of its audio redirects.
func SniffMedia(ctx context.Context, asset string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", asset, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Range", "bytes=0-63")
	client := &http.Client{Timeout: 12 * time.Second}
	res, err := client.Do(request)
	if err != nil {
		return "", &Error{Code: "network", Message: "Cannot reach the podcast audio host. Your Apple login was kept."}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 && res.StatusCode != 206 {
		return "", &Error{Code: "unavailable", Message: "The podcast audio host did not provide playable audio"}
	}
	bytes, _ := io.ReadAll(io.LimitReader(res.Body, 64))
	mime := strings.ToLower(res.Header.Get("Content-Type"))
	switch {
	case len(bytes) >= 8 && string(bytes[4:8]) == "ftyp", strings.Contains(mime, "mp4"), strings.Contains(mime, "m4a"):
		return "m4a", nil
	case strings.HasPrefix(string(bytes), "OggS"):
		return "ogg", nil
	case strings.HasPrefix(string(bytes), "fLaC"):
		return "flac", nil
	case strings.HasPrefix(string(bytes), "RIFF"):
		return "wav", nil
	case strings.Contains(mime, "aac"):
		return "aac", nil
	case strings.HasPrefix(string(bytes), "ID3"), strings.Contains(mime, "mpeg"):
		return "mp3", nil
	}
	return "", &Error{Code: "unsupported", Message: "The episode audio format has not been verified with native MyGo playback"}
}
