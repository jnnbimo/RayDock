package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ReleasesPage is where RayDock's releases are downloaded. RayDock never updates itself: asked, it looks
// for a newer release (CheckUpdate) and opens this page.
const ReleasesPage = "https://github.com/jnnbimo/RayDock/releases/latest"

// releaseAPI gives RayDock's latest release, which is neither a draft nor a pre-release; tests point it at a
// local server.
var releaseAPI = "https://api.github.com/repos/jnnbimo/RayDock/releases/latest"

// CheckUpdate looks for a newer release of RayDock, when the user asks: it offers one (UpdateLatest), or
// says there is none or why the lookup failed (UpdateNote). It downloads nothing. A build without a
// version, as from go run, does not look.
func (a *App) CheckUpdate() {
	if a.closing || a.UpdateChecking || a.Version == "" {
		return
	}
	a.UpdateChecking, a.UpdateNote = true, ""
	current, client := a.Version, a.httpClient(0)
	a.jobs.Go(func() {
		defer client.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(a.workCtx, 30*time.Second)
		defer cancel()
		tag, err := latestRelease(ctx, client)
		a.Post(func() {
			a.UpdateChecking = false
			switch {
			case err != nil:
				a.UpdateNote = "检查更新失败：" + err.Error()
			case newerVersion(tag, current):
				a.UpdateLatest = tag
			default:
				a.UpdateNote = "已是最新版本"
			}
		})
	})
}

// OpenReleases opens the download page in the browser.
func (a *App) OpenReleases() {
	if a.OpenURL != nil {
		a.OpenURL(ReleasesPage)
	}
}

// latestRelease returns the tag of RayDock's latest release, e.g. v0.1.8.
func latestRelease(ctx context.Context, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseAPI, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var rel struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return "", fmt.Errorf("解析版本信息失败：%w", err)
	}
	if parseVersion(rel.Tag) == nil {
		return "", fmt.Errorf("无法识别的版本号 %q", rel.Tag)
	}
	return rel.Tag, nil
}
