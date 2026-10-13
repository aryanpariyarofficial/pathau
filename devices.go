package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Device is a paired phone, browser or iPhone Shortcut. Pairing is permanent (saved to disk)
// until removed on this PC. Only a SHA-256 of each device's secret is stored, so a copy of
// devices.json can't be used to connect.
type Device struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Kind    string    `json:"kind"` // "browser" or "shortcut"
	UA      string    `json:"ua"`
	IP      string    `json:"ip"`
	Paired  time.Time `json:"paired"`
	Seen    time.Time `json:"seen"`
	Pending bool      `json:"pending"` // waiting for Allow on this PC
}

const pendingFor = 5 * time.Minute

func hashToken(tok string) string { s := sha256.Sum256([]byte(tok)); return hex.EncodeToString(s[:]) }

func (a *App) loadDevices() {
	a.devices = map[string]*Device{}
	if b, err := os.ReadFile(a.devPath); err == nil {
		json.Unmarshal(b, &a.devices)
	}
}

// saveDevices writes paired (not pending) devices; caller holds a.mu.
func (a *App) saveDevices() {
	keep := map[string]*Device{}
	for k, d := range a.devices {
		if !d.Pending {
			keep[k] = d
		}
	}
	b, _ := json.MarshalIndent(keep, "", "  ")
	os.MkdirAll(filepath.Dir(a.devPath), 0o755)
	os.WriteFile(a.devPath, b, 0o600)
}

// token: the browser's cookie, or "Authorization: Bearer <key>" from an iPhone Shortcut.
func token(r *http.Request) string {
	if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	if c, err := r.Cookie("pathau"); err == nil {
		return c.Value
	}
	return ""
}

// device returns the request's paired device (nil if unknown, removed or still pending).
func (a *App) device(r *http.Request) *Device {
	d := a.anyDevice(r)
	if d == nil || d.Pending {
		return nil
	}
	return d
}

func (a *App) anyDevice(r *http.Request) *Device {
	tok := token(r)
	if tok == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	d := a.devices[hashToken(tok)]
	if d != nil {
		d.Seen, d.IP = time.Now(), clientIP(r)
	}
	return d
}

// who names the sender of a request, for text history and transfer rows.
func (a *App) who(r *http.Request) string {
	if isLocal(r) {
		return "This PC"
	}
	if d := a.device(r); d != nil {
		return d.Name
	}
	return deviceLabel(r.UserAgent())
}

// pair checks the passcode and creates a pending device; it becomes usable once Allowed on this PC.
func (a *App) pair(w http.ResponseWriter, r *http.Request, code string) (int, string) {
	ip := clientIP(r)
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.fails[ip]
	if f != nil && time.Now().Before(f.until) {
		return http.StatusTooManyRequests, "Too many wrong passcodes. Try again in a few minutes."
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(code)), []byte(a.pass)) != 1 {
		if f == nil {
			f = &fail{}
			a.fails[ip] = f
		}
		if f.n++; f.n >= maxFails {
			f.n, f.until = 0, time.Now().Add(pauseFor)
		}
		return http.StatusUnauthorized, "Wrong passcode"
	}
	delete(a.fails, ip)
	if a.cfg.Passcode == "" {
		a.pass = randDigits(6) // a code works once: a photo of the old QR is useless
	}
	tok := randHex(32)
	a.devices[hashToken(tok)] = &Device{ID: randHex(4), Name: deviceLabel(r.UserAgent()), Kind: "browser",
		UA: r.UserAgent(), IP: ip, Paired: time.Now(), Seen: time.Now(), Pending: true}
	http.SetCookie(w, &http.Cookie{Name: "pathau", Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: 10 * 365 * 86400})
	go a.alert("pair", deviceLabel(r.UserAgent())+" wants to connect", "Open Pathau and click Allow if this is your phone.", true)
	return http.StatusAccepted, "Waiting for approval on the PC"
}

func (a *App) pairAPI(w http.ResponseWriter, r *http.Request) {
	var req struct{ Code string }
	if readJSON(w, r, &req) != nil {
		return
	}
	code, msg := a.pair(w, r, req.Code)
	http.Error(w, msg, code)
}

// findDevice by public ID; caller holds a.mu.
func (a *App) findDevice(id string) (string, *Device) {
	for k, d := range a.devices {
		if d.ID == id {
			return k, d
		}
	}
	return "", nil
}

func (a *App) answerPair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string
		Allow bool
	}
	if readJSON(w, r, &req) != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if k, d := a.findDevice(req.ID); d != nil && d.Pending {
		if req.Allow {
			d.Pending, d.Paired = false, time.Now()
		} else {
			delete(a.devices, k)
		}
		a.saveDevices()
	}
}

func (a *App) renameDevice(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID, Name string }
	if readJSON(w, r, &req) != nil {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 40 {
		http.Error(w, "Name must be 1 to 40 characters", 400)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, d := a.findDevice(req.ID); d != nil {
		d.Name = name
		a.saveDevices()
	}
}

func (a *App) removeDevice(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID string }
	if readJSON(w, r, &req) != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if k, _ := a.findDevice(req.ID); k != "" {
		delete(a.devices, k)
		a.saveDevices()
	}
}

// newShortcutKey creates a device key for an iPhone Shortcut. The key is shown once.
func (a *App) newShortcutKey(w http.ResponseWriter, r *http.Request) {
	tok := randHex(32)
	a.mu.Lock()
	a.devices[hashToken(tok)] = &Device{ID: randHex(4), Name: "iPhone Shortcut", Kind: "shortcut", Paired: time.Now()}
	a.saveDevices()
	a.mu.Unlock()
	writeJSON(w, map[string]string{"key": tok})
}

// deviceList for the UI, dropping pairing requests nobody answered.
func (a *App) deviceList() []*Device {
	out := []*Device{}
	for k, d := range a.devices {
		if d.Pending && time.Since(d.Paired) > pendingFor {
			delete(a.devices, k)
			continue
		}
		out = append(out, d)
	}
	return out
}

// ---- OTPs ----

var (
	otpWord = regexp.MustCompile(`(?i)\b(otp|code|pin|verification|verify|passcode|one[- ]time)\b|कोड`)
	// a 4-8 digit number standing on its own: "is 482913.", "G-123456", but not "Namaste@2026" or "abc1234"
	otpNum = regexp.MustCompile(`(?:^|[\s:#-])(\d{4,8})(?:$|[\s.,;:!)])`)
)

// extractCode pulls the one-time code out of an SMS like "Your OTP is 482913, valid for 5 min".
// Returns "" when the text isn't an OTP message (or is already just the code).
func extractCode(t string) string {
	t = strings.TrimSpace(t)
	loc := otpWord.FindStringIndex(t)
	if loc == nil || otpNum.MatchString(t) && firstCode(t) == t {
		return ""
	}
	if c := firstCode(t[loc[1]:]); c != "" { // prefer the number after "OTP", not an amount before it
		return c
	}
	return firstCode(t)
}

func firstCode(t string) string {
	if m := otpNum.FindStringSubmatch(t); m != nil {
		return m[1]
	}
	return ""
}
