// fetchcore downloads the official Xray core into the installer resources before packaging:
//
//	go run ./cmd/fetchcore   # writes resources/windows-amd64/core
//	go tool mygo build
//
// It shares RayDock's download code (latest non-draft release, SHA-256 checked against .dgst)
// and skips the download when a working core is present, unless -force is given. The rule data of the
// package is replaced by Loyalsoldier/v2ray-rules-dat's, which RayDock updates from (see xray.GeoFiles).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"raydock/internal/xray"
)

func main() {
	dir := flag.String("dir", filepath.Join("resources", "windows-amd64", "core"), "target directory")
	force := flag.Bool("force", false, "download even if a core is present")
	flag.Parse()

	if !*force && len(xray.Missing(*dir)) == 0 {
		v, err := xray.Version(filepath.Join(*dir, xray.ExeName()))
		if err == nil {
			fmt.Printf("%s already has Xray %s, skipping (use -force to download again)\n", *dir, v)
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	rel, err := xray.LatestRelease(ctx, client)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("downloading Xray %s to %s\n", rel.Tag, *dir)
	last := -1
	err = xray.Install(ctx, client, rel, *dir, func(done, total int64) {
		if total > 0 {
			if p := int(done * 100 / total); p/10 != last/10 {
				last = p
				fmt.Printf("  %d%%\n", p)
			}
		}
	})
	if err != nil {
		log.Fatal(err)
	}
	tag, err := xray.LatestGeo(ctx, client)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("downloading rule data %s\n", tag)
	if err := xray.InstallGeo(ctx, client, tag, *dir, nil); err != nil {
		log.Fatal(err)
	}
	for _, f := range xray.CoreFiles() {
		fi, _ := os.Stat(filepath.Join(*dir, f))
		fmt.Printf("  %-12s %d bytes\n", f, fi.Size())
	}
}
