// Package api is the JSON REST layer over the store (ADR-001). The same handler
// serves localhost now and could back a hosted mode later. A generic resource
// helper keeps the per-table CRUD wiring DRY.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tompscanlan/coinrollhunter/internal/calc"
	"github.com/tompscanlan/coinrollhunter/internal/export"
	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// viteHashedAssetName recognizes Vite's default name-[hash].ext shape. This is a
// conservative cache heuristic, not proof that the suffix is a hash: an explicitly
// configured English suffix of 8+ URL-safe characters can also match. Public files land
// at the dist root by default, so that false-positive requires a deliberate output config.
var viteHashedAssetName = regexp.MustCompile(`^assets/(?:.*/)?[^/]+-[A-Za-z0-9_-]{8,}\.[^/]+$`)

// Handler builds the HTTP handler. webFS is the embedded static UI (may be nil
// in tests); it is served at the root with the API mounted under /api/.
//
// photosDir is where photo ORIGINALS live (photos/<owner_uid>/<uid>.<ext>, beside the
// database); cacheDir is the separate, regenerable derivative tree (om-6hlp). Both may be
// "" — an in-memory or test store has no on-disk home, and the photo routes then 404/500
// rather than touch the filesystem.
func Handler(s *store.Store, webFS fs.FS, photosDir, cacheDir string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/summary", func(w http.ResponseWriter, r *http.Request) {
		d, err := s.ResolveDataset()
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, calc.Compute(d))
	})

	// finds-report: the "1 per face $" hit-rate view, per denom × category × source (ADR-006).
	mux.HandleFunc("GET /api/finds-report", func(w http.ResponseWriter, r *http.Request) {
		d, err := s.ResolveDataset()
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, calc.ComputeFindsReport(d))
	})

	// export: the whole database as a zip the user can open in a spreadsheet — CSVs,
	// a lossless data.json, and the photo originals (internal/export). A browser can
	// only take a single file, hence the zip; `coinrollhunter export DIR` writes the
	// same bundle as a plain directory. This is data, so unlike POST /api/quit it
	// belongs in the API rather than in the command.
	mux.HandleFunc("GET /api/export", func(w http.ResponseWriter, r *http.Request) {
		// Built into a temp file, not straight down the wire: once bytes are on the
		// socket the status is spent, and a half-written zip that says 200 is worse
		// than an error. A bundle carrying a collection's photos is also not something
		// to hold in memory.
		f, err := os.CreateTemp("", "coinrollhunter-export-*.zip")
		if err != nil {
			writeErr(w, err)
			return
		}
		defer os.Remove(f.Name())
		defer f.Close()

		// This handler holds the user's REAL, already-migrated store, so the photo root is
		// simply beside its path — no throwaway copy involved (that is the CLI's concern).
		// Resolve symlinks so a DB reached through a link still finds its photos. The request
		// context flows in, so a browser that navigates away releases the DB connection.
		if err := export.WriteZip(r.Context(), s, export.PhotoRoot(export.ResolveDBPath(s.Path())), f); err != nil {
			writeErr(w, err)
			return
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+export.Filename(time.Now())+`"`)
		io.Copy(w, f)
	})

	// spot: GET history, GET latest, POST append/upsert
	mux.HandleFunc("GET /api/spot", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.ListSpot()
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, nonNil(list))
	})
	mux.HandleFunc("GET /api/spot/latest", func(w http.ResponseWriter, r *http.Request) {
		sp, err := s.LatestSpot()
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sp)
	})
	mux.HandleFunc("POST /api/spot", func(w http.ResponseWriter, r *http.Request) {
		sp, err := decode[model.Spot](r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		if err := s.PutSpot(sp); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sp)
	})

	// settings: GET / PUT
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := s.GetSettings()
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, cfg)
	})
	mux.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := decode[model.Settings](r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		if err := s.PutSettings(cfg); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, cfg)
	})

	// lots: sell (full or partial) — records disposal + realized P&L. More
	// specific than POST /api/lots, so it takes precedence in the mux.
	mux.HandleFunc("POST /api/lots/{id}/sell", func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		body, err := decode[struct {
			Qty      float64 `json:"qty"`
			Proceeds float64 `json:"proceeds_usd"`
			Date     string  `json:"date"`
		}](r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		if err := s.SellHolding(id, body.Qty, body.Proceeds, body.Date); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// CRUD resources
	register(mux, s, "item-types", resource[model.ItemType]{
		list: s.ListItemTypes, create: s.InsertItemType, update: s.UpdateItemType, del: s.DeleteItemType,
	})
	register(mux, s, "lots", resource[model.Holding]{
		list: s.ListHoldings, create: s.InsertHolding, update: s.UpdateHolding, del: s.DeleteHolding,
	})
	register(mux, s, "roll-txns", resource[model.RollTxn]{
		list: s.ListRollTxns, create: s.InsertRollTxn, update: s.UpdateRollTxn, del: s.DeleteRollTxn,
	})
	register(mux, s, "branches", resource[model.Branch]{
		list: s.ListBranches, create: s.InsertBranch, update: s.UpdateBranch, del: s.DeleteBranch,
	})
	// Fold duplicate branches into one survivor (ADR-010 dedup). More specific than
	// the generic /api/branches routes, so it takes precedence in the mux.
	mux.HandleFunc("POST /api/branches/{id}/merge", func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		body, err := decode[struct {
			LoserIDs []int64 `json:"loser_ids"`
		}](r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		if err := s.MergeBranches(id, body.LoserIDs); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	register(mux, s, "trips", resource[model.Trip]{
		list: s.ListTrips, create: s.InsertTrip, update: s.UpdateTrip, del: s.DeleteTrip,
	})
	register(mux, s, "supplies", resource[model.Supply]{
		list: s.ListSupplies, create: s.InsertSupply, update: s.UpdateSupply, del: s.DeleteSupply,
	})
	register(mux, s, "keepers", resource[model.Keeper]{
		list: s.ListKeepers, create: s.InsertKeeper, update: s.UpdateKeeper, del: s.DeleteKeeper,
	})
	register(mux, s, "losses", resource[model.Loss]{
		list: s.ListLosses, create: s.InsertLoss, update: s.UpdateLoss, del: s.DeleteLoss,
	})

	// Composite workflow endpoints (/api/workflows/*, om-2sl6): one endpoint per
	// compound Do-tab action, each wrapping the whole action in ONE store transaction.
	// They coexist with the generic register()'d routes above (distinct path prefix) and
	// with the two other hand-written handlers (lots/{id}/sell, branches/{id}/merge).
	registerWorkflows(mux, s)

	// Photos (om-6hlp): multipart upload, a DB-lookup file-serve route, and the
	// gallery list/update/soft-delete. Hand-written (register() assumes an argument-less
	// list; photos are owner-filtered and the upload bypasses the JSON decode path).
	registerPhotos(mux, s, photosDir, cacheDir)

	// Static UI at the root (when embedded), with an SPA fallback to index.html.
	if webFS != nil {
		mux.Handle("/", spaHandler(webFS))
	}
	return mux
}

// spaHandler serves embedded static files and falls back to index.html for any
// path that isn't a real asset, so the single-page app survives a deep-link or
// refresh. /api/* is matched by more specific routes and never reaches here.
func spaHandler(webFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(webFS))
	index, _ := fs.ReadFile(webFS, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		// The HTML entrypoint (including deep-link fallbacks) must be revalidated
		// because it selects the current hashed assets. A confirmed Vite asset is
		// content-hashed and can replace this default with an immutable policy.
		w.Header().Set("Cache-Control", "no-cache")
		if p != "" {
			if f, err := webFS.Open(p); err == nil {
				f.Close()
				if viteHashedAssetName.MatchString(p) {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		} else {
			fileServer.ServeHTTP(w, r) // "/" -> index.html
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	})
}

// resource bundles the store ops for one CRUD table.
type resource[T any] struct {
	list   func() ([]T, error)
	create func(T) (int64, error)
	update func(int64, T) error
	del    func(int64) error
}

// register wires GET/POST /api/<name> and PUT/DELETE /api/<name>/{id}.
//
// PUT is a MERGE, not a replace (see the PUT handler). T is constrained to
// model.Entity so the merge can find the row it addresses — which also means a new
// resource cannot be registered without opting into merge semantics.
func register[T model.Entity](mux *http.ServeMux, s *store.Store, name string, r resource[T]) {
	base := "/api/" + name

	mux.HandleFunc("GET "+base, func(w http.ResponseWriter, _ *http.Request) {
		items, err := r.list()
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, nonNil(items))
	})

	mux.HandleFunc("POST "+base, func(w http.ResponseWriter, req *http.Request) {
		v, err := decode[T](req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		id, err := r.create(v)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
	})

	// PUT is a MERGE, not a full replace: the body is decoded ONTO the stored row, so a
	// field the client does not send is a field it cannot destroy.
	//
	// It used to decode onto a zero T and write that, which made every PUT a full
	// replace — and the Holdings grid models only some of a lot's columns, so editing
	// any cell wrote back an empty string for notes and a zero for insured_value and
	// attributes. A user who imported a spreadsheet lost their notes to a quantity fix,
	// silently and with no undo. Replace semantics put that gun on the table for every
	// partial client; merge semantics take it away for all of them at once.
	//
	// Clearing a field still works — you just have to say so ("notes": ""), which is
	// the difference between intent and collateral damage.
	mux.HandleFunc("PUT "+base+"/{id}", func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		// Read-modify-write, so it runs under the store write lock: a sale committing
		// between the read and the write would be undone by the write-back.
		var badBody error
		err = s.WithWrite(func() error {
			items, err := r.list()
			if err != nil {
				return err
			}
			cur, ok := byID(items, id)
			if !ok {
				return store.ErrNotFound
			}
			merged, err := decodeOnto(req, cur)
			if err != nil {
				badBody = err
				return err
			}
			return r.update(id, merged)
		})
		if badBody != nil {
			writeJSON(w, http.StatusBadRequest, errBody(badBody))
			return
		}
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int64{"id": id})
	})

	mux.HandleFunc("DELETE "+base+"/{id}", func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		if err := r.del(id); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// --- helpers -----------------------------------------------------------------

func decode[T any](r *http.Request) (T, error) {
	var v T
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(&v)
	return v, err
}

// decodeOnto decodes the request body onto cur rather than onto a zero value, so a
// field the body does not name keeps the value it already has. This is the whole of
// the PUT merge: encoding/json leaves absent keys untouched in the destination.
func decodeOnto[T any](r *http.Request, cur T) (T, error) {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(&cur)
	return cur, err
}

// byID finds the row with the given id among items.
func byID[T model.Entity](items []T, id int64) (T, bool) {
	for _, it := range items {
		if it.EntityID() == id {
			return it, true
		}
	}
	var zero T
	return zero, false
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func errBody(err error) map[string]string { return map[string]string{"error": err.Error()} }

// writeErr maps store errors to status codes.
func writeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errBody(err))
		return
	}
	// A validation failure is the client's fault, not the server's: a 400 with the
	// field-level message, not a 500. Every store mutation funnels rejects here (via
	// r.create / r.update / SellHolding / PutSpot / PutSettings), so create AND the
	// PUT merge both get it. The body is {"error": "<field> <reason>"}, which the UI
	// already renders and reverts the cell on (api.ts / EditableGrid.svelte).
	if errors.Is(err, model.ErrInvalid) {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	writeJSON(w, http.StatusInternalServerError, errBody(err))
}

// nonNil returns an empty slice instead of nil so JSON encodes [] not null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
