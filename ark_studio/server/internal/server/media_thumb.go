package server

// GET /api/media/{id}/thumb (Go only): a small JPEG preview of an image
// attachment, or the first frame of a video (when ffmpeg is installed), for
// the thumbnails of lists such as Home's Needs you.  A list of reviews with
// dozens of full-size images and videos each made the browser download and
// decode all of them; a thumbnail is ~10-30 KB.  Thumbnails live in a
// bounded in-memory cache keyed by file, size and modification time (nothing
// is written next to the project); a file that cannot be previewed answers
// 404 and the client shows the original instead.

import (
	"bytes"
	"container/list"
	"context"
	"image"
	"image/color"
	_ "image/gif" // decoders for image.Decode
	"image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ThumbWidth is the preview width (2x and more of the 64 px list thumbnails).
const ThumbWidth = 160

const (
	thumbCacheBytes  = 64 << 20
	thumbMaxSource   = 40 << 20 // larger images are not decoded for a preview
	thumbMaxPixels   = 40_000_000
	thumbVideoWait   = 20 * time.Second
	thumbConcurrency = 2
)

var (
	reMediaThumb = regexp.MustCompile(`\A/api/media/(\d+)/thumb\z`)
	thumbImages  = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true}
	thumbVideos  = map[string]bool{".mp4": true, ".webm": true, ".mov": true, ".m4v": true, ".ogv": true}
	thumbSlots   = make(chan struct{}, thumbConcurrency)
	ffmpegOnce   sync.Once
	ffmpegPath   string
	thumbs       = newThumbCache(thumbCacheBytes)
)

// mediaThumbGoOnly are the Go-only ids of this file (GoOnlyRoutes).
var mediaThumbGoOnly = []PortedRoute{{`goonly:regex:^/api/media/(\d+)/thumb$`, "GET,HEAD"}}

// VideoPosters reports whether video thumbnails can be made (ffmpeg on PATH,
// or ARK_FFMPEG).
func VideoPosters() bool {
	ffmpegOnce.Do(func() {
		if p := strings.TrimSpace(os.Getenv("ARK_FFMPEG")); p != "" {
			ffmpegPath = p
			return
		}
		ffmpegPath = lookPath("ffmpeg")
	})
	return ffmpegPath != ""
}

func (c *reqCtx) mediaThumb(idText string) error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	path, _, err := c.resolveMedia(p, idText)
	if err != nil {
		return err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return apiErr(404, "Media file not found", "")
	}
	ext := strings.ToLower(filepath.Ext(path))
	video := thumbVideos[ext]
	if !thumbImages[ext] && !(video && VideoPosters()) {
		return apiErr(404, "No preview for this file", "")
	}
	etag := `"t` + strconv.FormatInt(fi.ModTime().UnixNano(), 16) + "-" + strconv.FormatInt(fi.Size(), 16) + "-" + strconv.Itoa(ThumbWidth) + `"`
	headers := headerList{{"ETag", etag}, {"Cache-Control", "private, max-age=86400"}, {"Content-Security-Policy", FileCSP}}
	if etagMatches(c.header("If-None-Match"), etag) {
		c.notModified(headers)
		return nil
	}
	key := path + "\x00" + etag
	data, ok := thumbs.get(key)
	if !ok {
		select {
		case thumbSlots <- struct{}{}:
		case <-c.ctx.Done():
			return c.ctx.Err()
		}
		data, ok = thumbs.get(key) // made by a request that held the slot
		if !ok {
			if video {
				data, err = videoPoster(c.ctx, path)
			} else {
				data, err = imageThumb(path, fi.Size())
			}
			if err == nil {
				thumbs.put(key, data)
			}
		}
		<-thumbSlots
		if err != nil {
			return apiErr(404, "No preview for this file", "")
		}
	}
	h := c.w.Header()
	for _, kv := range headers {
		h.Set(kv[0], kv[1])
	}
	h.Set("Content-Type", "image/jpeg")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("X-Content-Type-Options", "nosniff")
	c.w.WriteHeader(200)
	if c.r.Method != "HEAD" {
		c.w.Write(data) //nolint:errcheck // client went away
	}
	return nil
}

