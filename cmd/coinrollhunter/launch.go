package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// The double-click path (om-9p0l).
//
// Everything here exists to serve a user who has never opened a terminal: they
// download an archive, unpack it, double-click, and expect their dashboard. That
// user gets no second chance — if the first click produces a console flash and
// nothing else, they are gone. So a bare `coinrollhunter` with no arguments does
// not print usage; it runs the app.

const (
	// dbName is the database filename, in the per-user data dir or the cwd.
	dbName = "crh.db"
	// logName captures the diagnostics that would otherwise go to a console the
	// double-click path does not have.
	logName = "coinrollhunter.log"
	// defaultAddr is the preferred port. If it is taken we fall back to an
	// ephemeral one rather than making a busy port the user's problem.
	defaultAddr = "127.0.0.1:8787"
)

// runApp is what a double-click runs: pick a database, serve, open the UI.
func runApp() error {
	dbPath, err := defaultDBPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	// With no console, every fmt.Print in the serve path is shouting into a void.
	// Point stdout/stderr at a file so there is something to look at when a user
	// says "it didn't work" — and so the log line below tells them where.
	if !hasConsole() {
		logPath := filepath.Join(filepath.Dir(dbPath), logName)
		if f, err := redirectOutput(logPath); err == nil {
			defer f.Close()
		}
	}
	fmt.Printf("coinrollhunter %s starting (db: %s)\n", version, dbPath)

	// Bind before opening the store: if another instance already owns the port it
	// also owns the database, and opening it twice is how you get a locked DB.
	ln, err := net.Listen("tcp", defaultAddr)
	if err != nil {
		if instanceAt(defaultAddr) {
			// Second double-click. The app is already running — the user just
			// wants to see it, so show them and get out of the way.
			fmt.Println("already running — reopening the existing window")
			openBrowser("http://" + defaultAddr)
			return nil
		}
		// The port is taken by something that is not us. Not the user's problem.
		fmt.Printf("%s is in use by another program — using a free port instead\n", defaultAddr)
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return fmt.Errorf("listen: %w", err)
		}
	}

	s, err := store.Open(dbPath)
	if err != nil {
		ln.Close()
		return fmt.Errorf("open %s: %w", dbPath, err)
	}
	defer s.Close()

	return serveStore(s, serveOpts{
		dbPath:       dbPath,
		listener:     ln,
		spotProvider: envOr("CRH_SPOT_PROVIDER", "gold-api"),
		spotInterval: envDur("CRH_SPOT_INTERVAL", 6*time.Hour),
		onReady:      openBrowser,
	})
}

// defaultDBPath decides where a double-clicked binary keeps its data.
//
// A crh.db in the working directory wins. That is where every install to date
// has put it, and quietly serving an empty database from a new location would
// look exactly like data loss. Otherwise the database goes in the per-user data
// dir — because the working directory of a double-clicked binary is wherever the
// binary happens to sit (Downloads), or worse: Windows will run an .exe straight
// out of the zip preview by unpacking it to a temp dir, and holdings written
// there evaporate.
func defaultDBPath() (string, error) {
	if _, err := os.Stat(dbName); err == nil {
		return filepath.Abs(dbName)
	}
	dir, err := appDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, dbName), nil
}

// appDir is the per-user directory for this app's data, per platform convention.
func appDir() (string, error) {
	var base string
	switch runtime.GOOS {
	case "windows":
		// LOCALAPPDATA, not APPDATA: the database is machine-local state, not
		// something to drag across a roaming profile.
		if base = os.Getenv("LOCALAPPDATA"); base == "" {
			base = os.Getenv("APPDATA")
		}
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, "Library", "Application Support")
	default:
		if base = os.Getenv("XDG_DATA_HOME"); base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, ".local", "share")
		}
	}
	if base == "" {
		return "", errors.New("cannot locate a user data directory")
	}
	return filepath.Join(base, appDirName()), nil
}

// appDirName is title-case where users see it in a file manager, lowercase where
// the convention is lowercase.
func appDirName() string {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return "CoinRollHunter"
	}
	return "coinrollhunter"
}

// instanceAt reports whether a CoinRollHunter is already serving at addr. Only
// consulted after a failed bind, to tell our own second launch apart from some
// unrelated program sitting on the port — so it checks the health payload, not
// just that something answered.
func instanceAt(addr string) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + addr + "/api/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}
	return body.Status == "ok"
}

// hasConsole reports whether stdout is a terminal we can print to. False for the
// Windows GUI binary when double-clicked (it is built for the GUI subsystem, so
// it starts with no console and no valid std handles at all), and false for a
// macOS/Linux launch from a file manager, where stdout is /dev/null.
//
// It has to be a real isatty check, not a character-device test: /dev/null is a
// character device, so the cheap test says "console" for exactly the GUI launches
// that most need a log file.
func hasConsole() bool {
	return isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
}

// redirectOutput points stdout/stderr at a log file. fmt.Print* resolves
// os.Stdout at call time, so this captures the existing diagnostics too.
func redirectOutput(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	os.Stdout = f
	os.Stderr = f
	return f, nil
}

// printFatal is the stderr half of showFatal, shared by both platform builds.
func printFatal(title, msg string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", title, msg)
}
