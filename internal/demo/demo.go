// Package demo seeds an empty store with a deterministic, entirely fictional
// dataset sized like a real hunter's: ~$40k face searched across ~500 buys and
// ~15 months, with per-source hit rates borrowed from published r/CRH field
// data (see docs/ADR-006). It exists so a first-time user opens a full app —
// dashboard, hit-rate grid (with both confident and low-N cells), trophies,
// realized P&L, reconciliation float — instead of an empty one.
//
// Nothing here is real: banks, dates, finds, and the bullion stack are all
// generated. Amounts reconcile by construction (returns are derived from buys
// minus finds/keepers/losses, with the final week left outstanding so the
// "Return to bank" workflow has something to do).
package demo

import (
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/tompscanlan/coinrollhunter/internal/model"
	"github.com/tompscanlan/coinrollhunter/internal/store"
)

const (
	weeks      = 65 // ~15 months
	blockWks   = 4  // "monthly" grouping for common find categories
	seed       = 42 // fixed: same now -> same dataset
	dumpBank   = "Midtown Community Bank"
	dateLayout = "2006-01-02"
)

// Seed populates s (which must be empty) with the demo dataset. now anchors the
// timeline: activity spans the ~15 months ending at now. Deterministic for a
// given now.
func Seed(s *store.Store, now time.Time) error {
	g := &gen{
		s:     s,
		rng:   rand.New(rand.NewSource(seed)),
		now:   now,
		start: now.AddDate(0, 0, -weeks*7),
		types: map[string]int64{},
	}

	// One transaction around the ~2k inserts: without it every row fsyncs its
	// own autocommit and a fresh on-disk seed takes ~40s instead of well under a
	// second. (Plain Exec BEGIN/COMMIT is safe here — the store holds a single
	// connection.) sales() stays outside: SellHolding opens its own transaction.
	if _, err := s.DB().Exec("BEGIN"); err != nil {
		return fmt.Errorf("seed demo data: %w", err)
	}
	g.bullion()
	g.hunt()    // buys + trophies (needs buys recorded before finds/returns)
	g.finds()   // per-buy rare finds + per-block commons
	g.returns() // derived: buys − finds − keepers − losses, last week outstanding
	g.trips()
	g.extras() // supplies, keepers, losses
	g.spotHistory()
	if g.err != nil {
		s.DB().Exec("ROLLBACK")
		return fmt.Errorf("seed demo data: %w", g.err)
	}
	if _, err := s.DB().Exec("COMMIT"); err != nil {
		return fmt.Errorf("seed demo data: %w", err)
	}

	g.sales() // realized P&L: some rounds + most of the Barber hoard
	if g.err != nil {
		return fmt.Errorf("seed demo data: %w", g.err)
	}
	return nil
}

// --- generator ----------------------------------------------------------------

type buyRec struct {
	id     int64
	week   int
	denom  string
	source string
	face   float64
	bank   string
	date   string
}

type gen struct {
	s     *store.Store
	rng   *rand.Rand
	now   time.Time
	start time.Time
	err   error

	types map[string]int64 // item_type name -> id
	buys  []buyRec

	findFaceWk map[int]map[string]float64 // week -> denom -> face of finds kept

	barberHoardID int64 // trophy lot sold (partially) in sales()
	roundsID      int64 // bullion lot sold (partially) in sales()
}

// date renders start + week*7 + day as an ISO date.
func (g *gen) date(week, day int) string {
	return g.start.AddDate(0, 0, week*7+day).Format(dateLayout)
}

// typ memoizes an item_type insert by name.
func (g *gen) typ(kind, name, metal, fineness string, fineOz float64) int64 {
	if id, ok := g.types[name]; ok {
		return id
	}
	if g.err != nil {
		return 0
	}
	id, err := g.s.InsertItemType(model.ItemType{
		Kind: kind, Name: name, Metal: metal, Fineness: fineness, FineOzEach: fineOz,
	})
	if err != nil {
		g.err = err
		return 0
	}
	g.types[name] = id
	return id
}

