//go:build !windows

package main

import "errors"

// Non-Windows builds: no tray or start-up entry yet; folders open with the default file manager.

func autostart() bool              { return false }
func setAutostart(bool) error      { return errors.New("only on Windows") }
func runTray(*App, string)         {}
func showInExplorer(dir, _ string) { open(dir) }

func pickFolder(string, string) (string, error) {
	return "", errors.New("type the folder path instead")
}
