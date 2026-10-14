// Pathau: send files, folders and text between phones and PCs on the same Wi-Fi/LAN.
// No cloud, no account. One local HTTP server per PC; phones just open it in a browser.
package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/big"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atotto/clipboard"
	"github.com/skip2/go-qrcode"
)

//go:embed web
var webFS embed.FS

var version = "dev" // set by the release build: -X main.version=1.2.3

const (
	discPort = 8766
	maxFails = 10
	pauseFor = 5 * time.Minute
)

type Config struct {
	Name     string   `json:"name"`
	Passcode string   `json:"passcode"` // empty = new random passcode every start
	SaveDir  string   `json:"saveDir"`
	Shares   []string `json:"shares"`
	AutoCopy bool     `json:"autoCopy"`
}

type TextMsg struct {
	From string    `json:"from"`
	Text string    `json:"text"`
	Code string    `json:"code,omitempty"` // OTP found in Text, copied instead of the whole message
	Time time.Time `json:"time"`
}

type fail struct {
	n     int
	until time.Time
}

type sub struct {
	ch    chan []byte
	admin bool
	dev   string // paired device ID, so the PC can show who's online
}

type App struct {
	mu      sync.Mutex
	cfg     Config
	cfgPath string
	outDir  string // files this PC sends to phones
	port    int
	pass    string // active passcode
	id      string // this instance, for discovery
	devPath string
	devices map[string]*Device // sha256(token) -> paired device
	fails   map[string]*fail   // ip -> wrong passcode count
	texts   []TextMsg
	subs    map[*sub]bool
	peers   map[string]*Peer
	offers  map[string]*Offer
	xfers   map[string]*xfer // running transfers that can be paused/canceled
	noted   map[string]time.Time
	quiet   bool // no notifications (tests)
	statN   int  // todayStats cache
	statB   int64
	statsAt time.Time
}

func init() { mime.AddExtensionType(".webmanifest", "application/manifest+json") }

func main() {
	port := 8765
	if p, err := strconv.Atoi(os.Getenv("PATHAU_PORT")); err == nil {
		port = p
	}
	local := fmt.Sprintf("http://localhost:%d", port)
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil { // already running: just show it
		open(local)
		return
	}
	a := newApp(port)
	setupLog(filepath.Dir(a.cfgPath))
	go a.discover()
	log.Printf("Pathau running at %s", local)
	background := len(os.Args) > 1 && os.Args[1] == "--background" // started with Windows
	if !background && os.Getenv("PATHAU_NO_BROWSER") == "" {
		open(local)
	}
	srv := &http.Server{Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() { log.Fatal(srv.Serve(ln)) }()
	runTray(a, local) // blocks; returns only where there's no tray
	select {}
}

func newApp(port int) *App {
	a := &App{port: port, id: randHex(8), fails: map[string]*fail{},
		subs: map[*sub]bool{}, peers: map[string]*Peer{}, offers: map[string]*Offer{}, xfers: map[string]*xfer{}, noted: map[string]time.Time{}}
	dir := os.Getenv("PATHAU_HOME")
	if dir == "" {
		d, _ := os.UserConfigDir()
		dir = filepath.Join(d, "Pathau")
	}
	a.cfgPath = filepath.Join(dir, "config.json")
	a.outDir = filepath.Join(dir, "Outbox")
	a.devPath = filepath.Join(dir, "devices.json")
	a.loadDevices()
	host, _ := os.Hostname()
	home, _ := os.UserHomeDir()
	a.cfg = Config{Name: host, SaveDir: filepath.Join(home, "Downloads", "Pathau"), AutoCopy: true}
	if b, err := os.ReadFile(a.cfgPath); err == nil {
		json.Unmarshal(b, &a.cfg)
	} else if os.Getenv("PATHAU_HOME") == "" { // first run: start with Windows, so phones can always reach this PC
		setAutostart(true)
	}
	if autostart() {
		setAutostart(true) // keep the startup entry pointing at this exe, even if it was moved
	}
	if a.cfg.Shares == nil { // JSON null would break the UI
		a.cfg.Shares = []string{}
	}
	a.texts = []TextMsg{}
	a.pass = a.cfg.Passcode
	if a.pass == "" {
		a.pass = randDigits(6)
	}
	return a
}

// setupLog also writes the log to pathau.log in the config folder (the tray app has no
// console), starting fresh once it passes 2 MB.
func setupLog(dir string) {
	p := filepath.Join(dir, "pathau.log")
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if st, err := os.Stat(p); err == nil && st.Size() > 2<<20 {
		flags |= os.O_TRUNC
	}
	os.MkdirAll(dir, 0o755)
	if f, err := os.OpenFile(p, flags, 0o600); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}
}

