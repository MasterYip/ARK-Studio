package chat

import (
	"bytes"
	"context"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/pyurl"
	"arkstudio/go/internal/store"
)

// TestConnection is assistant_config.test_connection(config): one tiny
// non-streaming completion.  Errors are ValueErrors (400 at the caller)
// except Python exceptions outside that family (store.InternalError).
func TestConnection(ctx context.Context, config *pyjson.Object) (*pyjson.Object, error) {
	key := config.Value("api_key")
	if !pyjson.Truthy(key) {
		return nil, valueErr("Save an API key first")
	}
	body := pyjson.NewObject("model", config.Value("model"),
		"messages", []any{pyjson.NewObject("role", "user", "content", "Reply with exactly OK.")},
		"max_tokens", int64(16), "stream", false)
	url := pyjson.Str(config.Value("base_url")) + "/chat/completions"
	if !strings.Contains(url, ":") {
		return nil, valueErr("unknown url type: " + pyjson.Repr(url))
	}
	timeout := timeoutOf(config)
	if timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(pyjson.DumpsASCII(body)))
	if err != nil {
		return nil, valueErr("<urlopen error unknown url type: " + strings.SplitN(url, ":", 2)[0] + ">")
	}
	req.Header.Set("Authorization", "Bearer "+pyjson.Str(key))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "Python-urllib/3.10")
	started := time.Now()
	resp, err := client(timeout).Do(req)
	if err != nil {
		return nil, valueErr(urlError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		detail := pytext.Strip(pyurl.DecodeReplace(raw))
		if detail == "" {
			detail = strings.TrimSpace(strings.TrimPrefix(resp.Status, strconv.Itoa(resp.StatusCode)))
		}
		return nil, valueErr("HTTP " + strconv.Itoa(resp.StatusCode) + ": " + detail)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, valueErr(urlError(err))
	}
	text := pyurl.DecodeStrict(raw)
	if text == nil {
		return nil, valueErr("'utf-8' codec can't decode the response")
	}
	data, err := pyjson.Loads(*text)
	if err != nil {
		return nil, valueErr(jsonError(*text))
	}
	// data["choices"][0]["message"].get("content") or "": KeyError,
	// IndexError and TypeError leave "", an AttributeError propagates.
	message := any("")
	func() {
		o, ok := data.(*pyjson.Object)
		if !ok {
			return
		}
		choices, ok := o.Get("choices")
		if !ok {
			return
		}
		var first any
		switch c := choices.(type) {
		case []any:
			if len(c) == 0 {
				return
			}
			first = c[0]
		case string:
			return // "x"[0]["message"]: TypeError
		default: // dict[0] is a KeyError (JSON keys are strings), a number a TypeError
			return
		}
		fo, ok := first.(*pyjson.Object)
		if !ok {
			return // list/str/number index: TypeError
		}
		msg, ok := fo.Get("message")
		if !ok {
			return
		}
		mo, ok := msg.(*pyjson.Object)
		if !ok {
			message = &store.InternalError{Message: "'" + store.PyTypeName(msg) + "' object has no attribute 'get'"}
			return
		}
		if v := mo.Value("content"); pyjson.Truthy(v) {
			message = v
		}
	}()
	if e, ok := message.(*store.InternalError); ok {
		return nil, e
	}
	reply := pyjson.Str(message)
	if r := []rune(reply); len(r) > 200 {
		reply = string(r[:200])
	}
	latency := float64(time.Since(started).Microseconds()) / 1000
	return pyjson.NewObject("ok", true, "model", config.Value("model"),
		"latency_ms", int64(math.RoundToEven(latency)), "reply", reply), nil
}
