package xray

import (
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
	"strings"
)

// GeoFiles are the rule data routing uses (geoip:, geosite:), from Loyalsoldier/v2ray-rules-dat, which
// publishes them daily. Xray's packages carry their own copy, since 26.10.21 from XTLS/Xray-rules-dat with
// a geoip.dat reduced to a few categories (cn and private, but no geoip:jp): RayDock installs it only into
// a directory without rule data, and puts Loyalsoldier's into the installer (cmd/fetchcore).
var GeoFiles = []string{"geoip.dat", "geosite.dat"}

// GeoTagFile, beside rule data of a release, holds its tag: cmd/fetchcore puts it in the installer's core
// directory, so that RayDock knows the rule data it brings.
const GeoTagFile = "geo.tag"

// ReadGeoTag returns the tag in dir's GeoTagFile, "" without a valid one. Tags are release times
// (202610282204), which compare as strings.
func ReadGeoTag(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, GeoTagFile))
	if err != nil {
		return ""
	}
	tag := strings.TrimSpace(string(b))
	if len(tag) != len("200601021504") || strings.Trim(tag, "0123456789") != "" {
		return ""
	}
	return tag
}

// NewerGeo reports whether rule data tag is newer than installed, "" for rule data of unknown release:
// the core's own, or an older installer's.
func NewerGeo(tag, installed string) bool {
	return tag != "" && (installed == "" || len(installed) == len(tag) && tag > installed)
}

// Where rule data releases are listed and downloaded; tests point them at a local server.
var (
	geoAPI       = "https://api.github.com/repos/Loyalsoldier/v2ray-rules-dat/releases/latest"
	geoDownloads = "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/download/"
)

// LatestGeo returns the tag of the newest rule data release, e.g. 202610132213.
func LatestGeo(ctx context.Context, client *http.Client) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, geoAPI, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("查询规则数据失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("查询规则数据失败：HTTP %d", resp.StatusCode)
	}
	var rel struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil || rel.Tag == "" {
		return "", errors.New("无法解析规则数据的版本")
	}
	return rel.Tag, nil
}

// InstallGeo downloads the rule data of release tag into dir, checking each file against its published
// SHA-256, and replaces the files once both are verified. A running core keeps working: Xray reads them
// only while loading, so a restart picks the new ones up.
func InstallGeo(ctx context.Context, client *http.Client, tag, dir string, progress func(done, total int64)) error {
	base := geoDownloads + tag + "/"
	var staged []string
	defer func() {
		for _, p := range staged {
			os.Remove(p)
		}
	}()
	// Progress counts each file as an equal share: sizes are known only once a download starts, and a total
	// growing file by file would move the bar backwards.
	const share = 1 << 20
	for i, name := range GeoFiles {
		pw := &progressWriter{fn: func(done, total int64) {
			if progress != nil && total > 0 {
				progress(int64(i)*share+done*share/total, int64(len(GeoFiles))*share)
			}
		}}
		want, err := fetchSum(ctx, client, base+name+".sha256sum")
		if err != nil {
			return fmt.Errorf("%s：%w", name, err)
		}
		dst := filepath.Join(dir, name+".new")
		staged = append(staged, dst)
		if err := download(ctx, client, base+name, dst, want, pw); err != nil {
			return fmt.Errorf("%s：%w", name, err)
		}
	}
	if err := commitFiles(staged); err != nil {
		return err
	}
	staged = nil
	return nil
}

// fetchSum reads a sha256sum file: "<hex>  <name>".
func fetchSum(ctx context.Context, client *http.Client, url string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载校验文件失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载校验文件失败：HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 || len(fields[0]) != 64 {
		return "", errors.New("校验文件格式错误")
	}
	return fields[0], nil
}

// download writes url to path, failing unless its SHA-256 is want, reporting to pw.
func download(ctx context.Context, client *http.Client, url, path, want string, pw *progressWriter) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("下载失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}
	pw.total = resp.ContentLength
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h, pw), resp.Body)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("下载中断：%w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("校验失败：SHA-256 为 %s，应为 %s", got, want)
	}
	return nil
}