// saveCfg writes the config; caller holds a.mu.
func (a *App) saveCfg() error {
	if err := os.MkdirAll(filepath.Dir(a.cfgPath), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(a.cfg, "", "  ")
	return os.WriteFile(a.cfgPath, b, 0o600)
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	web, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(web))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if k := r.URL.Query().Get("k"); k != "" && !isLocal(r) { // QR code login
			a.pair(w, r, k)
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		http.ServeFileFS(w, r, web, "index.html")
	})
	mux.HandleFunc("POST /api/login", a.pairAPI)
	mux.HandleFunc("GET /api/me", a.me)

	user, admin := a.guard(false), a.guard(true)
	mux.HandleFunc("GET /api/events", user(a.events))
	mux.HandleFunc("GET /api/texts", user(a.getTexts))
	mux.HandleFunc("POST /api/text", user(a.postText))
	mux.HandleFunc("PUT /api/upload", user(a.upload))
	mux.HandleFunc("POST /api/xfer", user(a.control))
	mux.HandleFunc("GET /api/shares", user(a.listShares))
	mux.HandleFunc("GET /api/ls", user(a.ls))
	mux.HandleFunc("GET /api/dl", user(a.dl))

	mux.HandleFunc("GET /api/state", admin(a.state))
	mux.HandleFunc("GET /api/qr", admin(a.qr))
	mux.HandleFunc("GET /api/clipboard", admin(a.readClipboard))
	mux.HandleFunc("POST /api/settings", admin(a.settings))
	mux.HandleFunc("POST /api/shares/add", admin(a.addShare))
	mux.HandleFunc("POST /api/pick-save-dir", admin(a.pickSaveDir))
	mux.HandleFunc("PUT /api/outbox", admin(a.outboxAdd))
	mux.HandleFunc("POST /api/outbox/remove", admin(a.outboxRemove))
	mux.HandleFunc("POST /api/shares/remove", admin(a.removeShare))
	mux.HandleFunc("POST /api/devices/answer", admin(a.answerPair))
	mux.HandleFunc("POST /api/devices/rename", admin(a.renameDevice))
	mux.HandleFunc("POST /api/devices/remove", admin(a.removeDevice))
	mux.HandleFunc("POST /api/devices/shortcut", admin(a.newShortcutKey))
	mux.HandleFunc("POST /api/autostart", admin(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ On bool }
		if readJSON(w, r, &req) == nil {
			if err := setAutostart(req.On); err != nil {
				http.Error(w, err.Error(), 500)
			}
		}
	}))
	mux.HandleFunc("POST /api/open-file", admin(a.openFile))
	mux.HandleFunc("POST /api/quit", admin(func(w http.ResponseWriter, r *http.Request) {
		go func() { time.Sleep(200 * time.Millisecond); os.Exit(0) }()
	}))
	mux.HandleFunc("POST /api/connect", admin(a.connect))
	mux.HandleFunc("POST /api/send/offer", admin(a.sendOffer))
	mux.HandleFunc("PUT /api/send/file", admin(a.sendFile))
	mux.HandleFunc("POST /api/send/text", admin(a.sendText))
	mux.HandleFunc("POST /api/offer/answer", admin(a.answerOffer))

	// PC-to-PC: open to the LAN, every transfer still needs the receiver to click Accept.
	mux.HandleFunc("GET /peer/hello", a.peerHello)
	mux.HandleFunc("POST /peer/offer", a.peerOffer)
	mux.HandleFunc("PUT /peer/file", a.peerFile)
	mux.HandleFunc("POST /peer/text", a.peerText)
	return mux
}

// ---- auth ----

