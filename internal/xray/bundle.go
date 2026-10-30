package xray

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

const bundleManifest = "bundle.sha256.json"

func bundleFiles() []string { return append(CoreFiles(), GeoTagFile) }

// BundleReady trusts a preparation only when its manifest matches every resource and the rules tag.
// A legacy tag by itself cannot prove that a failed forced preparation did not replace its rule files.
func BundleReady(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, bundleManifest))
	if err != nil || len(data) > 64*1024 || ReadGeoTag(dir) == "" {
		return false
	}
	var sums map[string]string
	if json.Unmarshal(data, &sums) != nil {
		return false
	}
	for _, name := range bundleFiles() {
		got, err := fileSum(filepath.Join(dir, name))
		if err != nil || got != sums[name] {
			return false
		}
	}
	return true
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sealBundle(dir string) error {
	sums := map[string]string{}
	for _, name := range bundleFiles() {
		sum, err := fileSum(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		sums[name] = sum
	}
	data, err := json.MarshalIndent(sums, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, bundleManifest), data, 0600)
}

// PrepareBundle stages the official executable, full Loyalsoldier rules, their tag and integrity marker
// together. The destination is untouched until all downloads and validation succeeded.
func PrepareBundle(ctx context.Context, client *http.Client, rel Release, dir string, progress func(int64, int64)) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(dir), "bundle-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err = Install(ctx, client, rel, stage, progress); err != nil {
		return err
	}
	tag, err := LatestGeo(ctx, client)
	if err != nil {
		return err
	}
	if err = InstallGeo(ctx, client, tag, stage, nil); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(stage, GeoTagFile), []byte(tag+"\n"), 0600); err != nil {
		return err
	}
	if ReadGeoTag(stage) == "" {
		return fmt.Errorf("规则数据版本标签无效：%s", tag)
	}
	if _, err = Version(filepath.Join(stage, ExeName())); err != nil {
		return fmt.Errorf("核心版本验证失败：%w", err)
	}
	if err = sealBundle(stage); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	required := append(bundleFiles(), bundleManifest)
	return copyFiles(stage, dir, required, licenseFiles, nil)
}