// poisson samples a Poisson count (Knuth); good enough for our small lambdas.
func (g *gen) poisson(lambda float64) int {
	if lambda <= 0 {
		return 0
	}
	l := math.Exp(-lambda)
	k, p := 0, 1.0
	for {
		p *= g.rng.Float64()
		if p < l || k > 60 {
			return k
		}
		k++
	}
}

func (g *gen) insertBuy(week, day int, bank, denom, unit, source string, amount, face float64, notes string) buyRec {
	if g.err != nil {
		return buyRec{}
	}
	id, err := g.s.InsertRollTxn(model.RollTxn{
		Date: g.date(week, day), Bank: bank, Action: "buy", Denom: denom,
		Unit: unit, Amount: amount, FaceUSD: face, SourceType: source, Notes: notes,
	})
	if err != nil {
		g.err = err
		return buyRec{}
	}
	b := buyRec{id, week, denom, source, face, bank, g.date(week, day)}
	g.buys = append(g.buys, b)
	return b
}

// find records a CRH find lot attributed to buy b. Face (= basis) is qty ×
// coinFace and is tallied so returns() can keep the float honest.
func (g *gen) find(b buyRec, typeID int64, qty, coinFace float64, cat, subcat string, trophy bool, notes string) int64 {
	if g.err != nil || qty <= 0 {
		return 0
	}
	face := qty * coinFace
	id, err := g.s.InsertHolding(model.Holding{
		ItemTypeID: typeID, RollTxnID: b.id, Activity: "crh", Qty: qty,
		BasisUSD: face, FaceValueUSD: face, Acquired: b.date, Source: b.bank,
		Category: cat, Subcategory: subcat, Trophy: trophy, Notes: notes,
	})
	if err != nil {
		g.err = err
		return 0
	}
	if g.findFaceWk == nil {
		g.findFaceWk = map[int]map[string]float64{}
	}
	if g.findFaceWk[b.week] == nil {
		g.findFaceWk[b.week] = map[string]float64{}
	}
	g.findFaceWk[b.week][b.denom] += face
	return id
}

// pick returns a key from weights, sampled proportionally.
func (g *gen) pick(weights map[string]float64) string {
	var total float64
	for _, w := range weights {
		total += w
	}
	r := g.rng.Float64() * total
	// map order is random; iterate a stable slice for determinism.
	for _, k := range sortedKeys(weights) {
		if r -= weights[k]; r < 0 {
			return k
		}
	}
	return ""
}

func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// insertion sort — tiny maps
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// --- fictional reference data --------------------------------------------------

var demoBanks = []string{
	"Riverbend Credit Union", "First Keystone Bank", "Oak & Main Savings",
	"Harvest National", "Bluegrass Federal CU", "Midtown Community Bank",
	"Second Street Trust", "Lakeview Savings", "Iron Bridge Bank", "Maple City CU",
}

// coin face by denom (mirrors web/app presets)
var coinFace = map[string]float64{"halves": 0.5, "quarters": 0.25, "dimes": 0.1, "nickels": 0.05}
var rollFace = map[string]float64{"halves": 10, "quarters": 10, "dimes": 5, "nickels": 2}
var boxFace = map[string]float64{"halves": 500, "quarters": 500, "dimes": 250, "nickels": 100}
var bagFace = map[string]float64{"halves": 1000, "quarters": 1000, "dimes": 1000, "nickels": 200}

// denom mix of buys, and source mix per denom. Dimes are CR-dominated with no
// boxes (matches the field data); halves are the box denom.
var denomWeights = map[string]float64{"dimes": 0.60, "halves": 0.12, "quarters": 0.18, "nickels": 0.10}
var sourceWeights = map[string]map[string]float64{
	"dimes":    {"customer_roll": 0.80, "machine_roll": 0.125, "bag": 0.015, "loose": 0.06},
	"halves":   {"customer_roll": 0.60, "machine_roll": 0.15, "box": 0.25},
	"quarters": {"customer_roll": 0.65, "machine_roll": 0.29, "box": 0.06},
	"nickels":  {"customer_roll": 0.80, "machine_roll": 0.20},
}

// --- phases ---------------------------------------------------------------------

