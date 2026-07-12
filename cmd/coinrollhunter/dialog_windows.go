package main

import (
	"syscall"
	"unsafe"
)

// showFatal reports a startup failure. The GUI binary has no console, so an
// error written to stderr is an error nobody ever sees: the app would just fail
// to appear. A message box is the only channel left.
func showFatal(title, msg string) {
	if hasConsole() {
		printFatal(title, msg)
		return
	}
	const mbIconError = 0x00000010
	t, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	m, err := syscall.UTF16PtrFromString(msg)
	if err != nil {
		return
	}
	messageBoxW := syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW")
	messageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbIconError)
}
