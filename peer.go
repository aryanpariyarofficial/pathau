package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Peer struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Addr   string    `json:"addr"` // ip:port
	Manual bool      `json:"manual"`
	Seen   time.Time `json:"-"`
}

type hello struct {
	App  string `json:"app"`
	ID   string `json:"id"`
	Name string `json:"name"`
	Port int    `json:"port"`
}

type OfferFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type Offer struct {
	ID     string
	From   string
	Files  []OfferFile
	total  int64
	answer chan bool
	token  string           // set once accepted
	left   map[string]int64 // files still to receive -> size
	exp    time.Time
}

var client = &http.Client{}

// discover announces this PC by UDP broadcast every 3s and listens for other Pathau PCs.
func (a *App) discover() {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: discPort})
	if err != nil {
		log.Printf("auto-discovery off (%v); use Connect by IP", err)
		return
	}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				time.Sleep(time.Second)
				continue
			}
			var h hello
			if json.Unmarshal(buf[:n], &h) != nil || h.App != "pathau" || h.ID == a.id || h.Port <= 0 || h.Port > 65535 {
				continue
			}
			a.addPeer(h, net.JoinHostPort(src.IP.String(), strconv.Itoa(h.Port)), false)
		}
	}()
	for {
		a.mu.Lock()
		msg, _ := json.Marshal(hello{"pathau", a.id, a.cfg.Name, a.port})
		a.mu.Unlock()
		for _, b := range broadcastAddrs() {
			conn.WriteToUDP(msg, &net.UDPAddr{IP: b, Port: discPort})
		}
		time.Sleep(3 * time.Second)
	}
}

func (a *App) addPeer(h hello, addr string, manual bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if old := a.peers[h.ID]; old != nil && old.Manual {
		manual = true
	}
	a.peers[h.ID] = &Peer{ID: h.ID, Name: h.Name, Addr: addr, Manual: manual, Seen: time.Now()}
}

func broadcastAddrs() []net.IP {
	out := []net.IP{net.IPv4bcast}
	for _, n := range lanNets() {
		ip, mask := n.IP.To4(), n.Mask
		if len(mask) == 16 {
			mask = mask[12:]
		}
		b := make(net.IP, 4)
		for i := range b {
			b[i] = ip[i] | ^mask[i]
		}
		out = append(out, b)
	}
	return out
}

func (a *App) peerHello(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	writeJSON(w, hello{"pathau", a.id, a.cfg.Name, a.port})
}

// connect adds a PC by IP when broadcast discovery is blocked (some routers, guest networks).
func (a *App) connect(w http.ResponseWriter, r *http.Request) {
	var req struct{ Addr string }
	if readJSON(w, r, &req) != nil {
		return
	}
	addr := strings.TrimSpace(req.Addr)
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "8765")
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/peer/hello")
	if err != nil {
		http.Error(w, "No Pathau found at "+addr, 502)
		return
	}
	defer resp.Body.Close()
	var h hello
	if json.NewDecoder(resp.Body).Decode(&h) != nil || h.App != "pathau" {
		http.Error(w, "No Pathau found at "+addr, 502)
		return
	}
	a.addPeer(h, addr, true)
	writeJSON(w, h)
}

// ---- receiving side ----

// peerOffer waits (up to 2 min) for the user here to Accept, then hands back a one-time upload token.
func (a *App) peerOffer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From  string
		Files []OfferFile
	}
	if readJSON(w, r, &req) != nil {
		return
	}
	o := &Offer{ID: randHex(6), From: req.From, Files: req.Files, answer: make(chan bool, 1), left: map[string]int64{}}
	if len(o.From) > 64 {
		o.From = o.From[:64]
	}
	for _, f := range req.Files {
		if cleanRel(f.Path) == "" {
			http.Error(w, "bad file name: "+f.Path, 400)
			return
		}
		o.left[f.Path] = f.Size
		o.total += f.Size
	}
	if len(o.left) == 0 {
		http.Error(w, "no files", 400)
		return
	}
	a.mu.Lock()
	a.offers[o.ID] = o
	a.mu.Unlock()

	ok := false
	select {
	case ok = <-o.answer:
	case <-time.After(2 * time.Minute):
	case <-r.Context().Done():
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !ok {
		delete(a.offers, o.ID)
		http.Error(w, "declined", 403)
		return
	}
	o.token, o.exp = randHex(16), time.Now().Add(24*time.Hour)
	writeJSON(w, map[string]string{"token": o.token})
}

func (a *App) answerOffer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string
		Accept bool
	}
	if readJSON(w, r, &req) != nil {
		return
	}
	a.mu.Lock()
	o := a.offers[req.ID]
	a.mu.Unlock()
	if o != nil {
		select {
		case o.answer <- req.Accept:
		default:
		}
	}
}