// imageThumb decodes a PNG/JPEG/GIF and returns a JPEG at most ThumbWidth
// wide (the original size when smaller), transparency on white.
func imageThumb(path string, size int64) ([]byte, error) {
	if size > thumbMaxSource {
		return nil, errThumb
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > thumbMaxPixels {
		return nil, errThumb
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	return encodeThumb(Downscale(src, ThumbWidth))
}

var errThumb = apiErr(404, "No preview for this file", "")

func encodeThumb(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 78}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Downscale returns src scaled to at most width pixels wide (aspect kept) by
// averaging the source pixels under each target pixel (area filter), on a
// white background.  Smaller images are only flattened.
func Downscale(src image.Image, width int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	tw, th := sw, sh
	if sw > width {
		tw = width
		th = max(1, int(float64(sh)*float64(width)/float64(sw)+0.5))
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	for ty := 0; ty < th; ty++ {
		y0, y1 := ty*sh/th, max(ty*sh/th+1, (ty+1)*sh/th)
		for tx := 0; tx < tw; tx++ {
			x0, x1 := tx*sw/tw, max(tx*sw/tw+1, (tx+1)*sw/tw)
			var r, g, bl, n uint64
			// At most 4x4 samples per target pixel: enough for a preview and
			// bounded for very large sources.
			stepY, stepX := max(1, (y1-y0)/4), max(1, (x1-x0)/4)
			for y := y0; y < y1; y += stepY {
				for x := x0; x < x1; x += stepX {
					cr, cg, cb, ca := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
					// Premultiplied: composite over white.
					r += uint64(cr + (0xffff - ca))
					g += uint64(cg + (0xffff - ca))
					bl += uint64(cb + (0xffff - ca))
					n++
				}
			}
			dst.SetRGBA(tx, ty, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(bl / n >> 8), 255})
		}
	}
	return dst
}

// videoPoster grabs a frame near the start of a video with ffmpeg.
func videoPoster(ctx context.Context, path string) ([]byte, error) {
	if !VideoPosters() {
		return nil, errThumb
	}
	ctx, cancel := context.WithTimeout(ctx, thumbVideoWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-ss", "0.1", "-i", path, "-frames:v", "1",
		"-vf", "scale='min("+strconv.Itoa(ThumbWidth)+",iw)':-2", "-f", "image2", "-c:v", "mjpeg", "-q:v", "5", "pipe:1")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil || out.Len() == 0 {
		return nil, errThumb
	}
	return out.Bytes(), nil
}

// thumbCache is a byte-bounded LRU.
type thumbCache struct {
	mu    sync.Mutex
	limit int
	size  int
	order *list.List
	items map[string]*list.Element
}

type thumbEntry struct {
	key  string
	data []byte
}

func newThumbCache(limit int) *thumbCache {
	return &thumbCache{limit: limit, order: list.New(), items: map[string]*list.Element{}}
}

func (t *thumbCache) get(key string) ([]byte, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if el, ok := t.items[key]; ok {
		t.order.MoveToFront(el)
		return el.Value.(*thumbEntry).data, true
	}
	return nil, false
}

func (t *thumbCache) put(key string, data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(data) > t.limit {
		return
	}
	if el, ok := t.items[key]; ok {
		t.size -= len(el.Value.(*thumbEntry).data)
		t.order.Remove(el)
	}
	t.items[key] = t.order.PushFront(&thumbEntry{key, data})
	t.size += len(data)
	for t.size > t.limit {
		last := t.order.Back()
		e := last.Value.(*thumbEntry)
		t.order.Remove(last)
		delete(t.items, e.key)
		t.size -= len(e.data)
	}
}
