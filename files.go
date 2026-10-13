package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var (
	errBadPath  = errors.New("bad file name")
	errChecksum = errors.New("file was damaged in transfer (checksum mismatch), please send again")
)

// cleanRel turns a client-supplied relative path ("Photos/a.jpg") into a safe local path, or "".
func cleanRel(p string) string {
	p = path.Clean(strings.ReplaceAll(p, "\\", "/"))
	if p == "." || !fs.ValidPath(p) {
		return ""
	}
	if osp := filepath.FromSlash(p); filepath.IsLocal(osp) { // also rejects C:, CON, NUL...
		return osp
	}
	return ""
}

// File sources. Every place files live is addressed by ?s=:
//
//	"recv"  received files (SaveDir)          this PC only
//	"out"   files this PC sent to phones       everyone with the passcode
//	"0".."n" shared folders                    everyone with the passcode
func (a *App) sourceDir(s string, local bool) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if i, err := strconv.Atoi(s); err == nil && i >= 0 && i < len(a.cfg.Shares) {
		return a.cfg.Shares[i]
	}
	switch {
	case s == "out":
		return a.outDir
	case s == "recv" && local:
		return a.cfg.SaveDir
	}
	return ""
}

// saveStream writes body to rel inside dir, never overwriting (a clash becomes "name (1).ext").
// wantSum, if set, is called after the body is fully read and must return the expected SHA-256 hex.
// On any failure the partial file is deleted, so a broken copy is never kept.
// from != "" reports live progress to this PC's UI.
func (a *App) saveStream(dir, rel string, body io.Reader, total int64, from string, wantSum func() string) (string, error) {
	rel = cleanRel(rel)
	if rel == "" {
		return "", errBadPath
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(dir) // os.Root: no escaping the folder, even via symlinks
	if err != nil {
		return "", err
	}
	defer root.Close()
	if d := filepath.Dir(rel); d != "." {
		if err := root.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
	}
	f, name, err := createUnique(root, rel)
	if err != nil {
		return "", err
	}
	pw := &progress{a: a, id: randHex(4), name: filepath.ToSlash(name), from: from, total: total}
	h := sha256.New()
	_, err = io.CopyBuffer(io.MultiWriter(f, h, pw), body, make([]byte, 1<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && wantSum != nil && !strings.EqualFold(wantSum(), hex.EncodeToString(h.Sum(nil))) {
		err = errChecksum
	}
	if err != nil {
		root.Remove(name)
		pw.send(err.Error())
		return "", err
	}
	pw.send("done")
	return filepath.ToSlash(name), nil
}

func createUnique(root *os.Root, p string) (*os.File, string, error) {
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 0; i < 10000; i++ {
		name := p
		if i > 0 {
			name = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return f, name, err
	}
	return nil, "", errors.New("too many files with this name")
}

// progress reports receive progress to this PC's UI, at most ~3 times a second.
type progress struct {
	a              *App
	id, name, from string
	total, done    int64
	last           time.Time
}

func (p *progress) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if time.Since(p.last) > 300*time.Millisecond {
		p.last = time.Now()
		p.send("")
	}
	return len(b), nil
}

// send: state "" = in progress, "done", or an error message.
func (p *progress) send(state string) {
	if p.from == "" {
		return
	}
	p.a.publish(true, "rx", map[string]any{"id": p.id, "name": p.name, "from": p.from,
		"done": p.done, "total": p.total, "state": state})
}

func httpErr(w http.ResponseWriter, err error) {
	code := 500
	switch err {
	case errBadPath:
		code = 400
	case errChecksum:
		code = 422
	}
	http.Error(w, err.Error(), code)
}

// upload: phone (or any browser with the passcode) -> this PC.
func (a *App) upload(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	dir := a.cfg.SaveDir
	a.mu.Unlock()
	name, err := a.saveStream(dir, r.URL.Query().Get("name"), r.Body, r.ContentLength, deviceLabel(r.UserAgent()), nil)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"name": name})
}

// ---- send to phones (outbox) ----

// outboxAdd: this PC drops/picks files to send to phones. The browser can't hand us file paths,
// so files are copied into the outbox; phones get notified and download from there.
// ponytail: copies the file (fast over loopback); a native file picker could share by path instead.
func (a *App) outboxAdd(w http.ResponseWriter, r *http.Request) {
	name, err := a.saveStream(a.outDir, r.URL.Query().Get("name"), r.Body, r.ContentLength, "", nil)
	if err != nil {
		httpErr(w, err)
		return
	}
	a.publish(false, "outbox", map[string]string{"name": name})
	writeJSON(w, map[string]string{"name": name})
}

// outboxRemove deletes one item, or everything when p is empty.
func (a *App) outboxRemove(w http.ResponseWriter, r *http.Request) {
	var req struct{ P string }
	if readJSON(w, r, &req) != nil {
		return
	}
	if req.P == "" {
		os.RemoveAll(a.outDir)
		os.MkdirAll(a.outDir, 0o755)
	} else if rel := cleanRel(req.P); rel == "" {
		httpErr(w, errBadPath)
		return
	} else if root, err := os.OpenRoot(a.outDir); err == nil {
		root.RemoveAll(rel)
		root.Close()
	}
	a.publish(false, "outbox", map[string]string{})
}

// ---- browsing & downloading ----

func (a *App) listShares(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []map[string]any{}
	for i, s := range a.cfg.Shares {
		out = append(out, map[string]any{"i": i, "name": filepath.Base(s), "path": s})
	}
	writeJSON(w, out)
}

// openSource opens source ?s= and validates sub path ?p= (slash-separated, "." = top).
func (a *App) openSource(r *http.Request) (*os.Root, string, string, error) {
	dir := a.sourceDir(r.URL.Query().Get("s"), isLocal(r))
	if dir == "" {
		return nil, "", "", errBadPath
	}
	p := strings.Trim(r.URL.Query().Get("p"), "/")
	if p == "" {
		p = "."
	}
	if !fs.ValidPath(p) {
		return nil, "", "", errBadPath
	}
	os.MkdirAll(dir, 0o755) // recv/out may not exist yet
	root, err := os.OpenRoot(dir)
	return root, p, filepath.Base(dir), err
}

func (a *App) ls(w http.ResponseWriter, r *http.Request) {
	root, p, _, err := a.openSource(r)
	if err != nil {
		httpErr(w, err)
		return
	}
	defer root.Close()
	ents, err := fs.ReadDir(root.FS(), p)
	if err != nil {
		http.Error(w, "folder not found", 404)
		return
	}
	type ent struct {
		Name string    `json:"name"`
		Dir  bool      `json:"dir"`
		Size int64     `json:"size"`
		Mod  time.Time `json:"mod"`
	}
	out := []ent{}
	for _, e := range ents {
		if info, err := e.Info(); err == nil && (e.IsDir() || e.Type().IsRegular()) {
			out = append(out, ent{e.Name(), e.IsDir(), info.Size(), info.ModTime()})
		}
	}
	writeJSON(w, out) // the UI sorts
}

// dl downloads a file (with resume support via Range) or a whole folder as a .zip.
func (a *App) dl(w http.ResponseWriter, r *http.Request) {
	root, p, srcName, err := a.openSource(r)
	if err != nil {
		httpErr(w, err)
		return
	}
	defer root.Close()
	fsys := root.FS()
	st, err := fs.Stat(fsys, p)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	name := st.Name()
	if p == "." {
		name = srcName
	}
	if st.IsDir() {
		zipDir(w, fsys, p, name)
		return
	}
	f, err := fsys.Open(p)
	if err != nil {
		http.Error(w, "cannot open", 500)
		return
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "cannot read", 500)
		return
	}
	disp := "attachment"
	if r.URL.Query().Get("inline") == "1" && previewable(name) {
		disp = "inline" // for the preview / thumbnails
	}
	w.Header().Set("Content-Disposition", disp+"; filename*=UTF-8''"+url.PathEscape(name))
	// a shared .html/.svg must never run script on our origin
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, st.ModTime(), rs)
}