// peerFile receives one file of an accepted offer; the sender's SHA-256 arrives as an HTTP trailer.
func (a *App) peerFile(w http.ResponseWriter, r *http.Request) {
	tok, p := r.URL.Query().Get("token"), r.URL.Query().Get("path")
	var o *Offer
	var size int64
	a.mu.Lock()
	for _, x := range a.offers {
		if x.token != "" && subtle.ConstantTimeCompare([]byte(x.token), []byte(tok)) == 1 && time.Now().Before(x.exp) {
			if s, ok := x.left[p]; ok {
				o, size = x, s
			}
		}
	}
	a.mu.Unlock()
	if o == nil {
		http.Error(w, "not allowed", 403)
		return
	}
	a.mu.Lock()
	dir := a.cfg.SaveDir
	a.mu.Unlock()
	x := a.newXfer("", "")
	defer a.endXfer(x)
	name, err := a.saveStream(dir, p, r.Body, size, o.From, x, func() string { return r.Trailer.Get("X-Sha256") })
	if err != nil {
		httpErr(w, err)
		return
	}
	a.mu.Lock()
	delete(o.left, p)
	if len(o.left) == 0 {
		delete(a.offers, o.ID)
	}
	a.mu.Unlock()
	writeJSON(w, map[string]string{"name": name})
}

// peerText: text from another PC is shown, but not auto-copied: anyone on the Wi-Fi could send it,
// and silently replacing the clipboard is how paste-jacking works. Phones (passcode) do auto-copy.
func (a *App) peerText(w http.ResponseWriter, r *http.Request) {
	var req struct{ From, Text string }
	if readJSON(w, r, &req) != nil || strings.TrimSpace(req.Text) == "" {
		return
	}
	if len(req.From) > 64 {
		req.From = req.From[:64]
	}
	a.addText(req.From, req.Text, false)
}

// ---- sending side (this PC's browser -> this server -> the other PC) ----

func (a *App) sendOffer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Peer  string
		Files []OfferFile
	}
	if readJSON(w, r, &req) != nil {
		return
	}
	a.mu.Lock()
	body, _ := json.Marshal(map[string]any{"from": a.cfg.Name, "files": req.Files})
	a.mu.Unlock()
	out, _ := http.NewRequestWithContext(r.Context(), "POST", "http://"+req.Peer+"/peer/offer", bytes.NewReader(body))
	relay(w, out)
}

func (a *App) sendFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h := sha256.New()
	target := "http://" + q.Get("peer") + "/peer/file?" + url.Values{"token": {q.Get("token")}, "path": {q.Get("path")}}.Encode()
	out, err := http.NewRequestWithContext(r.Context(), "PUT", target, nil)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	out.ContentLength = -1 // chunked, so the trailer can follow the body
	out.Trailer = http.Header{"X-Sha256": nil}
	x := a.reqXfer(r, "") // lets this PC pause/cancel what it's sending
	defer a.endXfer(x)
	out.Body = io.NopCloser(&sumReader{r: io.TeeReader(gate{x, r.Body}, h), done: func() {
		out.Trailer.Set("X-Sha256", hex.EncodeToString(h.Sum(nil)))
	}})
	relay(w, out)
}

func (a *App) sendText(w http.ResponseWriter, r *http.Request) {
	var req struct{ Peer, To, Text string }
	if readJSON(w, r, &req) != nil {
		return
	}
	a.mu.Lock()
	body, _ := json.Marshal(map[string]string{"from": a.cfg.Name, "text": req.Text})
	a.mu.Unlock()
	out, err := http.NewRequestWithContext(r.Context(), "POST", "http://"+req.Peer+"/peer/text", bytes.NewReader(body))
	if err != nil {
		http.Error(w, "bad peer address", 400)
		return
	}
	out.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(out)
	if err != nil {
		http.Error(w, "Could not reach "+req.To, 502)
		return
	}
	resp.Body.Close()
	a.addText("This PC → "+req.To, req.Text, false)
}

// relay sends out and copies the peer's answer back to our browser.
func relay(w http.ResponseWriter, out *http.Request) {
	if out == nil {
		http.Error(w, "bad peer address", 400)
		return
	}
	resp, err := client.Do(out)
	if err != nil {
		http.Error(w, "Could not reach the other PC: "+err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// sumReader fills the checksum trailer the moment the body ends.
type sumReader struct {
	r    io.Reader
	done func()
}

func (s *sumReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err == io.EOF {
		s.done()
	}
	return n, err
}