// isLocal: the request comes from this PC's own browser. The Host check stops DNS-rebinding sites.
func isLocal(r *http.Request) bool {
	ip := net.ParseIP(clientIP(r))
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	h := r.Host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func clientIP(r *http.Request) string {
	h, _, _ := net.SplitHostPort(r.RemoteAddr)
	return h
}

// guard: admin = this PC only; user = this PC or a paired device.
// Cookie writes need the X-Pathau header, which other websites cannot send without a CORS preflight
// we never allow. Bearer keys (iPhone Shortcuts) are never sent automatically, so they don't need it.
func (a *App) guard(adminOnly bool) func(http.HandlerFunc) http.HandlerFunc {
	return func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			bearer := strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
			if r.Method != http.MethodGet && r.Header.Get("X-Pathau") == "" && !bearer {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if !isLocal(r) && (adminOnly || a.device(r) == nil) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
}

func (a *App) me(w http.ResponseWriter, r *http.Request) {
	admin := isLocal(r)
	a.mu.Lock()
	name := a.cfg.Name
	a.mu.Unlock()
	d := a.anyDevice(r)
	writeJSON(w, map[string]any{"admin": admin, "authed": admin || (d != nil && !d.Pending),
		"pending": d != nil && d.Pending, "name": name})
}

// ---- live events (Server-Sent Events) ----

func (a *App) publish(adminOnly bool, typ string, v any) {
	b, _ := json.Marshal(v)
	msg := []byte("event: " + typ + "\ndata: " + string(b) + "\n\n")
	a.mu.Lock()
	defer a.mu.Unlock()
	for s := range a.subs {
		if adminOnly && !s.admin {
			continue
		}
		select {
		case s.ch <- msg:
		default: // slow client, drop
		}
	}
}

func (a *App) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	s := &sub{ch: make(chan []byte, 256), admin: isLocal(r)}
	if d := a.device(r); d != nil {
		s.dev = d.ID
	}
	a.mu.Lock()
	a.subs[s] = true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.subs, s); a.mu.Unlock() }()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	fl.Flush()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case m := <-s.ch:
			if !s.admin && a.device(r) == nil { // removed on this PC
				return
			}
			w.Write(m)
		case <-t.C:
			w.Write([]byte(": ping\n\n"))
		}
		fl.Flush()
	}
}

// alert tells the person at this PC about something that happened while they may not be looking.
// Important events (a phone asking to pair, a PC offering files) always notify, and open Pathau
// if no Pathau window is open; the rest notify only when no window is open. Repeats are throttled.
func (a *App) alert(kind, title, text string, important bool) {
	a.mu.Lock()
	watching := false
	for s := range a.subs {
		watching = watching || s.admin
	}
	skip := a.quiet || time.Since(a.noted[kind]) < 4*time.Second || (watching && !important)
	if !skip {
		a.noted[kind] = time.Now()
	}
	a.mu.Unlock()
	if skip {
		return
	}
	notify(title, text)
	if important && !watching {
		open(fmt.Sprintf("http://localhost:%d", a.port))
	}
}

// ---- text ----

func (a *App) addText(from, text string, incoming bool) {
	m := TextMsg{From: from, Text: text, Code: extractCode(text), Time: time.Now()}
	a.mu.Lock()
	a.texts = append(a.texts, m)
	if len(a.texts) > 200 {
		a.texts = a.texts[len(a.texts)-200:]
	}
	auto := a.cfg.AutoCopy
	a.mu.Unlock()
	a.publish(false, "text", m)
	if incoming && auto {
		if m.Code != "" {
			text = m.Code
		}
		clipboard.WriteAll(text)
	}
	if incoming {
		note := text
		if m.Code != "" {
			note = "Code " + m.Code
		}
		if len(note) > 80 {
			note = note[:80] + "…"
		}
		if auto {
			note += "  (copied, press Ctrl+V)"
		}
		a.alert("text", "Text from "+from, note, false)
	}
}

func (a *App) getTexts(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	writeJSON(w, a.texts)
}

func (a *App) postText(w http.ResponseWriter, r *http.Request) {
	var req struct{ Text string }
	if readJSON(w, r, &req) != nil || strings.TrimSpace(req.Text) == "" {
		return
	}
	// only this PC and paired devices reach here, so auto-copy is safe
	a.addText(a.who(r), req.Text, !isLocal(r))
}

func (a *App) readClipboard(w http.ResponseWriter, r *http.Request) {
	t, _ := clipboard.ReadAll()
	writeJSON(w, map[string]string{"text": t})
}

// ---- admin state & settings ----

