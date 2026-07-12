//go:build !windows

package main

// showFatal reports a startup failure. Only Windows ships a GUI binary with no
// console, so everywhere else stderr is still a real channel.
func showFatal(title, msg string) {
	printFatal(title, msg)
}
