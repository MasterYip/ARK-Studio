package server

// One phone is one device (mobile/deviceid.go): a sign-in that sends the
// phone's device id replaces the device's earlier sign-ins, the WebView
// (cookie ark_device) and the native screens (X-ARK-Device) count as the
// same device, older apps without an id keep working, and the person
// merges leftover duplicates or removes stale devices in Settings.

import (
	"testing"
)

const testPhoneID = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"

func phoneLogin(t *testing.T, app *App, project, deviceID string) (string, string) {
	t.Helper()
	body := map[string]any{"username": "ana", "password": desktopPassword, "project_id": project, "device": "HUAWEI ALN-AL00"}
	if deviceID != "" {
		body["device_id"] = deviceID
	}
	r := gpuCall(t, app, "POST", "/api/mobile/login", body, map[string]string{"User-Agent": "ARKStudioAndroid/3.0"})
	token, _ := r.body["mobile_token"].(string)
	if r.code != 200 || token == "" {
		t.Fatalf("login: %d %s", r.code, r.raw)
	}
	if r.body["device_recognized"] != (deviceID != "") {
		t.Fatalf("device_recognized: %v", r.body["device_recognized"])
	}
	pair, _ := r.body["pairing_id"].(string)
	return token, pair
}

func activePairs(t *testing.T, app *App) []map[string]any {
	t.Helper()
	r := gpuCall(t, app, "GET", "/api/mobile/pairs", nil, sessionCookie(app, "ana"))
	if r.code != 200 {
		t.Fatalf("pairs: %d %s", r.code, r.raw)
	}
	var out []map[string]any
	for _, p := range r.body["pairs"].([]any) {
		if item := p.(map[string]any); item["state"] == "claimed" {
			out = append(out, item)
		}
	}
	return out
}

func TestPhoneSignInsWithDeviceIDAreOneDevice(t *testing.T) {
	app, team := authDesktopApp(t)
	first, _ := phoneLogin(t, app, team, testPhoneID)
	second, pair := phoneLogin(t, app, team, testPhoneID)
	// The native screens send the id as a header, the WebView as a cookie.
	if r := gpuCall(t, app, "GET", "/api/bootstrap", nil, map[string]string{"X-ARK-Mobile-Token": second, "X-ARK-Device": testPhoneID}); r.code != 200 {
		t.Fatalf("new token: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/bootstrap", nil, mobileHeader(first)); r.code != 401 {
		t.Fatalf("replaced token still works: %d", r.code)
	}
	web := map[string]string{"Cookie": "ark_mobile=" + second + "; ark_device=" + testPhoneID, "X-ARK-Project": team}
	if r := gpuCall(t, app, "GET", "/api/bootstrap", nil, web); r.code != 200 {
		t.Fatalf("WebView: %d %s", r.code, r.raw)
	}
	pairs := activePairs(t, app)
	if len(pairs) != 1 || pairs[0]["id"] != pair || pairs[0]["device_id_missing"] != false {
		t.Fatalf("devices: %v", pairs)
	}
	// A different phone of the same person stays a device of its own.
	other, _ := phoneLogin(t, app, team, "another-phone-0123456789abcdef")
	if r := gpuCall(t, app, "GET", "/api/bootstrap", nil, mobileHeader(other)); r.code != 200 || len(activePairs(t, app)) != 2 {
		t.Fatalf("second phone: %d %d", r.code, len(activePairs(t, app)))
	}
}

func TestOldAppSignInsMergeAndStale(t *testing.T) {
	app, team := authDesktopApp(t)
	a, _ := phoneLogin(t, app, team, "")
	b, _ := phoneLogin(t, app, team, "")
	c, keep := phoneLogin(t, app, team, "")
	for _, token := range []string{a, b, c} {
		if r := gpuCall(t, app, "GET", "/api/bootstrap", nil, mobileHeader(token)); r.code != 200 {
			t.Fatalf("old app token: %d", r.code)
		}
	}
	pairs := activePairs(t, app)
	if len(pairs) != 3 || pairs[0]["device_id_missing"] != true {
		t.Fatalf("old app devices: %v", pairs)
	}
	// A paired phone cannot merge devices.
	if r := gpuCall(t, app, "POST", "/api/mobile/pairs/merge", map[string]any{"like": keep}, mobileHeader(c)); r.code != 403 {
		t.Fatalf("phone merged: %d", r.code)
	}
	desk := sessionCookie(app, "ana")
	preview := gpuCall(t, app, "POST", "/api/mobile/pairs/merge", map[string]any{"like": keep, "dry_run": true}, desk)
	if preview.code != 200 || preview.body["keep"].(map[string]any)["id"] != keep || len(preview.body["revoke"].([]any)) != 2 {
		t.Fatalf("preview: %d %s", preview.code, preview.raw)
	}
	if len(activePairs(t, app)) != 3 {
		t.Fatal("the preview revoked devices")
	}
	done := gpuCall(t, app, "POST", "/api/mobile/pairs/merge", map[string]any{"like": keep}, desk)
	if done.code != 200 || len(activePairs(t, app)) != 1 {
		t.Fatalf("merge: %d %s", done.code, done.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/bootstrap", nil, mobileHeader(c)); r.code != 200 {
		t.Fatalf("kept token: %d", r.code)
	}
	if r := gpuCall(t, app, "GET", "/api/bootstrap", nil, mobileHeader(a)); r.code != 401 {
		t.Fatalf("merged-away token: %d", r.code)
	}
	// Nothing is stale yet; the cleanup revokes nothing.
	stale := gpuCall(t, app, "POST", "/api/mobile/pairs/stale", map[string]any{}, desk)
	if stale.code != 200 || len(stale.body["stale"].([]any)) != 0 || len(activePairs(t, app)) != 1 {
		t.Fatalf("stale: %d %s", stale.code, stale.raw)
	}
}
