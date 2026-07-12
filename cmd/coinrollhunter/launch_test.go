package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The compat rule is the one place this change could silently eat someone's
// data: every install to date keeps crh.db next to the binary, so if the new
// per-user data dir ever wins over an existing crh.db, that user opens the app
// to an empty dashboard and concludes their holdings are gone.
func TestDefaultDBPathPrefersExistingCwdDB(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	if err := os.WriteFile(dbName, []byte("not really a db"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := defaultDBPath()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(dbName)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("with a crh.db in cwd, defaultDBPath() = %q, want the existing db %q", got, want)
	}
}

// The other half: a fresh install must NOT write into the working directory,
// because for a double-clicked binary that is Downloads — or a temp dir, if
// Windows ran the .exe straight out of the zip preview, in which case the
// holdings evaporate.
func TestDefaultDBPathUsesDataDirWhenCwdIsEmpty(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	data := t.TempDir()
	t.Setenv("LOCALAPPDATA", data) // windows
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("HOME", data) // darwin

	got, err := defaultDBPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, data) {
		t.Errorf("with an empty cwd, defaultDBPath() = %q, want it under the user data dir %q", got, data)
	}
	if filepath.Dir(got) == dir {
		t.Errorf("defaultDBPath() = %q — must not land in the working directory", got)
	}
	if filepath.Base(got) != dbName {
		t.Errorf("defaultDBPath() = %q, want basename %q", got, dbName)
	}
}

// instanceAt decides whether a failed bind means "we are already running" (open
// the existing window) or "someone else owns this port" (move to another one).
// Getting it wrong the first way opens a browser onto a stranger's server.
func TestInstanceAtRecognisesOnlyUs(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    bool
	}{
		{
			name: "our health endpoint",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"status":"ok"}`))
			},
			want: true,
		},
		{
			name: "some other server answering 200 on that path",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`<html>not us</html>`))
			},
			want: false,
		},
		{
			name:    "something on the port that 404s",
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) },
			want:    false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			addr := strings.TrimPrefix(srv.URL, "http://")
			if got := instanceAt(addr); got != tc.want {
				t.Errorf("instanceAt() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInstanceAtOnADeadPort(t *testing.T) {
	// A port nobody is listening on: a connection error, not a bad response.
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()

	if instanceAt(addr) {
		t.Error("instanceAt() = true for a closed port, want false")
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(prev) })
}
