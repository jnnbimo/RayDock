package brand

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestImage(t *testing.T) {
	img := Image(64, Active)
	// Transparent corners, white tower in the middle.
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Errorf("corner should be transparent")
	}
	if r, g, b, _ := img.At(32, 44).RGBA(); r>>8 < 0xf0 || g>>8 < 0xf0 || b>>8 < 0xf0 {
		t.Errorf("tower should be white: %v", img.At(32, 44))
	}
	if Image(32, Idle).At(4, 16) == Image(32, Active).At(4, 16) {
		t.Error("idle style should differ from active")
	}
}

// The app icon must be generated from internal/brand so it matches the tray, window and sidebar.
func TestAppIconUpToDate(t *testing.T) {
	got, err := os.ReadFile(filepath.Join("..", "..", "resources", "icon.png"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, PNG(1024, Active)) {
		t.Fatal("resources/icon.png differs from the logo; run go run ./cmd/genicon")
	}
}
