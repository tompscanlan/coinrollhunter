// Package api is the JSON REST layer over the store (ADR-001). The same handler
// serves localhost now and could back a hosted mode later. A generic resource
// helper keeps the per-table CRUD wiring DRY.
package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/tompscanlan/coinrollhunter/internal/calc"
	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// Handler builds the HTTP handler. webFS is the embedded static UI (may be nil
// in tests); it is served at the root with the API mounted under /api/.
func Handler(s *store.Store, webFS fs.FS) http.Handler {
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
	register(mux, "item-types", resource[model.ItemType]{
		list: s.ListItemTypes, create: s.InsertItemType, update: s.UpdateItemType, del: s.DeleteItemType,
	})
	register(mux, "lots", resource[model.Holding]{
		list: s.ListHoldings, create: s.InsertHolding, update: s.UpdateHolding, del: s.DeleteHolding,
	})
	register(mux, "roll-txns", resource[model.RollTxn]{
		list: s.ListRollTxns, create: s.InsertRollTxn, update: s.UpdateRollTxn, del: s.DeleteRollTxn,
	})
	register(mux, "branches", resource[model.Branch]{
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
	register(mux, "trips", resource[model.Trip]{
		list: s.ListTrips, create: s.InsertTrip, update: s.UpdateTrip, del: s.DeleteTrip,
	})
	register(mux, "supplies", resource[model.Supply]{
		list: s.ListSupplies, create: s.InsertSupply, update: s.UpdateSupply, del: s.DeleteSupply,
	})
	register(mux, "keepers", resource[model.Keeper]{
		list: s.ListKeepers, create: s.InsertKeeper, update: s.UpdateKeeper, del: s.DeleteKeeper,
	})
	register(mux, "losses", resource[model.Loss]{
		list: s.ListLosses, create: s.InsertLoss, update: s.UpdateLoss, del: s.DeleteLoss,
	})

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
		if p != "" {
			if f, err := webFS.Open(p); err == nil {
				f.Close()
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
func register[T any](mux *http.ServeMux, name string, r resource[T]) {
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

	mux.HandleFunc("PUT "+base+"/{id}", func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		v, err := decode[T](req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		if err := r.update(id, v); err != nil {
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
	writeJSON(w, http.StatusInternalServerError, errBody(err))
}

// nonNil returns an empty slice instead of nil so JSON encodes [] not null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
