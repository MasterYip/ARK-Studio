package sessions

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// Images and files pasted into a session composer, and "send now" for a
// queued message.

const (
	maxUploadBytes = 8 << 20
	sendNowWait    = 20 * time.Second
)

var uploadNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// saveUpload stores a pasted image (base64, optionally a data: URL) in the
// session workspace under uploads/ and returns its workspace path.
func (m *Manager) saveUpload(s *Session, name, data string) (string, error) {
	if i := strings.Index(data, ","); strings.HasPrefix(data, "data:") && i > 0 {
		data = data[i+1:]
	}
	if base64.StdEncoding.DecodedLen(len(data)) > maxUploadBytes+3 {
		return "", serr("the image is larger than "+strconv.Itoa(maxUploadBytes>>20)+" MB", 413, "UPLOAD_TOO_LARGE")
	}
	blob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data))
	if err != nil || len(blob) == 0 {
		return "", serr("the image data is not valid base64", 400, "")
	}
	if len(blob) > maxUploadBytes {
		return "", serr("the image is larger than "+strconv.Itoa(maxUploadBytes>>20)+" MB", 413, "UPLOAD_TOO_LARGE")
	}
	base := uploadNameRe.ReplaceAllString(filepath.Base(pytext.Strip(name)), "-")
	base = strings.Trim(base, "-.")
	if base == "" {
		base = "image.png"
	}
	ext := strings.ToLower(filepath.Ext(base))
	if !map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true, ".svg": true}[ext] {
		return "", serr("only images can be pasted (png, jpg, gif, webp, bmp, svg)", 400, "")
	}
	dir := filepath.Join(s.Workspace(), "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", serr("cannot save the image: "+err.Error(), 500, "")
	}
	rel := "uploads/" + time.Now().UTC().Format("20060102-150405") + "-" + limit(base, 80)
	if err := os.WriteFile(filepath.Join(s.Workspace(), filepath.FromSlash(rel)), blob, 0o644); err != nil {
		return "", serr("cannot save the image: "+err.Error(), 500, "")
	}
	return rel, nil
}

// recordMedia notes the media sent with a message, so the transcript shows
// them with that turn.
func (m *Manager) recordMedia(s *Session, entries []*pyjson.Object) {
	var paths []any
	for _, e := range entries {
		if e.Value("kind") == "media" {
			paths = append(paths, e.Value("path"))
		}
	}
	if len(paths) > 0 {
		m.appendEvent(s, "stdout", string(pyjson.Dumps(paths)), "media")
	}
}

// SendSteerNow delivers a queued message at once.  A session that reads
// follow-up input gets it now; any other running turn is stopped and the
// message starts the next turn.  Other queued messages stay queued, in order.
func (m *Manager) SendSteerNow(h *Hooks, id, steerID, actor string) (*pyjson.Object, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	var item *pyjson.Object
	for _, e := range objects(s.Meta.Value("steer_queue")) {
		if strOr(e.Value("id")) == steerID {
			item = e
		}
	}
	if item == nil {
		m.mu.Unlock()
		return nil, serr("queued message not found", 404, "")
	}
	if item.Value("status") != "queued" {
		m.mu.Unlock()
		return nil, serr("this message was already sent", 409, "")
	}
	text := strOr(item.Value("text"))
	followup := pyjson.Truthy(s.Meta.Value("followup"))
	m.mu.Unlock()
	mode := "next_turn"
	if m.IsActive(s) && followup {
		if err := m.sendUserMessage(s, text, nil, text, true); err != nil {
			return nil, serr("could not send the message: "+err.Error(), 409, "")
		}
		m.mu.Lock()
		item.Set("status", "delivered").Set("delivered_at", NowISO()).Set("channel", "stdin")
		m.saveLocked(s)
		out := m.publicLocked(s, true)
		m.mu.Unlock()
		return pyjson.NewObject("session", out, "steer", steerPublic(item), "mode", "stdin"), nil
	}
	if m.IsActive(s) {
		mode = "interrupt"
		if _, err := m.RequestStop(id); err != nil {
			return nil, err
		}
		deadline := time.Now().Add(sendNowWait)
		for m.IsActive(s) && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if m.IsActive(s) {
			return nil, serr("the running turn did not stop in time; the message stays queued", 409, "")
		}
	}
	m.mu.Lock()
	item.Set("status", "delivered").Set("delivered_at", NowISO()).Set("channel", "continue")
	m.saveLocked(s)
	m.mu.Unlock()
	out, err := m.ContinueSession(h, id, text, ContinueOptions{Actor: actor, KeepQueue: true})
	if err != nil {
		m.mu.Lock()
		item.Set("status", "queued").Set("delivered_at", nil).Set("channel", "next_turn").Set("error", err.Error())
		m.saveLocked(s)
		m.mu.Unlock()
		return nil, err
	}
	return pyjson.NewObject("session", out, "steer", steerPublic(item), "mode", mode), nil
}

// attachMedia adds the media paths of a "media" log line to the last user
// turn (`images`).
func (b *transcriptBuilder) attachMedia(line string) {
	list, err := pyjson.Loads(line)
	paths, ok := list.([]any)
	if err != nil || !ok {
		return
	}
	for i := len(b.items) - 1; i >= 0; i-- {
		it := b.items[i]
		if it.Value("type") == "message" && it.Value("role") == "user" {
			prev, _ := it.Value("images").([]any)
			it.Set("images", append(append([]any{}, prev...), paths...))
			return
		}
	}
}