// bullion is the long-term stack: a small DCA story across metals, including a
// gross-weight×purity bar and a lot that gets partially sold in sales().
func (g *gen) bullion() {
	age := g.typ("coin", "1 oz American Gold Eagle", "gold", "22k .9167", 1.0)
	ageQ := g.typ("coin", "1/4 oz American Gold Eagle", "gold", "22k .9167", 0.25)
	round := g.typ("round", "1 oz silver round (generic)", "silver", ".999", 1.0)
	bar := g.typ("bar", "10 oz silver bar", "silver", ".999", 0) // derived from gross×purity
	plat := g.typ("coin", "1 oz Platinum Maple Leaf", "platinum", ".9995", 1.0)
	junk := g.typ("junk", "90% Kennedy half (1964)", "silver", "90%", 0.36169)

	add := func(h model.Holding) int64 {
		if g.err != nil {
			return 0
		}
		id, err := g.s.InsertHolding(h)
		if err != nil {
			g.err = err
		}
		return id
	}
	ago := func(months int) string { return g.now.AddDate(0, -months, 0).Format(dateLayout) }

	add(model.Holding{ItemTypeID: age, Activity: "bullion", Qty: 1, BasisUSD: 1512, PremiumUSD: 62,
		Acquired: ago(70), Source: "Blue Moon Bullion", Location: "home safe", Notes: "first DCA buy"})
	add(model.Holding{ItemTypeID: age, Activity: "bullion", Qty: 1, BasisUSD: 1985, PremiumUSD: 75,
		Acquired: ago(32), Source: "Blue Moon Bullion", Location: "home safe"})
	add(model.Holding{ItemTypeID: ageQ, Activity: "bullion", Qty: 2, BasisUSD: 1124, PremiumUSD: 88,
		Acquired: ago(50), Source: "Coin show table 12", Location: "home safe"})
	g.roundsID = add(model.Holding{ItemTypeID: round, Activity: "bullion", Qty: 40, BasisUSD: 1028, PremiumUSD: 120,
		Acquired: ago(44), Source: "Blue Moon Bullion", Location: "home safe", Notes: "tube stack, avg cost"})
	add(model.Holding{ItemTypeID: bar, Activity: "bullion", Qty: 1, GrossWeight: 10, Purity: 0.999, WeightUnit: "ozt",
		BasisUSD: 262, PremiumUSD: 18, Acquired: ago(58), Source: "Estate sale", Location: "home safe"})
	add(model.Holding{ItemTypeID: plat, Activity: "bullion", Qty: 1, BasisUSD: 988, PremiumUSD: 55,
		Acquired: ago(38), Source: "Blue Moon Bullion", Location: "safe deposit box"})
	add(model.Holding{ItemTypeID: junk, Activity: "bullion", Qty: 20, BasisUSD: 158, PremiumUSD: 12,
		Acquired: ago(26), Source: "Coin show table 12", Location: "home safe", Notes: "$10 face, bought at melt+"})
}

// hunt generates ~8 buys/week for 65 weeks plus the hand-authored trophy buys.
func (g *gen) hunt() {
	for w := 0; w < weeks; w++ {
		n := 7 + g.rng.Intn(3)
		for i := 0; i < n; i++ {
			denom := g.pick(denomWeights)
			source := g.pick(sourceWeights[denom])
			bank := demoBanks[g.rng.Intn(len(demoBanks))]
			day := g.rng.Intn(6)
			switch source {
			case "box":
				g.insertBuy(w, day, bank, denom, "box", source, 1, boxFace[denom], "")
			case "bag":
				g.insertBuy(w, day, bank, denom, "bag", source, 1, bagFace[denom], "")
			case "loose":
				face := 2 + g.rng.Float64()*6
				face = math.Round(face*10) / 10
				g.insertBuy(w, day, bank, denom, "face", source, face, face, "reject tray")
			default: // customer_roll / machine_roll
				rolls := 4 + g.rng.Intn(14)
				g.insertBuy(w, day, bank, denom, "roll", source, float64(rolls), float64(rolls)*rollFace[denom], "")
			}
		}
	}
	g.trophies()
}

