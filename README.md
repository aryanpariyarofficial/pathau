![Pathau: send files between phone & PC over Wi-Fi, free, no cloud, no cable](assets/Pathau%20Wireless%20File%20Transfer.png)

# Pathau (पठाउ)

Send files, folders and text between your **phone and PC**, or **PC and PC**, over your own Wi-Fi or Ethernet.
No cable, no cloud, no account, no internet data. Free and open source.

तपाईंको फोन र कम्प्युटर, वा दुई कम्प्युटरबीच फाइल, फोल्डर र टेक्स्ट Wi-Fi बाट सिधै पठाउनुहोस्। केबल चाहिँदैन, इन्टरनेट डाटा खर्च हुँदैन।

## Features
- **Phone ↔ PC**: scan the QR code, and Pathau opens in the phone's browser. Nothing to install on Android or iPhone.
- **PC → phone**: drop any files or folders on *Send to phones*, and every connected phone gets them under *Files*.
- **Files browser**: received files, files sent to phones and shared folders in one place. Grid or list view, sorted by name, size or date, with search, a full-screen photo/video preview, *Open* and *Show in folder*.
- **PC ↔ PC**: other PCs show up automatically. Click *Send files* / *Send folder* or drag and drop; the other PC clicks *Accept*. Folders keep their structure.
- **Every PC-to-PC file is SHA-256 verified.** A damaged copy is deleted and resent automatically.
- **Text & clipboard**: send links, OTPs and codes. Text from your phone lands in the PC clipboard, ready for Ctrl+V.
- **Shared folders** (read-only, passcode protected): phones browse and download, single files or whole folders as .zip. Downloads can resume.
- **Pair once**: a phone pairs with the QR code, and you click **Allow** on the PC. It stays paired, even after restarts, until you remove it.
- **iPhone OTPs to the PC clipboard automatically**: an iPhone Shortcut forwards OTP messages, and Pathau copies just the code. Set it up under Devices.
- **Always ready**: starts with Windows and lives in the system tray.
- **Safe by default**: see [SECURITY.md](SECURITY.md). New devices need your Allow, passcodes work once, 10 wrong tries pauses a device, and existing files are never overwritten.
- Big files (many GB) stream straight to disk with live progress and speed.
- A ~8 MB single `.exe`. No installer and no admin rights needed.

## Use it
1. Download `pathau-…-windows-amd64.exe` from [Releases](../../releases) and run it. No install and no admin rights; it opens in your browser and adds a tray icon.
2. When Windows Firewall asks, click **Allow** (private networks).
3. Phone: scan the QR code, then click **Allow** on the PC. Other PC: run Pathau there too.

**"Windows protected your PC"?** Windows SmartScreen shows this for new apps that aren't code-signed yet. Click **More info → Run anyway**. Every release is built from this source code by [GitHub Actions](.github/workflows/release.yml) and published with SHA-256 checksums. See [SECURITY.md](SECURITY.md#verifying-a-download) to verify your download.

Received files go to `Downloads\Pathau` (change it in Settings).

**PC not showing up?** Use *Connect by IP*, and make sure both PCs are on the same Wi-Fi and the network is set to *Private* in Windows.

## Build from source
Requires [Go](https://go.dev/dl/) 1.25+.
```sh
go test ./...
go build -trimpath -ldflags "-s -w -H windowsgui" -o pathau.exe .   # -H windowsgui: tray app, no console window
```
macOS / Linux: `GOOS=darwin go build` / `GOOS=linux go build`.

## How it works
Each PC runs a small HTTP server on port **8765**, and the UI is a web page served by it. A phone pairs using the one-time passcode in the QR code plus your **Allow**. It then gets a permanent device key, stored as a cookie on the phone and as a hash in `devices.json` on the PC. PCs find each other with a UDP broadcast on port **8766**. A PC-to-PC transfer is an offer, then Accept, then a one-time token. Each file streams with its SHA-256 sent as an HTTP trailer, and the receiver deletes the file if the checksum doesn't match.

| File | What |
|---|---|
| `main.go` | server, auth, settings, live events, text |
| `files.go` | file sources (received, sent to phones, shared folders), uploads, downloads |
| `peer.go` | discovery and PC-to-PC transfers |
| `devices.go` | pairing (approval, permanent devices, Shortcut keys) and OTP detection |
| `shell_windows.go` | tray, start with Windows, folder dialog, Explorer (Win32/COM, no scripts) |
| `web/` | the UI (embedded into the exe): one page for this PC and for phones |
| `rsrc_windows_*.syso` | the app icon for the .exe (made with `go-winres`) |

## Support
Free forever. Questions or ideas? Open an issue. To make a release: `git tag v1.0.0 && git push origin v1.0.0`.

## License
MIT
