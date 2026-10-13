package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testApp(t *testing.T) (*App, *httptest.Server) {
	t.Setenv("PATHAU_HOME", t.TempDir())
	a := newApp(8765)
	a.cfg.SaveDir = t.TempDir()
	a.cfg.AutoCopy = false
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	return a, srv
}

func do(t *testing.T, method, url, host string, body io.Reader, hdr map[string]string) *http.Response {
	req, _ := http.NewRequest(method, url, body)
	if host != "" {
		req.Host = host // pretend to come from the LAN instead of this PC
	}
	req.Header.Set("X-Pathau", "1")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestExtractCode(t *testing.T) {
	for in, want := range map[string]string{
		"Your OTP is 482913. Valid for 5 minutes.":          "482913",
		"Rs 5000 debited. OTP for txn: 7731. Do not share.": "7731",
		"482913 is your verification code":                  "482913",
		"G-123456 is your Google verification code":         "123456",
		"482913":                         "", // already just the code
		"See you at 1830 near the gate":  "", // not an OTP message
		"Your code expires soon, no num": "",
	} {
		if got := extractCode(in); got != want {
			t.Errorf("extractCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPathau(t *testing.T) {
	a, srv := testApp(t)
	lan := "192.168.1.5:8765"

	// fresh start: lists must be [] not null, or the UI crashes before showing the QR
	for _, u := range []string{"/api/texts", "/api/state"} {
		b, _ := io.ReadAll(do(t, "GET", srv.URL+u, "", nil, nil).Body)
		if strings.Contains(string(b), "null") {
			t.Fatalf("%s returned null: %s", u, b)
		}
	}

	// LAN device without passcode: locked out of user and admin routes
	if r := do(t, "GET", srv.URL+"/api/shares", lan, nil, nil); r.StatusCode != 401 {
		t.Fatalf("no passcode: got %d", r.StatusCode)
	}
	if r := do(t, "GET", srv.URL+"/api/state", lan, nil, nil); r.StatusCode != 401 {
		t.Fatalf("admin from LAN: got %d", r.StatusCode)
	}
	// writes without our header are refused (blocks cross-site requests)
	if r, _ := http.Post(srv.URL+"/api/text", "application/json", strings.NewReader(`{"text":"x"}`)); r.StatusCode != 403 {
		t.Fatalf("missing header: got %d", r.StatusCode)
	}

	// uploads never overwrite and never escape the save folder
	for _, want := range []string{"a.txt", "a (1).txt"} {
		r := do(t, "PUT", srv.URL+"/api/upload?name=a.txt", "", strings.NewReader("hi"), nil)
		var got struct{ Name string }
		json.NewDecoder(r.Body).Decode(&got)
		if got.Name != want {
			t.Fatalf("dedup: got %q want %q", got.Name, want)
		}
	}
	for _, bad := range []string{"../evil.txt", "C:/evil.txt", "/etc/x", "sub/../../x", "NUL"} {
		if r := do(t, "PUT", srv.URL+"/api/upload?name="+bad, "", strings.NewReader("x"), nil); r.StatusCode != 400 {
			t.Fatalf("path %q: got %d", bad, r.StatusCode)
		}
	}

	// send-to-phones outbox: only this PC can add; it then lists under s=out
	if r := do(t, "PUT", srv.URL+"/api/outbox?name=hi.txt", lan, strings.NewReader("x"), nil); r.StatusCode != 401 {
		t.Fatalf("outbox from LAN: got %d", r.StatusCode)
	}
	do(t, "PUT", srv.URL+"/api/outbox?name=hi.txt", "", strings.NewReader("x"), nil)
	if b, _ := io.ReadAll(do(t, "GET", srv.URL+"/api/ls?s=out", "", nil, nil).Body); !strings.Contains(string(b), "hi.txt") {
		t.Fatalf("outbox list: %s", b)
	}

	// pairing: right passcode -> pending (no access) until Allowed here; persists; removable
	code := a.pass
	r0 := do(t, "POST", srv.URL+"/api/login", lan, strings.NewReader(`{"code":"`+code+`"}`), nil)
	if r0.StatusCode != 202 || a.pass == code {
		t.Fatalf("pair: got %d, passcode rotated: %v", r0.StatusCode, a.pass != code)
	}
	cookie := map[string]string{"Cookie": "pathau=" + r0.Cookies()[0].Value}
	if r := do(t, "GET", srv.URL+"/api/shares", lan, nil, cookie); r.StatusCode != 401 {
		t.Fatalf("pending device got in: %d", r.StatusCode)
	}
	var devID string
	for _, d := range a.devices {
		devID = d.ID
	}
	do(t, "POST", srv.URL+"/api/devices/answer", "", strings.NewReader(`{"id":"`+devID+`","allow":true}`), nil)
	if r := do(t, "GET", srv.URL+"/api/shares", lan, nil, cookie); r.StatusCode != 200 {
		t.Fatalf("allowed device refused: %d", r.StatusCode)
	}
	if b, _ := os.ReadFile(a.devPath); strings.Contains(string(b), r0.Cookies()[0].Value) || !strings.Contains(string(b), devID) {
		t.Fatalf("devices.json must hold the device but not its raw key: %s", b)
	}
	do(t, "POST", srv.URL+"/api/devices/remove", "", strings.NewReader(`{"id":"`+devID+`"}`), nil)
	if r := do(t, "GET", srv.URL+"/api/shares", lan, nil, cookie); r.StatusCode != 401 {
		t.Fatalf("removed device still in: %d", r.StatusCode)
	}

	// iPhone Shortcut key: Bearer works without the X-Pathau header, and only for this key
	var sk struct{ Key string }
	json.NewDecoder(do(t, "POST", srv.URL+"/api/devices/shortcut", "", strings.NewReader(`{}`), nil).Body).Decode(&sk)
	post := func(key string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/api/text", strings.NewReader(`{"text":"Your OTP is 4829, valid 5 min"}`))
		req.Host = lan
		req.Header.Set("Authorization", "Bearer "+key)
		resp, _ := http.DefaultClient.Do(req)
		resp.Body.Close()
		return resp.StatusCode
	}
	if post(sk.Key) != 200 || post(sk.Key+"x") != 401 {
		t.Fatal("shortcut key auth")
	}
	if got := a.texts[len(a.texts)-1]; got.Code != "4829" || got.From != "iPhone Shortcut" {
		t.Fatalf("shortcut text: %+v", got)
	}

	// ten wrong passcodes pause the device, even for the right one
	for range maxFails {
		do(t, "POST", srv.URL+"/api/login", lan, strings.NewReader(`{"code":"nope"}`), nil)
	}
	if r := do(t, "POST", srv.URL+"/api/login", lan, strings.NewReader(`{"code":"`+a.pass+`"}`), nil); r.StatusCode != 429 {
		t.Fatalf("lockout: got %d", r.StatusCode)
	}

	// PC-to-PC: offer -> accept -> file with checksum trailer
	go func() {
		for {
			time.Sleep(20 * time.Millisecond)
			a.mu.Lock()
			for _, o := range a.offers {
				select {
				case o.answer <- true:
				default:
				}
			}
			a.mu.Unlock()
		}
	}()
	r := do(t, "POST", srv.URL+"/peer/offer", "", strings.NewReader(`{"from":"PC2","files":[{"path":"Album/x.bin","size":5},{"path":"y.bin","size":5}]}`), nil)
	var off struct{ Token string }
	json.NewDecoder(r.Body).Decode(&off)
	if off.Token == "" {
		t.Fatalf("offer not accepted: %d", r.StatusCode)
	}
	send := func(p, body, sum string) int {
		req, _ := http.NewRequest("PUT", srv.URL+"/peer/file?token="+off.Token+"&path="+p, io.NopCloser(strings.NewReader(body)))
		req.ContentLength = -1
		req.Trailer = http.Header{"X-Sha256": {sum}}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	good := sha256.Sum256([]byte("hello"))
	if c := send("Album/x.bin", "hello", hex.EncodeToString(good[:])); c != 200 {
		t.Fatalf("good checksum: got %d", c)
	}
	if b, _ := os.ReadFile(filepath.Join(a.cfg.SaveDir, "Album", "x.bin")); string(b) != "hello" {
		t.Fatalf("folder structure not kept: %q", b)
	}
	if c := send("y.bin", "hellX", hex.EncodeToString(good[:])); c != 422 {
		t.Fatalf("bad checksum: got %d", c)
	}
	if _, err := os.Stat(filepath.Join(a.cfg.SaveDir, "y.bin")); !os.IsNotExist(err) {
		t.Fatal("damaged file was kept")
	}
	if c := send("other.bin", "hello", hex.EncodeToString(good[:])); c != 403 {
		t.Fatalf("file not in offer: got %d", c)
	}
}
