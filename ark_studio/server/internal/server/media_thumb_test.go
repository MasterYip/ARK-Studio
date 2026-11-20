package server

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestDownscaleAndImageThumb(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 1000, 500))
	for y := 0; y < 500; y++ {
		for x := 0; x < 1000; x++ {
			c := color.NRGBA{255, 0, 0, 255}
			if x >= 500 {
				c = color.NRGBA{0, 0, 0, 0} // transparent: white in the preview
			}
			src.SetNRGBA(x, y, c)
		}
	}
	dst := Downscale(src, ThumbWidth)
	if dst.Bounds().Dx() != ThumbWidth || dst.Bounds().Dy() != ThumbWidth/2 {
		t.Fatalf("size %v", dst.Bounds())
	}
	if c := dst.RGBAAt(10, 10); c.R < 250 || c.G > 5 {
		t.Fatalf("left red: %v", c)
	}
	if c := dst.RGBAAt(ThumbWidth-10, 10); c.R < 250 || c.G < 250 || c.B < 250 {
		t.Fatalf("transparent on white: %v", c)
	}
	if small := Downscale(image.NewRGBA(image.Rect(0, 0, 40, 20)), ThumbWidth); small.Bounds().Dx() != 40 {
		t.Fatal("small images keep their size")
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := imageThumb(path, int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil || img.Bounds().Dx() != ThumbWidth {
		t.Fatalf("jpeg thumb: %v %v", err, img)
	}
	if len(data) > 20000 {
		t.Fatalf("thumbnail is %d bytes", len(data))
	}
	bad := filepath.Join(t.TempDir(), "b.png")
	os.WriteFile(bad, []byte("not an image"), 0o644) //nolint:errcheck
	if _, err := imageThumb(bad, 12); err == nil {
		t.Fatal("a broken file has no thumbnail")
	}
}

func TestThumbCacheEvicts(t *testing.T) {
	c := newThumbCache(10)
	c.put("a", make([]byte, 4))
	c.put("b", make([]byte, 4))
	c.get("a")
	c.put("c", make([]byte, 4)) // b is the least recently used
	if _, ok := c.get("b"); ok {
		t.Fatal("b evicted")
	}
	if _, ok := c.get("a"); !ok {
		t.Fatal("a kept")
	}
	c.put("huge", make([]byte, 11))
	if _, ok := c.get("huge"); ok {
		t.Fatal("larger than the cache: not kept")
	}
}

func TestMediaThumbRouteIsGoOnly(t *testing.T) {
	found := false
	for _, r := range GoOnlyRoutes {
		found = found || r.ID == `goonly:regex:^/api/media/(\d+)/thumb$`
	}
	if !found || !reMediaThumb.MatchString("/api/media/12/thumb") || reMediaThumb.MatchString("/api/media/x/thumb") {
		t.Fatal("thumb route")
	}
}