// trophies are hand-authored highlight finds on dedicated buys, echoing the
// shapes real hunters report (a Barber hoard in one roll, a Seated Liberty, a
// bag find, a dramatic error).
func (g *gen) trophies() {
	barber := g.typ("junk", "Barber dime", "silver", "90%", 0.07234)
	seated := g.typ("junk", "Seated Liberty dime", "silver", "90%", 0.07234)
	errHalf := g.typ("junk", "Error coin (clad)", "", "", 0)

	b1 := g.insertBuy(12, 2, "Copper Kettle Grocery (courtesy counter)", "dimes", "roll", "customer_roll", 5, 25, "grocery counter tip-off")
	g.barberHoardID = g.find(b1, barber, 19, 0.1, "Silver", "Barber", true,
		"19 Barbers in one hand-wrapped roll — the roll of a lifetime")

	b2 := g.insertBuy(30, 4, "Riverbend Credit Union", "dimes", "roll", "customer_roll", 10, 50, "")
	g.find(b2, seated, 1, 0.1, "Silver", "Seated Liberty", true, "1891 Seated Liberty — into the holder it goes")

	b3 := g.insertBuy(55, 1, "Bluegrass Federal CU", "dimes", "bag", "bag", 1, 1000, "")
	g.find(b3, seated, 1, 0.1, "Silver", "Seated Liberty", true, "1887-S out of a $1k bag, plus friends")

	b4 := g.insertBuy(44, 3, "Iron Bridge Bank", "halves", "box", "box", 1, 500, "")
	g.find(b4, errHalf, 1, 0.5, "Error", "major", true, "Kennedy struck ~15% off-center")
}

// rate tables: face dollars you must search to find one, per source_type
// (absent source = never found there). Borrowed from the field data, lightly
// smoothed. perBuy categories sample on every buy; the rest group per 4-week
// block to keep the grid at a realistic-but-snappy row count.
type rateSpec struct {
	cat, subcat string
	typeName    string
	perBuy      bool
	rate        map[string]float64
}

