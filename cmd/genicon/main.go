// genicon writes the app icon resources/icon.png (1024×1024) from internal/brand; mygo build
// derives the executable and installer icons from it. Run it after changing the logo:
//
//	go run ./cmd/genicon
package main

import (
	"log"
	"os"
	"path/filepath"

	"raydock/internal/brand"
)

func main() {
	path := filepath.Join("resources", "icon.png")
	if err := os.WriteFile(path, brand.PNG(1024, brand.Active), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Println("wrote", path)
}
