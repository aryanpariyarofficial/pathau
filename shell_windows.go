package main

// Windows integration with direct Win32/COM calls (no PowerShell or scripts, which antivirus
// heuristics rightly distrust): tray icon, start with Windows, folder dialog, Explorer.

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"fyne.io/systray"
	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"github.com/ncruces/zenity"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	user32              = windows.NewLazySystemDLL("user32.dll")
	getForegroundWindow = user32.NewProc("GetForegroundWindow")
	setForegroundWindow = user32.NewProc("SetForegroundWindow")
	showWindow          = user32.NewProc("ShowWindow")
	isIconic            = user32.NewProc("IsIconic")
	allowSetForeground  = user32.NewProc("AllowSetForegroundWindow")
)

// ---- start with Windows (per-user Run key, no admin rights) ----

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func autostart() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue("Pathau")
	return err == nil
}

func setAutostart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue("Pathau"); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue("Pathau", `"`+exe+`" --background`)
}

// ---- tray ----

func runTray(a *App, url string) {
	systray.Run(func() {
		systray.SetIcon(icoFromPNG(mustRead("web/favicon.png")))
		systray.SetTooltip("Pathau: Wi-Fi file transfer")
		systray.SetOnTapped(func() { open(url) })
		show := systray.AddMenuItem("Open Pathau", "Open the Pathau window")
		systray.AddSeparator()
		auto := systray.AddMenuItemCheckbox("Start with Windows", "Run Pathau when you sign in", autostart())
		quit := systray.AddMenuItem("Quit Pathau", "Stop Pathau; phones will disconnect")
		go func() {
			for {
				select {
				case <-show.ClickedCh:
					open(url)
				case <-auto.ClickedCh:
					if setAutostart(!auto.Checked()) == nil {
						if autostart() {
							auto.Check()
						} else {
							auto.Uncheck()
						}
					}
				case <-quit.ClickedCh:
					systray.Quit()
				}
			}
		}()
	}, func() { os.Exit(0) })
}

func mustRead(name string) []byte {
	b, err := webFS.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return b
}

// icoFromPNG wraps a PNG in a one-image .ico (PNG-in-ICO is supported since Windows Vista).
func icoFromPNG(png []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, []uint16{0, 1, 1})              // reserved, type=icon, count
	b.Write([]byte{0, 0, 0, 0})                                           // 256x256 (0 = 256), no palette
	binary.Write(&b, binary.LittleEndian, []uint16{1, 32})                // planes, bits per pixel
	binary.Write(&b, binary.LittleEndian, []uint32{uint32(len(png)), 22}) // size, offset
	b.Write(png)
	return b.Bytes()
}

// ---- folder dialog ----

func pickFolder(title, okLabel string) (string, error) {
	owner, _, _ := getForegroundWindow.Call() // the browser, so the dialog opens on top of it
	dir, err := zenity.SelectFile(zenity.Directory(), zenity.Title(title), zenity.OKLabel(okLabel), zenity.Attach(owner))
	if err == zenity.ErrCanceled {
		return "", nil
	}
	return dir, err
}

// ---- Explorer ----

// showInExplorer navigates an Explorer window that's already open to dir (selecting file, if
// given) instead of opening a new window each time; opens a new window only if none is open.
func showInExplorer(dir, file string) {
	allowSetForeground.Call(uintptr(0xFFFFFFFF)) // ASFW_ANY: let Explorer come to the front
	go func() {
		runtime.LockOSThread() // COM needs one thread for its whole conversation
		defer runtime.UnlockOSThread()
		if err := reuseExplorer(dir, file); err != nil {
			if file != "" {
				startExplorer(`/select,"` + dir + `\` + file + `"`)
			} else {
				startExplorer(`"` + dir + `"`)
			}
		}
	}()
}

type errNoWindow struct{}

func (errNoWindow) Error() string { return "no Explorer window open" }

func reuseExplorer(dir, file string) error {
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		return err
	}
	defer ole.CoUninitialize()
	unk, err := oleutil.CreateObject("Shell.Application")
	if err != nil {
		return err
	}
	defer unk.Release()
	shell, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer shell.Release()
	winsV, err := oleutil.CallMethod(shell, "Windows")
	if err != nil {
		return err
	}
	wins := winsV.ToIDispatch()
	defer wins.Release()
	countV, err := oleutil.GetProperty(wins, "Count")
	if err != nil {
		return err
	}
	for i := 0; i < int(countV.Val); i++ {
		itemV, err := oleutil.CallMethod(wins, "Item", i)
		if err != nil || itemV.VT != ole.VT_DISPATCH || itemV.ToIDispatch() == nil {
			continue
		}
		w := itemV.ToIDispatch()
		name, _ := oleutil.GetProperty(w, "FullName")
		if name == nil || !strings.HasSuffix(strings.ToLower(name.ToString()), `\explorer.exe`) {
			w.Release()
			continue
		}
		defer w.Release()
		if _, err := oleutil.CallMethod(w, "Navigate2", dir); err != nil {
			return err
		}
		if file != "" {
			selectWhenLoaded(w, dir, file)
		}
		if hv, err := oleutil.GetProperty(w, "HWND"); err == nil {
			hwnd := uintptr(hv.Val)
			if r, _, _ := isIconic.Call(hwnd); r != 0 {
				showWindow.Call(hwnd, 9) // SW_RESTORE
			}
			setForegroundWindow.Call(hwnd)
		}
		return nil
	}
	return errNoWindow{}
}

// selectWhenLoaded waits (up to 5s) for the window to show dir, then selects file.
func selectWhenLoaded(w *ole.IDispatch, dir, file string) {
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		docV, err := oleutil.GetProperty(w, "Document")
		if err != nil || docV.VT != ole.VT_DISPATCH {
			continue
		}
		doc := docV.ToIDispatch()
		folderV, err := oleutil.GetProperty(doc, "Folder")
		if err == nil && folderV.VT == ole.VT_DISPATCH {
			folder := folderV.ToIDispatch()
			selfV, err := oleutil.GetProperty(folder, "Self")
			if err == nil && selfV.VT == ole.VT_DISPATCH {
				pathV, _ := oleutil.GetProperty(selfV.ToIDispatch(), "Path")
				if pathV != nil && strings.EqualFold(pathV.ToString(), dir) {
					if itemV, err := oleutil.CallMethod(folder, "ParseName", file); err == nil && itemV.VT == ole.VT_DISPATCH {
						oleutil.CallMethod(doc, "SelectItem", itemV, 1|4|8|16) // select, deselect others, scroll to, focus
					}
					doc.Release()
					return
				}
			}
		}
		doc.Release()
	}
}

func startExplorer(args string) {
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "explorer.exe " + args} // explorer wants its own quoting
	cmd.Start()
}

// notify shows a Windows notification (non-blocking).
func notify(title, text string) {
	go zenity.Notify(text, zenity.Title(title), zenity.InfoIcon)
}