func (g *gen) finds() {
	// dime silver types are used directly by dimeSilver below; everything else
	// is registered here and referenced by name via the rate specs' typeName.
	rosie := g.typ("junk", "90% Roosevelt dime (pre-1965)", "silver", "90%", 0.07234)
	merc := g.typ("junk", "Mercury dime", "silver", "90%", 0.07234)
	barber := g.typ("junk", "Barber dime", "silver", "90%", 0.07234)
	g.typ("junk", "Canadian silver dime (pre-1968)", "silver", "80% (CAD)", 0.06)
	g.typ("junk", "2009 Roosevelt dime", "", "", 0)
	g.typ("junk", "Proof coin (clad)", "", "", 0)
	g.typ("junk", "Canadian dime (clad)", "", "", 0)
	g.typ("junk", "World coin", "", "", 0)
	g.typ("junk", "Error coin (clad)", "", "", 0)
	g.typ("junk", "PMD coin (damaged clad)", "", "", 0)
	g.typ("junk", "AU+ high-grade clad", "", "", 0)
	// other denoms
	g.typ("junk", "90% Washington quarter (pre-1965)", "silver", "90%", 0.18084)
	g.typ("junk", "W-mintmark quarter", "", "", 0)
	g.typ("junk", "90% Kennedy half (1964)", "silver", "90%", 0.36169)
	g.typ("junk", "40% Kennedy half (1965–70)", "silver", "40%", 0.1479)
	g.typ("junk", "35% war nickel (1942–45)", "silver", "35%", 0.05626)

	// Silver subcat split for dimes (Roosevelt-dominated; the odd Merc/Barber).
	dimeSilver := func(b buyRec, k int) {
		counts := map[string]float64{}
		for i := 0; i < k; i++ {
			counts[g.pick(map[string]float64{"Roosevelt 90%": 0.86, "Mercury": 0.10, "Barber": 0.04})]++
		}
		typeFor := map[string]int64{"Roosevelt 90%": rosie, "Mercury": merc, "Barber": barber}
		for _, sub := range sortedKeys(counts) {
			g.find(b, typeFor[sub], counts[sub], 0.1, "Silver", sub, false, "")
		}
	}

	perBuy := map[string][]rateSpec{
		"dimes": {
			{cat: "Silver", perBuy: true, rate: map[string]float64{"customer_roll": 140, "machine_roll": 450, "bag": 165, "loose": 6}},
			{cat: "Proof", typeName: "Proof coin (clad)", perBuy: true, rate: map[string]float64{"customer_roll": 1400, "machine_roll": 1187}},
			{cat: "Error", subcat: "minor", typeName: "Error coin (clad)", perBuy: true, rate: map[string]float64{"customer_roll": 756, "machine_roll": 1187}},
			{cat: "Error", subcat: "major", typeName: "Error coin (clad)", perBuy: true, rate: map[string]float64{"customer_roll": 1937, "machine_roll": 400}},
			{cat: "Other Silver", subcat: "CAD 80%", typeName: "Canadian silver dime (pre-1968)", perBuy: true, rate: map[string]float64{"customer_roll": 4000, "loose": 4}},
		},
		"quarters": {
			{cat: "Silver", subcat: "Washington 90%", typeName: "90% Washington quarter (pre-1965)", perBuy: true, rate: map[string]float64{"customer_roll": 700, "box": 1200, "machine_roll": 3000}},
			{cat: "Key Date", subcat: "W mintmark", typeName: "W-mintmark quarter", perBuy: true, rate: map[string]float64{"customer_roll": 450}},
			{cat: "Error", subcat: "minor", typeName: "Error coin (clad)", perBuy: true, rate: map[string]float64{"customer_roll": 900}},
		},
		"halves": {
			{cat: "Silver", subcat: "40% Kennedy", typeName: "40% Kennedy half (1965–70)", perBuy: true, rate: map[string]float64{"customer_roll": 180, "box": 350, "machine_roll": 700}},
			{cat: "Silver", subcat: "90% Kennedy", typeName: "90% Kennedy half (1964)", perBuy: true, rate: map[string]float64{"customer_roll": 450, "box": 900, "machine_roll": 2000}},
			{cat: "Proof", typeName: "Proof coin (clad)", perBuy: true, rate: map[string]float64{"box": 800, "customer_roll": 1500}},
		},
		"nickels": {
			{cat: "Silver", subcat: "war nickel", typeName: "35% war nickel (1942–45)", perBuy: true, rate: map[string]float64{"customer_roll": 500}},
		},
	}
	perBlock := map[string][]rateSpec{
		"dimes": {
			{cat: "Key Date", subcat: "2009", typeName: "2009 Roosevelt dime", rate: map[string]float64{"customer_roll": 274, "machine_roll": 2374, "bag": 241}},
			{cat: "CAD", typeName: "Canadian dime (clad)", rate: map[string]float64{"customer_roll": 85, "machine_roll": 475, "bag": 7000, "loose": 1.2}},
			{cat: "World", typeName: "World coin", rate: map[string]float64{"customer_roll": 301, "machine_roll": 1187, "bag": 2333}},
			{cat: "AU+", typeName: "AU+ high-grade clad", rate: map[string]float64{"customer_roll": 574, "bag": 1167}},
			{cat: "PMD", typeName: "PMD coin (damaged clad)", rate: map[string]float64{"customer_roll": 59, "machine_roll": 198, "bag": 79, "loose": 5}},
		},
		"quarters": {
			{cat: "World", typeName: "World coin", rate: map[string]float64{"customer_roll": 200}},
			{cat: "PMD", typeName: "PMD coin (damaged clad)", rate: map[string]float64{"customer_roll": 90, "box": 110}},
		},
		"halves": {
			{cat: "PMD", typeName: "PMD coin (damaged clad)", rate: map[string]float64{"box": 150, "customer_roll": 120}},
		},
		"nickels": {
			{cat: "PMD", typeName: "PMD coin (damaged clad)", rate: map[string]float64{"customer_roll": 80}},
		},
	}
	pmdSubcats := map[string]float64{"parking lot": 0.45, "Slider": 0.12, "bent": 0.10, "fire": 0.07, "roller": 0.05, "Oreo": 0.03, "": 0.18}

	// Per-buy rare categories.
	for _, b := range g.buys {
		for _, spec := range perBuy[b.denom] {
			r, ok := spec.rate[b.source]
			if !ok {
				continue
			}
			k := g.poisson(b.face / r)
			if k == 0 {
				continue
			}
			if spec.cat == "Silver" && b.denom == "dimes" {
				dimeSilver(b, k)
				continue
			}
			g.find(b, g.types[spec.typeName], float64(k), coinFace[b.denom], spec.cat, spec.subcat, false, "")
		}
	}

	// Per-block commons: expected count from every buy in the block, attached to
	// the block's biggest customer-roll buy (finds cluster in the fat rolls).
	for blk := 0; blk*blockWks < weeks; blk++ {
		lo, hi := blk*blockWks, (blk+1)*blockWks
		byDenom := map[string][]buyRec{}
		for _, b := range g.buys {
			if b.week >= lo && b.week < hi {
				byDenom[b.denom] = append(byDenom[b.denom], b)
			}
		}
		// Fixed denom order: map iteration would reorder the rng draws and make
		// the "deterministic" seed drift between runs.
		for _, denom := range []string{"dimes", "halves", "quarters", "nickels"} {
			bs := byDenom[denom]
			if len(bs) == 0 {
				continue
			}
			anchor := bs[0]
			for _, b := range bs {
				better := b.source == "customer_roll" && anchor.source != "customer_roll"
				if better || (b.source == anchor.source && b.face > anchor.face) {
					anchor = b
				}
			}
			for _, spec := range perBlock[denom] {
				var lambda float64
				for _, b := range bs {
					if r, ok := spec.rate[b.source]; ok {
						lambda += b.face / r
					}
				}
				k := g.poisson(lambda)
				if k == 0 {
					continue
				}
				if spec.cat == "PMD" {
					// split the block's PMD across damage subcats
					counts := map[string]float64{}
					for i := 0; i < k; i++ {
						counts[g.pick(pmdSubcats)]++
					}
					for _, sub := range sortedKeys(counts) {
						g.find(anchor, g.types[spec.typeName], counts[sub], coinFace[denom], "PMD", sub, false, "")
					}
					continue
				}
				g.find(anchor, g.types[spec.typeName], float64(k), coinFace[denom], spec.cat, spec.subcat, false, "")
			}
		}
	}
}

