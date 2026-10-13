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
- **Safe by default**: random passcode each start (or set your own), 10 wrong tries pauses that device, see and disconnect any device, existing files are never overwritten.
- Big files (many GB) stream straight to disk with live progress and speed.
- A ~8 MB single `.exe`. No installer and no admin rights needed.

## Use it
1. Download `pathau.exe` from [Releases](../../releases) and run it. It opens in your browser.
2. When Windows Firewall asks, click **Allow** (private networks).
3. Phone: scan the QR code. Other PC: run Pathau there too.

Received files go to `Downloads\Pathau` (change it in Settings).

**PC not showing up?** Use *Connect by IP*, and make sure both PCs are on the same Wi-Fi and the network is set to *Private* in Windows.

## Build from source
Requires [Go](https://go.dev/dl/) 1.25+.
```sh
go test ./...
go build -ldflags "-s -w -H windowsgui" -o pathau.exe .   # -H windowsgui hides the console window
```
macOS / Linux: `GOOS=darwin go build` / `GOOS=linux go build`.

## How it works
Each PC runs a small HTTP server on port **8765**, and the UI is a web page served by it. Phones log in with the passcode carried in the QR code, which gives them a session cookie. PCs find each other with a UDP broadcast on port **8766**. A PC-to-PC transfer is an offer, then Accept, then a one-time token. Each file streams with its SHA-256 sent as an HTTP trailer, and the receiver deletes the file if the checksum doesn't match.

| File | What |
|---|---|
| `main.go` | server, auth, settings, live events, text |
| `files.go` | file sources (received, sent to phones, shared folders), uploads, downloads, Windows folder dialog and Explorer |
| `peer.go` | discovery and PC-to-PC transfers |
| `web/` | the UI (embedded into the exe): one page for this PC and for phones |
| `rsrc_windows_*.syso` | the app icon for the .exe (made with `go-winres`) |

## Support
Free forever. Questions or ideas? Open an issue.

## License
MIT
