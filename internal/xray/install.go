package xray

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

func ExeName() string {
	if runtime.GOOS == "windows" {
		return "xray.exe"
	}
	return "xray"
}

// CoreFiles must all be present: the executable, the geoip/geosite data for routing rules and, on
// Windows, wintun.dll for TUN mode (shipped in the official package).
func CoreFiles() []string {
	files := []string{ExeName(), "geoip.dat", "geosite.dat"}
	if runtime.GOOS == "windows" {
		files = append(files, "wintun.dll")
	}
	return files
}

// licenseFiles are kept with the core (needed to redistribute Xray and Wintun) but are optional.
var licenseFiles = []string{"LICENSE", "LICENSE-Wintun"}

// Missing returns the core files missing in dir; empty means ready.
func Missing(dir string) []string {
	var out []string
	for _, f := range CoreFiles() {
		if fi, err := os.Stat(filepath.Join(dir, f)); err != nil || fi.Size() == 0 {
			out = append(out, f)
		}
	}
	return out
}

// assetName is the release archive for this platform.
func assetName() (string, error) {
	names := map[string]string{
		"windows/amd64": "Xray-windows-64.zip",
		"windows/arm64": "Xray-windows-arm64-v8a.zip",
		"windows/386":   "Xray-windows-32.zip",
		"linux/amd64":   "Xray-linux-64.zip",
		"linux/arm64":   "Xray-linux-arm64-v8a.zip",
		"darwin/amd64":  "Xray-macos-64.zip",
		"darwin/arm64":  "Xray-macos-arm64-v8a.zip",
	}
	if n, ok := names[runtime.GOOS+"/"+runtime.GOARCH]; ok {
		return n, nil
	}
	return "", fmt.Errorf("没有适用于 %s/%s 的 Xray 发布包", runtime.GOOS, runtime.GOARCH)
}

type Release struct {
	Tag     string // e.g. v26.3.27
	ZipURL  string
	DgstURL string
}

// MinTUNVersion is the oldest core that runs RayDock's TUN config: older ones lack autoSystemWfpBlockLeak,
// and before 26.4.13 gateway and autoSystemRoutingTable are silently ignored.
const MinTUNVersion = "26.9.30"

// LatestRelease returns the newest XTLS/Xray-core release with a package for this platform. Since 26.4.13
// every release is marked pre-release and /releases/latest returns the old 26.3.27, whose TUN cannot
// take over routing, so the first non-draft release in the list is taken.
func LatestRelease(ctx context.Context, client *http.Client) (Release, error) {
	name, err := assetName()
	if err != nil {
		return Release{}, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/XTLS/Xray-core/releases?per_page=10", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("查询最新版本失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("查询最新版本失败：HTTP %d", resp.StatusCode)
	}
	return pickRelease(resp.Body, name)
}

// pickRelease picks the first non-draft release (newest first) that has asset name and its digest.
func pickRelease(r io.Reader, name string) (Release, error) {
	var list []struct {
		Tag    string `json:"tag_name"`
		Draft  bool   `json:"draft"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(r).Decode(&list); err != nil {
		return Release{}, fmt.Errorf("解析版本信息失败：%w", err)
	}
	for _, body := range list {
		if body.Draft {
			continue
		}
		rel := Release{Tag: body.Tag}
		for _, a := range body.Assets {
			switch a.Name {
			case name:
				rel.ZipURL = a.URL
			case name + ".dgst":
				rel.DgstURL = a.URL
			}
		}
		if rel.ZipURL != "" && rel.DgstURL != "" {
			return rel, nil
		}
	}
	return Release{}, fmt.Errorf("最近的版本中没有 %s 及其校验文件", name)
}

// Install downloads rel, checks its SHA-256 against the official .dgst and extracts CoreFiles into dir.
// Files are replaced only after all were written, so a failure leaves an old core intact. Stop a core
// running from dir first; RayDock downloads to a directory of its own and installs from it (CopyCore).
func Install(ctx context.Context, client *http.Client, rel Release, dir string, progress func(done, total int64)) error {
	want, err := fetchDigest(ctx, client, rel.DgstURL)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "download-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rel.ZipURL, nil)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("下载失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}
	h := sha256.New()
	pw := &progressWriter{total: resp.ContentLength, fn: progress}
	size, err := io.Copy(io.MultiWriter(tmp, h, pw), resp.Body)
	if err != nil {
		return fmt.Errorf("下载中断：%w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("校验失败：SHA-256 为 %s，应为 %s", got, want)
	}

	zr, err := zip.NewReader(tmp, size)
	if err != nil {
		return fmt.Errorf("压缩包损坏：%w", err)
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	return stage(dir, CoreFiles(), licenseFiles, nil, func(name, dst string) (bool, error) {
		f := files[name]
		if f == nil {
			return false, nil
		}
		return true, extract(f, dst)
	})
}

// CopyCore copies the core in src (e.g. the bundled one) into dir, replacing files like Install.
func CopyCore(src, dir string, keep []string) error {
	return copyFiles(src, dir, CoreFiles(), licenseFiles, keep)
}

// CopyGeo installs a separately downloaded, verified rule set without touching the executable.
func CopyGeo(src, dir string) error {
	return copyFiles(src, dir, GeoFiles, nil, nil)
}

func copyFiles(src, dir string, required, optional, keep []string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return stage(dir, required, optional, keep, func(name, dst string) (bool, error) {
		in, err := os.Open(filepath.Join(src, name))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return true, err
		}
		defer in.Close()
		return true, writeFile(dst, in)
	})
}

// stage writes a file set through put as *.new files and commits only once all
// required files are present. Files in keep that dir has are left alone.
func stage(dir string, required, optional, keep []string, put func(name, dst string) (bool, error)) error {
	var staged, missing []string
	defer func() {
		for _, p := range staged {
			os.Remove(p)
		}
	}()
	for i, name := range slices.Concat(required, optional) {
		if slices.Contains(keep, name) && exists(filepath.Join(dir, name)) {
			continue
		}
		p := filepath.Join(dir, name+".new")
		found, err := put(name, p)
		if found {
			staged = append(staged, p)
		}
		if err != nil {
			return fmt.Errorf("写入 %s 失败：%w", name, err)
		}
		if !found && i < len(required) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少文件：%v", missing)
	}
	if err := commitFiles(staged); err != nil {
		return err
	}
	staged = nil
	return nil
}

// fetchDigest reads the "SHA2-256= <hex>" line of a .dgst file.
func fetchDigest(ctx context.Context, client *http.Client, url string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载校验文件失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载校验文件失败：HTTP %d", resp.StatusCode)
	}
	return parseDigest(resp.Body)
}

func parseDigest(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "SHA2-256="); ok {
			v = strings.TrimSpace(v)
			if len(v) == 64 {
				return v, nil
			}
		}
	}
	return "", errors.New("校验文件中没有 SHA2-256")
}

func extract(f *zip.File, path string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return writeFile(path, rc)
}

func writeFile(path string, r io.Reader) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

type progressWriter struct {
	done, total int64
	fn          func(done, total int64)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.done += int64(len(p))
	if w.fn != nil {
		w.fn(w.done, w.total)
	}
	return len(p), nil
}

func exists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Size() > 0
}