// returns writes the weekly dump runs: everything bought that week minus what
// was kept as finds goes back to the bank. The final week is left outstanding
// (the float the Do tab offers to return), and the keeper/loss face — coin kept
// or lost rather than returned — is skimmed off the most recent returns so the
// all-time reconciliation identity holds exactly.
func (g *gen) returns() {
	boughtWk := map[int]map[string]float64{}
	for _, b := range g.buys {
		if boughtWk[b.week] == nil {
			boughtWk[b.week] = map[string]float64{}
		}
		boughtWk[b.week][b.denom] += b.face
	}

	type ret struct {
		week  int
		denom string
		face  float64
	}
	var rets []ret
	for w := 0; w < weeks-1; w++ { // last week stays outstanding
		for _, denom := range sortedKeys(boughtWk[w]) {
			face := boughtWk[w][denom] - g.findFaceWk[w][denom]
			if face <= 0 {
				continue
			}
			rets = append(rets, ret{w, denom, face})
		}
	}

	// Skim keeper+loss face (kept in albums / genuinely lost — never returned)
	// off the latest returns.
	skim := keeperTotalFace + lossTotalFace
	for i := len(rets) - 1; i >= 0 && skim > 0; i-- {
		take := math.Min(skim, rets[i].face-1)
		if take <= 0 {
			continue
		}
		rets[i].face -= take
		skim -= take
	}

	for _, r := range rets {
		if g.err != nil {
			return
		}
		face := math.Round(r.face*100) / 100
		_, err := g.s.InsertRollTxn(model.RollTxn{
			Date: g.date(r.week, 6), Bank: dumpBank, Action: "return", Denom: r.denom,
			Unit: "face", Amount: face, FaceUSD: face, Notes: "weekly dump run",
		})
		if err != nil {
			g.err = err
		}
	}
}