func previewable(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".avif",
		".mp4", ".webm", ".mov", ".m4v", ".mp3", ".m4a", ".wav", ".ogg", ".aac", ".flac":
		return true
	}
	return false
}

func zipDir(w http.ResponseWriter, fsys fs.FS, dir, name string) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name+".zip"))
	zw := zip.NewWriter(w)
	defer zw.Close()
	fs.WalkDir(fsys, dir, func(fp string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil // skip unreadable entries, folders and links
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		f, err := fsys.Open(fp)
		if err != nil {
			return nil
		}
		defer f.Close()
		hdr, _ := zip.FileInfoHeader(info)
		hdr.Name = strings.TrimPrefix(fp, dir+"/")
		hdr.Method = zip.Store // photos/videos are already compressed; storing is much faster
		zf, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = io.Copy(zf, f)
		return err
	})
}

// openFile opens a file from any source with its default app, or shows it in Explorer.
func (a *App) openFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		S, P   string
		Reveal bool
	}
	if readJSON(w, r, &req) != nil {
		return
	}
	dir, rel := a.sourceDir(req.S, true), cleanRel(req.P)
	if req.P == "" {
		rel = "."
	}
	full := filepath.Join(dir, rel)
	st, err := os.Stat(full)
	if dir == "" || rel == "" || err != nil {
		http.Error(w, "file not found", 404)
		return
	}
	ext := strings.ToLower(filepath.Ext(full))
	switch {
	case st.IsDir():
		showInExplorer(full, "")
	case req.Reveal || strings.Contains(".exe.msi.bat.cmd.ps1.vbs.js.jse.wsf.hta.scr.com.lnk.reg.", ext+"."):
		showInExplorer(filepath.Dir(full), filepath.Base(full)) // never launch programs from another device with one click
	default:
		open(full)
	}
}