func (a *App) state(w http.ResponseWriter, r *http.Request) {
	recvN, recvB := a.todayStats()
	outN := 0
	if ents, err := os.ReadDir(a.outDir); err == nil {
		outN = len(ents)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	online := []string{}
	for s := range a.subs {
		if s.dev != "" {
			online = append(online, s.dev)
		}
	}
	peers := []*Peer{}
	for id, p := range a.peers {
		if !p.Manual && time.Since(p.Seen) > 10*time.Second {
			delete(a.peers, id)
			continue
		}
		peers = append(peers, p)
	}
	offers := []map[string]any{}
	for _, o := range a.offers {
		if o.token == "" {
			offers = append(offers, map[string]any{"id": o.ID, "from": o.From, "count": len(o.Files), "total": o.total})
		}
	}
	writeJSON(w, map[string]any{"name": a.cfg.Name, "passcode": a.pass, "fixedPass": a.cfg.Passcode != "",
		"port": a.port, "ips": lanIPs(), "saveDir": a.cfg.SaveDir, "shares": a.cfg.Shares,
		"autoCopy": a.cfg.AutoCopy, "peers": peers, "devices": a.deviceList(), "offers": offers,
		"autostart": autostart(), "canAutostart": runtime.GOOS == "windows", "version": version,
		"online": online, "stats": map[string]any{"recvToday": recvN, "recvTodayBytes": recvB, "outbox": outN}})
}

func (a *App) qr(w http.ResponseWriter, r *http.Request) {
	ip := net.ParseIP(r.URL.Query().Get("ip"))
	if ip == nil {
		http.Error(w, "bad ip", 400)
		return
	}
	a.mu.Lock()
	url := fmt.Sprintf("http://%s/?k=%s", net.JoinHostPort(ip.String(), strconv.Itoa(a.port)), a.pass)
	a.mu.Unlock()
	png, err := qrcode.Encode(url, qrcode.Medium, 512)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(png)
}

func (a *App) settings(w http.ResponseWriter, r *http.Request) {
	var req Config
	if readJSON(w, r, &req) != nil {
		return
	}
	req.Passcode = strings.TrimSpace(req.Passcode)
	if req.Passcode != "" && len(req.Passcode) < 4 {
		http.Error(w, "Passcode must be at least 4 characters", 400)
		return
	}
	if !filepath.IsAbs(req.SaveDir) {
		http.Error(w, "Save folder must be a full path, like C:\\Users\\you\\Downloads\\Pathau", 400)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if name := strings.TrimSpace(req.Name); name != "" {
		a.cfg.Name = name
	}
	if req.Passcode != a.cfg.Passcode {
		a.cfg.Passcode, a.pass = req.Passcode, req.Passcode
		if a.pass == "" {
			a.pass = randDigits(6)
		}
	}
	a.cfg.SaveDir, a.cfg.AutoCopy = filepath.Clean(req.SaveDir), req.AutoCopy
	if err := a.saveCfg(); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

// todayStats counts files received today (any subfolder), cached for 10 s.
// ponytail: walks the save folder; stops after 50k entries so a huge folder stays fast.
func (a *App) todayStats() (int, int64) {
	a.mu.Lock()
	dir, cached := a.cfg.SaveDir, time.Since(a.statsAt) < 10*time.Second
	n, b := a.statN, a.statB
	a.mu.Unlock()
	if cached {
		return n, b
	}
	n, b = 0, 0
	y, m, d := time.Now().Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	seen := 0
	filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if seen++; err != nil || seen > 50000 {
			return filepath.SkipAll
		}
		if e.Type().IsRegular() {
			if info, err := e.Info(); err == nil && !info.ModTime().Before(midnight) {
				n++
				b += info.Size()
			}
		}
		return nil
	})
	a.mu.Lock()
	a.statN, a.statB, a.statsAt = n, b, time.Now()
	a.mu.Unlock()
	return n, b
}

// ---- helpers ----

func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(v)
	if err != nil {
		http.Error(w, "bad request", 400)
	}
	return err
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func randDigits(n int) string {
	s := ""
	for range n {
		d, _ := rand.Int(rand.Reader, big.NewInt(10))
		s += d.String()
	}
	return s
}

func deviceLabel(ua string) string {
	for _, k := range []string{"iPhone", "iPad", "Android", "Windows", "Macintosh", "Linux"} {
		if strings.Contains(ua, k) {
			return strings.Replace(k, "Macintosh", "Mac", 1)
		}
	}
	return "Device"
}

// lanNets lists private IPv4 networks, real adapters first and virtual ones (Hyper-V, VMware...) last.
func lanNets() []*net.IPNet {
	var good, virt []*net.IPNet
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, ad := range addrs {
			n, ok := ad.(*net.IPNet)
			if !ok || n.IP.To4() == nil || !n.IP.IsPrivate() {
				continue
			}
			name := strings.ToLower(ifc.Name)
			if strings.Contains(name, "virtual") || strings.Contains(name, "vethernet") ||
				strings.Contains(name, "vmware") || strings.Contains(name, "wsl") {
				virt = append(virt, n)
			} else {
				good = append(good, n)
			}
		}
	}
	return append(good, virt...)
}

func lanIPs() []string {
	ips := []string{}
	for _, n := range lanNets() {
		ips = append(ips, n.IP.To4().String())
	}
	return ips
}

// open shows a URL or folder with the OS default handler.
func open(target string) {
	switch runtime.GOOS {
	case "windows":
		if strings.HasPrefix(target, "http") {
			exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
		} else {
			exec.Command("explorer", target).Start()
		}
	case "darwin":
		exec.Command("open", target).Start()
	default:
		exec.Command("xdg-open", target).Start()
	}
}
