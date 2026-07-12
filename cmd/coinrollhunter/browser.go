package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// openBrowser shows the UI at url. Best-effort by construction: a browser that
// will not start is a worse experience, not a failed one — the server keeps
// serving and we print the URL, so nothing here is allowed to return an error
// that would take the app down with it.
func openBrowser(url string) {
	if err := openAppWindow(url); err == nil {
		return
	}
	if err := openDefaultBrowser(url); err != nil {
		fmt.Printf("could not open a browser (%v)\n", err)
	}
	fmt.Printf("CoinRollHunter is running at %s\n", url)
}

// openAppWindow tries to open url in a Chromium "app window" — no tabs, no URL
// bar, its own taskbar entry. It is the difference between the user thinking
// they are in an application and thinking they are on a web page, and it costs
// nothing: Edge ships on every Windows 10/11 box, and it is the same binary we
// would have opened a plain tab in.
func openAppWindow(url string) error {
	for _, bin := range chromiumPaths() {
		path, err := exec.LookPath(bin)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, "--app="+url)
		if err := cmd.Start(); err != nil {
			continue
		}
		// Reap it, so a browser the user closes does not linger as a zombie.
		go func() { _ = cmd.Wait() }()
		return nil
	}
	return fmt.Errorf("no chromium browser found")
}

// chromiumPaths lists Edge/Chrome candidates, most-likely first. On Windows they
// are absolute: neither browser is on PATH there, so LookPath alone finds
// nothing. exec.LookPath accepts an absolute path and just verifies it runs.
func chromiumPaths() []string {
	switch runtime.GOOS {
	case "windows":
		var paths []string
		for _, base := range []string{
			os.Getenv("ProgramFiles(x86)"),
			os.Getenv("ProgramFiles"),
			os.Getenv("LOCALAPPDATA"),
		} {
			if base == "" {
				continue
			}
			paths = append(paths,
				filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
				filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
			)
		}
		return paths
	case "darwin":
		// macOS hides the executable inside the bundle; `open` (below) is the
		// well-behaved way in, and app-mode is not worth reaching past it for.
		return nil
	default:
		return []string{"google-chrome", "chromium", "chromium-browser", "microsoft-edge"}
	}
}

// openDefaultBrowser hands url to whatever the OS considers the default browser.
func openDefaultBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// rundll32 rather than `cmd /c start`: start treats a leading quoted
		// argument as a window title and mangles URLs containing &.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
