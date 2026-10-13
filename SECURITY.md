# Security

## Reporting a problem
Please report security issues privately via **GitHub → Security → Report a vulnerability** on this repository, not in a public issue.

## How Pathau protects you
- **Local only.** Pathau never talks to the internet. Files and text go directly between your devices on your own network.
- **Pairing needs your OK.** A phone needs the passcode (in the QR code) *and* you must click **Allow** on the PC. A passcode works once, then a new one is made.
- **Paired devices are revocable.** Devices → Remove instantly locks a phone out. Only a SHA-256 hash of each device's secret key is stored on disk.
- **Brute force is blocked.** 10 wrong passcodes pause that device for 5 minutes.
- **PC-to-PC transfers need Accept**, and every file is SHA-256 verified. Damaged copies are deleted.
- **Shared folders are read-only.** Paths are confined with Go's `os.Root`, so `..` and symlinks cannot escape a shared folder. Received files never overwrite existing files.
- **Files from other devices are never launched with one click.** Programs (`.exe`, `.bat`, scripts…) only get "Show in folder".
- **Browser protections.** Admin actions only work from the PC itself (loopback plus a Host check against DNS rebinding). Writes require a custom header that other websites cannot send. Previews are served with `Content-Security-Policy: sandbox` and `nosniff`.
- **No admin rights, no scripts.** Pathau runs as a normal user, and Windows integration uses direct Win32/COM calls, not PowerShell.

## Known limits
- Traffic on your network is plain HTTP, as with similar LAN tools. It is protected by your Wi-Fi encryption (WPA2/WPA3). Avoid using Pathau on public or untrusted Wi-Fi.
- PC-to-PC offers can come from any device on the network. They do nothing unless you click Accept.

## Verifying a download
Every release is built by GitHub Actions from the tagged source ([release workflow](.github/workflows/release.yml)) and lists SHA-256 checksums in `SHA256SUMS.txt`. Check your download in PowerShell:
```powershell
Get-FileHash .\pathau-1.0.0-windows-amd64.exe -Algorithm SHA256
```