func (g *gen) trips() {
	for w := 0; w < weeks; w++ {
		n := 1 + g.rng.Intn(2)
		for i := 0; i < n; i++ {
			if g.err != nil {
				return
			}
			miles := math.Round((3+g.rng.Float64()*6)*10) / 10
			hours := math.Round((0.2+miles*0.06+g.rng.Float64()*0.2)*100) / 100
			_, err := g.s.InsertTrip(model.Trip{
				Date: g.date(w, g.rng.Intn(7)), Bank: demoBanks[g.rng.Intn(len(demoBanks))],
				Miles: miles, Hours: hours,
			})
			if err != nil {
				g.err = err
			}
		}
	}
}

const keeperTotalFace = 46.0 // 31 + 9 + 6, below
const lossTotalFace = 25.50  // sum of the loss rows below

func (g *gen) extras() {
	if g.err != nil {
		return
	}
	supplies := []struct {
		week int
		item string
		cost float64
	}{
		{1, "Coin tubes (halves + dimes)", 24.50},
		{9, "Nitrile gloves", 11.99},
		{18, "Crimping coin wrappers, 500ct", 16.25},
		{33, "2x2 flips + storage box", 21.40},
		{47, "Loupe (10x)", 12.99},
		{58, "More dime wrappers", 8.75},
	}
	for _, x := range supplies {
		if _, err := g.s.InsertSupply(model.Supply{Date: g.date(x.week, 3), Item: x.item, CostUSD: x.cost}); err != nil {
			g.err = err
			return
		}
	}

	keepers := []model.Keeper{
		{Denom: "halves", Count: 62, FaceUSD: 31}, // NIFC + album fillers
		{Denom: "dimes", Count: 90, FaceUSD: 9},
		{Denom: "nickels", Count: 120, FaceUSD: 6},
	}
	for _, k := range keepers {
		if _, err := g.s.InsertKeeper(k); err != nil {
			g.err = err
			return
		}
	}

	losses := []struct {
		week   int
		amount float64
		reason string
		scope  string
	}{
		{16, 2.40, "coin machine miscount", "spring dime run"},
		{28, 10.00, "short deposit — teller recount disagreed", "week 28 dump run"},
		{41, 1.50, "dropped roll under the car seat, gone", "halves run"},
		{60, 11.60, "reconcile write-off", "quarterly close"},
	}
	for _, l := range losses {
		if _, err := g.s.InsertLoss(model.Loss{Date: g.date(l.week, 5), AmountUSD: l.amount, Reason: l.reason, Scope: l.scope}); err != nil {
			g.err = err
			return
		}
	}
}

// spotHistory backfills ~monthly observations so valuations work before the
// background poller's first fetch and the future over-time chart has a curve.
func (g *gen) spotHistory() {
	if g.err != nil {
		return
	}
	months := 15
	for i := 0; i <= months; i++ {
		t := float64(i) / float64(months)    // 0 → 1 across the period
		wiggle := math.Sin(float64(i) * 1.7) // deterministic wobble
		sp := model.Spot{
			AsOf:         g.now.AddDate(0, i-months, -2).UTC().Format(time.RFC3339),
			GoldUSD:      math.Round((2950+t*1060+wiggle*45)*100) / 100,
			SilverUSD:    math.Round((31+t*27+wiggle*1.4)*100) / 100,
			PlatinumUSD:  math.Round((990+t*450+wiggle*25)*100) / 100,
			PalladiumUSD: math.Round((960+t*330+wiggle*30)*100) / 100,
			Source:       "demo-seed",
		}
		if err := g.s.PutSpot(sp); err != nil {
			g.err = err
			return
		}
	}
}

// sales books realized P&L on both sides: bullion rounds sold on a spike, and
// most of the Barber hoard sold well above melt (numismatic premium). Partial
// sales split the lots, exercising the disposed/survivorship paths.
func (g *gen) sales() {
	if g.err != nil {
		return
	}
	if g.roundsID != 0 {
		if err := g.s.SellHolding(g.roundsID, 8, 470, g.now.AddDate(0, -2, 0).Format(dateLayout)); err != nil {
			g.err = err
			return
		}
	}
	if g.barberHoardID != 0 {
		if err := g.s.SellHolding(g.barberHoardID, 12, 210, g.now.AddDate(0, -3, 12).Format(dateLayout)); err != nil {
			g.err = err
		}
	}
}