// ---- shared folders ----

func (a *App) addShareDir(dir string) error {
	dir, err := filepath.Abs(strings.Trim(strings.TrimSpace(dir), `"`))
	if err != nil {
		return err
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return errors.New("folder not found: " + dir)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.cfg.Shares {
		if strings.EqualFold(s, dir) {
			return nil
		}
	}
	a.cfg.Shares = append(a.cfg.Shares, dir)
	return a.saveCfg()
}

func (a *App) addShare(w http.ResponseWriter, r *http.Request) {
	var req struct{ Path string }
	if readJSON(w, r, &req) != nil {
		return
	}
	if req.Path == "" { // no path: ask with the folder dialog
		var err error
		if req.Path, err = pickFolder("Choose a folder to share with your phones", "Share this folder"); err != nil || req.Path == "" {
			return // cancelled
		}
	}
	if err := a.addShareDir(req.Path); err != nil {
		http.Error(w, err.Error(), 400)
	}
}

func (a *App) removeShare(w http.ResponseWriter, r *http.Request) {
	var req struct{ I int }
	if readJSON(w, r, &req) != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if req.I >= 0 && req.I < len(a.cfg.Shares) {
		a.cfg.Shares = append(a.cfg.Shares[:req.I], a.cfg.Shares[req.I+1:]...)
		a.saveCfg()
	}
}

// pickSaveDir lets the user choose where received files go, and saves it.
func (a *App) pickSaveDir(w http.ResponseWriter, r *http.Request) {
	dir, err := pickFolder("Choose where to save received files", "Save files here")
	if err != nil || dir == "" {
		writeJSON(w, map[string]string{})
		return
	}
	a.mu.Lock()
	a.cfg.SaveDir = filepath.Clean(dir)
	a.saveCfg()
	a.mu.Unlock()
	writeJSON(w, map[string]string{"path": dir})
}

// ---- Windows shell helpers ----

func powershell(script string, env ...string) *exec.Cmd {
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-Command", script)
	cmd.Env = append(os.Environ(), env...)
	hideWindow(cmd)
	return cmd
}

// pickFolder shows the modern Windows "Select Folder" dialog, on top of the browser.
// ponytail: compiled by PowerShell on each use (~1s); move to Win32 syscalls if that's ever too slow.
func pickFolder(title, okLabel string) (string, error) {
	if runtime.GOOS != "windows" {
		return "", errors.New("type the folder path instead")
	}
	out, err := powershell(folderPickerPS, "PATHAU_TITLE="+title, "PATHAU_OK="+okLabel).Output()
	return strings.TrimSpace(string(out)), err
}

// showInExplorer navigates an already-open Explorer window to dir (selecting file, if given)
// instead of opening a new window every time. Falls back to a new window.
func showInExplorer(dir, file string) {
	if runtime.GOOS != "windows" {
		open(dir)
		return
	}
	powershell(explorerPS, "PATHAU_DIR="+dir, "PATHAU_FILE="+file).Start()
}

const explorerPS = `
$dir = $env:PATHAU_DIR; $file = $env:PATHAU_FILE
$w = @((New-Object -ComObject Shell.Application).Windows()) | Where-Object { $_.FullName -like '*\explorer.exe' } | Select-Object -First 1
if (-not $w) {
  if ($file) { Start-Process explorer.exe -ArgumentList ('/select,"' + (Join-Path $dir $file) + '"') }
  else { Start-Process explorer.exe -ArgumentList ('"' + $dir + '"') }
  exit
}
$w.Navigate2($dir)
for ($i = 0; $i -lt 50; $i++) {
  Start-Sleep -Milliseconds 100
  try { if ($w.ReadyState -eq 4 -and $w.Document.Folder.Self.Path -eq $dir) { break } } catch {}
}
if ($file) { try { $it = $w.Document.Folder.ParseName($file); if ($it) { $w.Document.SelectItem($it, 29) } } catch {} }
Add-Type -Name W -Namespace P -MemberDefinition @'
[DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
[DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr h, int c);
[DllImport("user32.dll")] public static extern bool IsIconic(IntPtr h);
[DllImport("user32.dll")] public static extern void keybd_event(byte k, byte s, uint f, UIntPtr e);
'@
$h = [IntPtr]$w.HWND
if ([P.W]::IsIconic($h)) { [P.W]::ShowWindow($h, 9) | Out-Null }
[P.W]::keybd_event(0x12, 0, 0, [UIntPtr]::Zero); [P.W]::keybd_event(0x12, 0, 2, [UIntPtr]::Zero) # an Alt tap lets us bring it to front
[P.W]::SetForegroundWindow($h) | Out-Null
`

const folderPickerPS = `
Add-Type -TypeDefinition @'
using System; using System.Runtime.InteropServices;
public static class Picker {
  [ComImport, Guid("DC1C5A9C-E88A-4dde-A5A1-60F82A20AEF7")] class FileOpenDialog {}
  [ComImport, Guid("43826D1E-E718-42EE-BC55-A1E261C37BFE"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
  interface IShellItem { void BindToHandler(); void GetParent(); [PreserveSig] int GetDisplayName(uint sigdn, out IntPtr name); }
  [ComImport, Guid("42f85136-db7e-439c-85f1-e4075d135fc8"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
  interface IFileOpenDialog {
    [PreserveSig] int Show(IntPtr owner);
    void SetFileTypes(); void SetFileTypeIndex(); void GetFileTypeIndex(); void Advise(); void Unadvise();
    void SetOptions(uint fos); void GetOptions(); void SetDefaultFolder(); void SetFolder(); void GetFolder();
    void GetCurrentSelection(); void SetFileName(); void GetFileName();
    void SetTitle([MarshalAs(UnmanagedType.LPWStr)] string title); void SetOkButtonLabel([MarshalAs(UnmanagedType.LPWStr)] string label);
    void SetFileNameLabel(); void GetResult(out IShellItem item);
  }
  [DllImport("user32.dll")] static extern IntPtr GetForegroundWindow();
  public static string Pick(string title, string ok) {
    var d = (IFileOpenDialog)new FileOpenDialog();
    d.SetOptions(0x20 | 0x40); // FOS_PICKFOLDERS | FOS_FORCEFILESYSTEM
    d.SetTitle(title); d.SetOkButtonLabel(ok);
    if (d.Show(GetForegroundWindow()) != 0) return "";
    IShellItem item; d.GetResult(out item);
    IntPtr p; item.GetDisplayName(0x80058000, out p); // SIGDN_FILESYSPATH
    var s = Marshal.PtrToStringUni(p); Marshal.FreeCoTaskMem(p); return s;
  }
}
'@
[Console]::OutputEncoding = [Text.Encoding]::UTF8
[Picker]::Pick($env:PATHAU_TITLE, $env:PATHAU_OK)
`
