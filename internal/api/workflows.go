package api

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

// The /api/workflows/* composite endpoints (om-2sl6). Every user-visible action in the
// Do tab used to be a sequence of independent POSTs from the browser, with no
// transaction spanning them — a failure after the first left the ledger half-written,
// and re-pressing the still-populated form duplicated the part that succeeded. Each
// endpoint here decodes ONE nested request and calls ONE composite store method
// (store.Record*/Revise*), which wraps the whole action in ONE transaction. A failure at
// any step rolls back everything, so a human re-press is automatically safe — no
// idempotency key needed (there is no auto-retry in the client).
//
// They are hand-written and mounted here rather than through the generic register():
// /api/workflows/* collides with no CRUD resource, so the mux needs no precedence tweak
// and register() is untouched. The GRANULAR endpoints stay exactly as they were — the
// Edit grids, /api/export, the e2e suite and any curl user still depend on them.

// registerWorkflows wires the composite endpoints onto mux. Called from Handler().
func registerWorkflows(mux *http.ServeMux, s *store.Store) {
	// POST /api/workflows/bought-a-box — a roll_txn buy plus an optional bank trip, in
	// one transaction. Branch resolution for both runs inside the tx, so a rejected trip
	// leaves no orphan box or branch (seam a + f).
	mux.HandleFunc("POST /api/workflows/bought-a-box", func(w http.ResponseWriter, r *http.Request) {
		body, err := decode[struct {
			Purchase model.RollTxn `json:"purchase"`
			Trip     *model.Trip   `json:"trip"`
		}](r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		rollTxnID, tripID, err := s.RecordPurchase(body.Purchase, body.Trip)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]int64{"roll_txn_id": rollTxnID, "trip_id": tripID})
	})

	// POST /api/workflows/logged-finds — an optional box (existing or created inline),
	// N CRH finds (each find-or-creating its item_type) and M clad keepers, all attributed
	// to that box, all in one transaction (seam b). Post-om-5psc a kept find is one flagged
	// holding row, never a duplicate keeper; the client sends exactly what it wants written.
	mux.HandleFunc("POST /api/workflows/logged-finds", func(w http.ResponseWriter, r *http.Request) {
		body, err := decode[struct {
			Box *struct {
				ExistingID int64          `json:"existing_id"`
				New        *model.RollTxn `json:"new"`
			} `json:"box"`
			Finds []struct {
				Product      string  `json:"product"`
				Metal        string  `json:"metal"`
				Fineness     string  `json:"fineness"`
				FineOzEach   float64 `json:"fine_oz_each"`
				Qty          float64 `json:"qty"`
				BasisUSD     float64 `json:"basis_usd"`
				PremiumUSD   float64 `json:"premium_usd"`
				FaceValueUSD float64 `json:"face_value_usd"`
				Acquired     string  `json:"acquired"`
				Source       string  `json:"source"`
				Kept         bool    `json:"kept"`
			} `json:"finds"`
			Keepers []struct {
				Denom   string  `json:"denom"`
				Count   int64   `json:"count"`
				FaceUSD float64 `json:"face_usd"`
				Date    string  `json:"date"`
			} `json:"keepers"`
		}](r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		var box store.BoxRef
		if body.Box != nil {
			box.ExistingID = body.Box.ExistingID
			box.New = body.Box.New
		}
		finds := make([]store.FindSpec, 0, len(body.Finds))
		for _, f := range body.Finds {
			finds = append(finds, store.FindSpec{
				Type: store.ItemTypeSpec{Name: f.Product, Metal: f.Metal, Fineness: f.Fineness, FineOzEach: f.FineOzEach},
				Holding: model.Holding{
					Activity: "crh", Qty: f.Qty, BasisUSD: f.BasisUSD, PremiumUSD: f.PremiumUSD,
					FaceValueUSD: f.FaceValueUSD, Acquired: f.Acquired, Source: f.Source, Kept: f.Kept,
				},
			})
		}
		keepers := make([]model.Keeper, 0, len(body.Keepers))
		for _, k := range body.Keepers {
			keepers = append(keepers, model.Keeper{Denom: k.Denom, Count: k.Count, FaceUSD: k.FaceUSD, Date: k.Date})
		}
		if err := s.RecordFinds(box, finds, keepers); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// POST /api/workflows/holdings-with-type — find-or-create the item_type for the
	// catalog fields, then insert the holding pointing at it, in one transaction (the
	// server-side twin of the client's old ensureItemType + separate lots POST). Covers
	// the Edit-grid create, NewBullion and Reconcile.addFind (seams c-create, d, e).
	mux.HandleFunc("POST /api/workflows/holdings-with-type", func(w http.ResponseWriter, r *http.Request) {
		env, err := decode[holdingEnvelope](r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		var h model.Holding
		if err := decodeStrict(env.Holding, &h); err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		id, err := s.RecordHolding(env.Catalog.spec(), h)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
	})

	// PUT /api/workflows/holdings-with-type/{id} — the Edit-grid UPDATE (seam c-update).
	// It find-or-creates the item_type AND merges the named holding fields onto the stored
	// row, in one transaction. The merge is the om-kyq7 guarantee: the `holding` object is
	// decoded ONTO the current row, so a column it does not name (notes, insured_value,
	// attributes, the disposal) survives.
	mux.HandleFunc("PUT /api/workflows/holdings-with-type/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		env, err := decode[holdingEnvelope](r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
		// The merge is a decode ONTO the stored row, inside the store transaction (so the
		// read-merge-write is atomic against other writers). A bad body is the client's
		// fault — capture it separately so it maps to 400, not the 500 a store error gets.
		var badBody error
		err = s.ReviseHolding(id, env.Catalog.spec(), func(cur model.Holding) (model.Holding, error) {
			if e := decodeStrict(env.Holding, &cur); e != nil {
				badBody = e
				return cur, e
			}
			return cur, nil
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
}

// holdingEnvelope is the holdings-with-type request: the catalog fields that
// find-or-create the item_type, and the holding itself carried as raw JSON so it can be
// decoded onto a fresh row (create) or the stored row (update-merge) — the latter is
// what preserves columns the client does not name. DisallowUnknownFields still applies at
// both levels: the envelope rejects a stray top-level key, and decodeStrict rejects a
// stray key inside `holding`.
type holdingEnvelope struct {
	Catalog catalogReq      `json:"catalog"`
	Holding json.RawMessage `json:"holding"`
}

type catalogReq struct {
	Product    string  `json:"product"`
	Metal      string  `json:"metal"`
	Fineness   string  `json:"fineness"`
	FineOzEach float64 `json:"fine_oz_each"`
}

func (c catalogReq) spec() store.ItemTypeSpec {
	return store.ItemTypeSpec{Name: c.Product, Metal: c.Metal, Fineness: c.Fineness, FineOzEach: c.FineOzEach}
}

// decodeStrict decodes a raw JSON object onto dst with DisallowUnknownFields — the same
// rejection the top-level decode[T] applies, used here for the nested `holding` object.
// Decoding onto a NON-zero dst leaves absent keys untouched: that is the PUT merge.
func decodeStrict(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
