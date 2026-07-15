// End-to-end regression guard for the workflow-first "Do" tab (bead om-tuu9).
// Drives every tile in a real headless browser against a running `serve`
// instance, asserting both the UI flow and the recorded rows (via the REST API),
// and fails on ANY console/page error. Assumes a freshly-migrated DB with a spot
// price seeded — see run.sh, which sets that up and tears it down.
//
// Run standalone:  BASE_URL=http://127.0.0.1:8799 node do-tab.e2e.mjs
import { chromium } from 'playwright'
import { statSync } from 'node:fs'

const BASE = process.env.BASE_URL || 'http://127.0.0.1:8799'
const SHOT = process.env.SHOT_DIR || '.'
const results = []
const consoleErrors = []
const pageErrors = []
const ok = (n, cond, extra = '') => {
  results.push({ n, pass: !!cond, extra })
  console.log(`${cond ? 'PASS' : 'FAIL'}  ${n}${extra ? '  — ' + extra : ''}`)
}
const api = (p) => fetch(BASE + '/api' + p).then((r) => r.json())
// Seeds a field the UI has no editor for (notes, insured_value…) so a later grid edit
// has something to destroy. PUT is a merge, so naming three fields touches only those.
const apiPut = async (p, body) => {
  const r = await fetch(BASE + '/api' + p, {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!r.ok) throw new Error(`PUT ${p} → ${r.status}`)
}
// Seeds a row directly, for checks about the grid itself (e.g. deleting one) rather
// than about the workflow that would normally create it.
const apiPost = async (p, body) => {
  const r = await fetch(BASE + '/api' + p, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!r.ok) throw new Error(`POST ${p} → ${r.status}`)
  return r.json()
}
// Removes a row directly — used to keep a self-contained assertion (om-5psc AC2) from
// leaving a lot behind that would perturb a later positional selection in this script.
const apiDelete = async (p) => {
  const r = await fetch(BASE + '/api' + p, { method: 'DELETE' })
  if (!r.ok) throw new Error(`DELETE ${p} → ${r.status}`)
}

const browser = await chromium.launch()
const page = await browser.newPage()
page.on('console', (m) => {
  if (m.type() === 'error') consoleErrors.push(m.text())
})
page.on('pageerror', (e) => pageErrors.push(e.message))

const goDo = async () => {
  await page.getByRole('button', { name: 'Do' }).click()
  await page.getByRole('heading', { name: 'What did you do?' }).waitFor({ timeout: 5000 })
}
const tile = (name) => page.locator('button.group', { hasText: name })

// EditableGrid renders its <thead> at mount but fills <tbody> from an async load()
// ($effect in EditableGrid.svelte) — so a check that waits on the header and then reads
// rows/tbody/a <datalist> races that fetch and can sample an EMPTY grid. That one defect
// is behind both known flakes (source-type-inert, mixed-denom) and every latent grid read
// (om-yd1h). Fix the WAIT, never the timeout: gate on the loaded rows. Real rows carry
// data-row-index; the trailing draft row does not, so this counts exactly what load()
// produced. `exact` is for AFTER a delete, where the count DROPS and a `>=` would
// short-circuit on the pre-delete rows.
const dataRowSel = 'section table tbody tr[data-row-index]'
const awaitRowCount = (n, { sel = dataRowSel, exact = false } = {}) =>
  page
    .waitForFunction(
      ([s, want, eq]) => {
        const c = document.querySelectorAll(s).length
        return eq ? c === want : c >= want
      },
      [sel, n, exact],
      { timeout: 5000 },
    )
    .catch(() => {}) // let the ok() below report the miss, with real numbers

// Poll a REST endpoint until it settles, instead of sleeping a fixed guess after a write.
// Returns the last value seen either way, so the ok() reports real state on a miss.
const awaitApi = async (path, pred, { tries = 60, gap = 100 } = {}) => {
  let v
  for (let i = 0; i < tries; i++) {
    v = await api(path)
    if (pred(v)) break
    await new Promise((r) => setTimeout(r, gap))
  }
  return v
}

try {
  await page.goto(BASE, { waitUntil: 'networkidle' })
  await page.getByRole('heading', { name: 'CoinRollHunter' }).waitFor({ timeout: 8000 })
  ok('app shell renders', await page.getByText('Local-first coins').isVisible())

  // === Do tab: all six tiles present ===
  await goDo()
  for (const t of ['Bought a box', 'Logged finds', 'Returned to bank', 'Reconcile', 'New coin', 'Sold something']) {
    ok(`tile present: ${t}`, (await tile(t).count()) > 0)
  }

  // === 1. Bought a box ===  (1 box of halves auto-fills $500, + a trip)
  await tile('Bought a box').click()
  await page.getByRole('heading', { name: 'Bought a box / rolls' }).waitFor()
  await page.getByPlaceholder('Stock Yards').fill('Stock Yards')
  ok('box-face auto-fills', true) // asserted via API below
  // ADR-006: pick the acquisition source-type (the high-yield channel)
  await page
    .locator('select')
    .filter({ has: page.locator('option[value="customer_roll"]') })
    .selectOption('customer_roll')
  await page.getByLabel('Also log the bank trip (gas + time)').check()
  await page.locator('input[type=number]').last().fill('0.5') // hours
  await page.getByRole('button', { name: /Log .* bought/ }).click()
  await page.getByText('Logged', { exact: false }).first().waitFor({ timeout: 5000 })
  const buys1 = (await api('/roll-txns')).filter((r) => r.action === 'buy')
  ok('buy recorded', buys1.length === 1, `${buys1.length} buys`)
  ok('buy face = $500 (auto)', buys1[0]?.face_usd === 500, `face ${buys1[0]?.face_usd}`)
  ok('buy source_type persisted (ADR-006)', buys1[0]?.source_type === 'customer_roll', `src ${buys1[0]?.source_type}`)
  ok('optional trip recorded', (await api('/trips')).length === 1)
  await page.getByRole('button', { name: 'Done' }).click()

  // === 2. Logged finds ===  (90% half x3 + 10 clad halves, linked to the box)
  await goDo()
  await tile('Logged finds').click()
  await page.getByRole('heading', { name: 'Logged finds' }).waitFor()
  const prod = page.getByPlaceholder('90% half (1964 & earlier)')
  await prod.fill('90% half (1964 & earlier)')
  await prod.dispatchEvent('input')
  const sb = () => page.getByRole('spinbutton')
  await sb().nth(0).fill('3') // find qty
  await sb().nth(0).dispatchEvent('input')
  await page.getByRole('button', { name: 'Add a keeper' }).click()
  await sb().nth(2).fill('10') // keeper count (find: qty,face ; keeper: count,face)
  await sb().nth(2).dispatchEvent('input')
  await page.getByRole('button', { name: 'Save finds' }).click()
  await page.getByText('in find face', { exact: false }).waitFor({ timeout: 5000 })
  const finds = (await api('/lots')).filter((l) => l.activity === 'crh')
  ok('find recorded (crh holding)', finds.length === 1, `${finds.length} finds`)
  ok('find linked to box', finds[0]?.roll_txn_id === buys1[0].id, `roll_txn_id ${finds[0]?.roll_txn_id}`)
  const keepers = await api('/keepers')
  ok('keeper recorded', keepers.length === 1, `${keepers.length} keepers`)
  ok('keeper face auto (10 halves=$5)', Math.abs((keepers[0]?.face_usd ?? 0) - 5) < 0.01, `face ${keepers[0]?.face_usd}`)
  await page.getByRole('button', { name: 'Done' }).click()
  const sum2 = await api('/summary')
  ok('box yield computed', (sum2.box_yields || []).some((b) => b.find_count > 0))

  // === 2b. A kept notable find writes ZERO keepers (om-5psc, AC2) ===
  // The double-count seam this bead closes: a find you keep is ONE flagged find row,
  // never a find PLUS a keeper for the same coin. Log one kept find with NO keeper and
  // prove the submission created no keeper, stored the flag, moved kept_face by exactly
  // the find's face, and left clad_face untouched.
  await goDo()
  await tile('Logged finds').click()
  await page.getByRole('heading', { name: 'Logged finds' }).waitFor()
  const keepersBefore2b = (await api('/keepers')).length
  const sumB4 = await api('/summary')
  const prodK = page.getByPlaceholder('90% half (1964 & earlier)')
  await prodK.fill('1943-S Merc (kept)')
  await prodK.dispatchEvent('input')
  await sb().nth(0).fill('1') // find qty
  await sb().nth(0).dispatchEvent('input')
  await sb().nth(1).fill('2') // find face $ (manual)
  await sb().nth(1).dispatchEvent('input')
  // "Keep" is checked by default for a logged find — the whole point of this section.
  const keepBox = page.getByRole('checkbox').first()
  ok('AC2 — "Keep" defaults on for a logged find', await keepBox.isChecked())
  // Deliberately add NO keeper row: the old path (a keeper alongside the find) is gone.
  await page.getByRole('button', { name: 'Save finds' }).click()
  await page.getByText('in find face', { exact: false }).waitFor({ timeout: 5000 })
  const keepersAfter2b = (await api('/keepers')).length
  ok('AC2 — a kept find created ZERO keeper rows', keepersAfter2b === keepersBefore2b,
     `${keepersBefore2b} -> ${keepersAfter2b}`)
  const keptFind = (await api('/lots')).find((l) => Math.abs((l.face_value_usd ?? 0) - 2) < 0.01 && l.activity === 'crh')
  ok('AC2 — the find is stored WITH the kept flag', keptFind?.kept === true, `kept ${JSON.stringify(keptFind?.kept)}`)
  const sum2b = await api('/summary')
  ok('AC2 — kept_face rose by EXACTLY the find face ($2)',
     Math.abs((sum2b.kept_face - sumB4.kept_face) - 2) < 0.01, `d kept_face ${(sum2b.kept_face - sumB4.kept_face).toFixed(2)}`)
  ok('AC2 — clad_face did NOT move (no keeper written)',
     Math.abs(sum2b.clad_face - sumB4.clad_face) < 0.01, `d clad_face ${(sum2b.clad_face - sumB4.clad_face).toFixed(2)}`)
  // Isolate: remove this assertion's find so the linear script's later positional lot
  // selection (the Sell step) sees exactly the lots it did before this block.
  if (keptFind?.id) await apiDelete('/lots/' + keptFind.id)
  await page.getByRole('button', { name: 'Done' }).click()

  // === 3. New coin / bullion ===  (1 oz gold eagle, basis $3950)
  await goDo()
  await tile('New coin').click()
  await page.getByRole('heading', { name: 'New coin / bullion' }).waitFor()
  await page.getByPlaceholder('1 oz American Gold Eagle').fill('1 oz Gold Eagle')
  await page.locator('input[type=number]').nth(0).fill('1') // fine oz/unit
  await page.locator('input[type=number]').nth(1).fill('1') // qty
  await page.locator('input[type=number]').nth(2).fill('3950') // basis
  await page.getByRole('button', { name: 'Add to stack' }).click()
  await page.getByText('Added', { exact: false }).first().waitFor({ timeout: 5000 })
  ok('bullion holding added', (await api('/lots')).filter((l) => l.activity === 'bullion').length === 1)
  await page.getByRole('button', { name: 'Done' }).click()

  // === 4. Returned to bank ===  (partial return $400)
  await goDo()
  await tile('Returned to bank').click()
  await page.getByRole('heading', { name: 'Return culls to the bank' }).waitFor()
  await page.getByRole('spinbutton').first().fill('400')
  await page.getByRole('button', { name: /Return .* to bank/ }).click()
  await page.getByText('Recorded', { exact: false }).first().waitFor({ timeout: 5000 })
  const returns4 = (await api('/roll-txns')).filter((r) => r.action === 'return')
  ok('return recorded', returns4.length === 1 && returns4[0].face_usd === 400, `@ ${returns4[0]?.face_usd}`)
  // A redeposit is a lump of face going back — the denom defaults to "Mixed" ("") so
  // a mixed pile records as one sum (single-pool float; the math never reads it).
  ok('return is denomless (mixed pile — single-pool float)', returns4[0]?.denom === '', `denom ${JSON.stringify(returns4[0]?.denom)}`)
  await page.getByRole('button', { name: 'Done' }).click()

  // === 5. Reconcile / close out ===  (record a forgotten keeper, then book the rest)
  const sumBefore = await api('/summary')
  ok('float still open before reconcile', sumBefore.to_redeposit > 0.01, `to_redeposit ${sumBefore.to_redeposit}`)
  await goDo()
  await tile('Reconcile').click()
  await page.getByRole('heading', { name: 'Reconcile / close the books' }).waitFor()
  // step 1: a forgotten keeper (5 halves = $2.50) shrinks the float — NOT a loss
  await page.getByRole('spinbutton').nth(0).fill('5')
  await page.getByRole('button', { name: 'Add', exact: true }).first().click()
  await awaitApi('/keepers', (k) => k.length === 2) // wait for the write, not a fixed guess
  ok('reconcile recorded forgotten keeper', (await api('/keepers')).length === 2)
  const sumMid = await api('/summary')
  ok('keeper reduced float (not a loss)', (sumMid.losses ?? 0) === 0 && sumMid.to_redeposit < sumBefore.to_redeposit,
     `losses ${sumMid.losses}, float ${sumMid.to_redeposit}`)
  // step 2: write off the rest
  await page.getByRole('button', { name: /Book .* loss/ }).click()
  await page.getByText('books closed', { exact: false }).waitFor({ timeout: 5000 })
  const sumAfter = await api('/summary')
  ok('loss booked', (sumAfter.losses ?? 0) > 0, `losses ${sumAfter.losses}`)
  ok('float reconciled to ~$0', Math.abs(sumAfter.to_redeposit) < 0.01, `to_redeposit ${sumAfter.to_redeposit}`)
  ok('reconciled flag true', sumAfter.reconciled === true)
  ok('CRH net dropped by the loss', Math.abs(sumMid.crh_net_real - sumAfter.losses - sumAfter.crh_net_real) < 0.01,
     `mid ${sumMid.crh_net_real} loss ${sumAfter.losses} after ${sumAfter.crh_net_real}`)
  await page.getByRole('button', { name: 'Done' }).click()

  // === 6. Sold something ===  (sell the gold eagle: 4200 − 3950 = +250)
  await goDo()
  await tile('Sold something').click()
  await page.getByRole('heading', { name: 'Sold something' }).waitFor()
  await page.locator('select').first().selectOption({ index: 1 }) // the bullion lot, not the find
  await page.getByRole('spinbutton').nth(1).fill('4200') // proceeds
  await page.getByRole('button', { name: 'Record sale' }).click()
  await page.getByText('Realized', { exact: false }).first().waitFor({ timeout: 5000 })
  const sumSold = await api('/summary')
  ok('sale recorded (realized)', (sumSold.realized || []).length === 1)
  ok('realized gain ~ +$250', Math.abs(sumSold.realized_gain - 250) < 0.01, `gain ${sumSold.realized_gain}`)
  // om-nass: the lot just sold is BULLION, so it must not touch the CRH lifetime
  // figure — a bullion sale is an investment result, not a hunt result. And the
  // realized split is total (bullion := not-crh), so the two halves must add up.
  ok('bullion sale does not move CRH lifetime',
     Math.abs(sumSold.crh_net_lifetime - sumSold.crh_net_real) < 0.01,
     `lifetime ${sumSold.crh_net_lifetime} vs live ${sumSold.crh_net_real}`)
  ok('realized split adds up',
     Math.abs(sumSold.realized_gain - (sumSold.realized_gain_crh + sumSold.realized_gain_bullion)) < 0.01,
     `${sumSold.realized_gain} vs ${sumSold.realized_gain_crh}+${sumSold.realized_gain_bullion}`)
  await page.getByRole('button', { name: 'Done' }).click()

  // === Edit layer: Losses grid round-trips ===
  await page.getByRole('button', { name: 'Edit' }).click()
  await page.getByRole('button', { name: 'Losses', exact: true }).click()
  await page.locator('section table thead th').first().waitFor({ timeout: 5000 })
  const apiLosses = (await api('/losses')).length
  await awaitRowCount(apiLosses) // the header is up; wait for load() to land the rows
  const lossRows = await page.locator('section table tbody tr:has(button[title="Delete row"])').count()
  ok('Losses grid round-trips', lossRows === apiLosses && apiLosses >= 1, `dom ${lossRows} vs api ${apiLosses}`)

  // === Overview reflects shrinkage ===
  await page.getByRole('button', { name: 'Overview', exact: true }).click()
  await page.getByText('All cashed in', { exact: false }).first().waitFor({ timeout: 5000 })
  ok('overview reconciliation banner shows lost', (await page.getByText('lost', { exact: false }).count()) > 0)
  // om-nass: both verdict cards render — the all-time question now lives on the
  // lifetime card, not over the live-only number.
  ok('Overview shows both CRH verdicts', (await page.getByText('Lifetime', { exact: false }).count()) > 0)

  // === ADR-006/007 surfacing (om-5tl2) ===
  // KPI cards from /api/summary
  const sumKpi = await api('/summary')
  ok('KPI: buy_count present', sumKpi.buy_count >= 1, `buy_count ${sumKpi.buy_count}`)
  ok('KPI cards render (Buys/Branches/Avg buy)',
    (await page.getByText('Buys', { exact: true }).count()) > 0 &&
    (await page.getByText('Branches', { exact: true }).count()) > 0 &&
    (await page.getByText('Avg buy', { exact: true }).count()) > 0)
  // spot freshness chip — matched by its unique (static) title attribute.
  ok('spot freshness chip visible (ADR-007)', await page.locator('span[title*="background"]').first().isVisible())
  // hit-rate report: the endpoint (data) plus the grid, which lives on the
  // Insights tab — the analysis altitude was lifted out of Overview in the ADR-012
  // IA refactor (commit 9810aa7 / om-bm7n), so navigate there before asserting it.
  const fr = await api('/finds-report')
  ok('finds-report endpoint shape', typeof fr.total_face_searched === 'number' && Array.isArray(fr.denoms),
    `face ${fr.total_face_searched}, denoms ${fr.denoms?.length}`)
  await page.screenshot({ path: `${SHOT}/do-overview.png`, fullPage: true }).catch(() => {})
  await page.getByRole('button', { name: 'Insights', exact: true }).click()
  await page.getByRole('heading', { name: 'Hit rate — 1 per face $' }).waitFor({ timeout: 5000 })
  ok('hit-rate grid heading renders', await page.getByRole('heading', { name: 'Hit rate — 1 per face $' }).isVisible())

  // === Holdings grid: category/subcategory/trophy are enterable + persist ===
  await page.getByRole('button', { name: 'Edit' }).click()
  await page.getByRole('button', { name: 'Holdings', exact: true }).click()
  await page.locator('section table thead th').first().waitFor({ timeout: 5000 })
  const newRow = page.locator('tbody tr').filter({ has: page.locator('button[title="Add row"]') })
  await newRow.locator('select').first().selectOption('crh') // activity
  await newRow.getByPlaceholder('1 oz Gold Eagle').fill('Mercury dime (trophy)')
  await newRow.getByPlaceholder('Silver').fill('Silver')
  await newRow.getByPlaceholder('Mercury').fill('Mercury')
  // Two checkbox columns now (Trophy, then Keep — om-5psc); Trophy is first. This block
  // pins the trophy flag, so target it explicitly rather than the ambiguous "the checkbox".
  await newRow.locator('input[type=checkbox]').first().check()
  await newRow.locator('button[title="Add row"]').click()
  const isTaxLot = (l) => l.category === 'Silver' && l.subcategory === 'Mercury' && l.trophy === true
  await awaitApi('/lots', (ls) => ls.some(isTaxLot)) // wait for create+reload, not a fixed guess
  const taxLot = (await api('/lots')).find(isTaxLot)
  ok('Holdings taxonomy persists (category/subcategory/trophy)', !!taxLot, taxLot ? `lot ${taxLot.id}` : 'not found')
  // The new find has basis 0 (grid default) — summary must still serialize (no +Inf in unreal_pct).
  const sumZeroBasis = await api('/summary')
  ok('summary survives a zero-basis find (unreal_pct null, not +Inf)',
    Array.isArray(sumZeroBasis.lots) && sumZeroBasis.lots.length >= 1,
    `lots ${sumZeroBasis.lots?.length}`)

  // === Photos (om-6hlp): a lot carries N photos; the grid opens a detail drawer ===
  // A tiny valid PNG, uploaded through the LIVE server against the trophy lot's stable uid.
  const TEST_PNG_B64 =
    'iVBORw0KGgoAAAANSUhEUgAAADAAAAAwCAIAAADYYG7QAAAAUklEQVR4nOzOoQ3AMAADQYN0745edYKnAXeyjP9s77Pds/PfTQQVQUVQEVQEFUFFUBFUBBVBRVARVAQVQUVQEVQEFUFFUBFUBBVBRVD5AgAA//9IYgMXBWAfpAAAAABJRU5ErkJggg=='
  const photoLot = (await api('/lots')).find(isTaxLot)
  ok('a fresh lot carries a stable uid for photos to key off', /^[0-9a-f-]{36}$/.test(photoLot?.uid || ''), photoLot?.uid)
  {
    const fd = new FormData()
    fd.append('owner_kind', 'lot')
    fd.append('owner_uid', photoLot.uid)
    fd.append('role', 'obverse')
    fd.append('file', new Blob([Buffer.from(TEST_PNG_B64, 'base64')], { type: 'image/png' }), 'coin.png')
    const upResp = await fetch(BASE + '/api/photos', { method: 'POST', body: fd })
    const photo = await upResp.json()
    ok('photo upload: a lot photo lands at seq 1 with a fresh uid',
      upResp.status === 201 && photo.seq === 1 && /^[0-9a-f-]{36}$/.test(photo.uid || ''),
      `status ${upResp.status} seq ${photo.seq}`)
    ok('the gallery lists the uploaded photo',
      (await api(`/photos?owner_kind=lot&owner_uid=${photoLot.uid}`)).length === 1)

    // om-9o4n.1: a role picked at upload reaches the server and is stored — a receipt files
    // as a receipt, no upload-then-re-role two-step. Cleaned up immediately so the obverse
    // soft-delete flow below still sees the gallery go 1 → 0.
    {
      const rfd = new FormData()
      rfd.append('owner_kind', 'lot')
      rfd.append('owner_uid', photoLot.uid)
      rfd.append('role', 'receipt')
      rfd.append('file', new Blob([Buffer.from(TEST_PNG_B64, 'base64')], { type: 'image/png' }), 'receipt.png')
      const rResp = await fetch(BASE + '/api/photos', { method: 'POST', body: rfd })
      const receipt = await rResp.json()
      ok('a receipt-tagged upload is stored with role=receipt',
        rResp.status === 201 && receipt.role === 'receipt', `status ${rResp.status} role ${receipt.role}`)
      await apiDelete(`/photos/${receipt.id}`)
    }

    const fileResp = await fetch(`${BASE}/api/photos/${photo.uid}/file?variant=display`)
    ok('the photo file serves an image (not the SPA index.html)',
      fileResp.ok && /image\//.test(fileResp.headers.get('content-type') || ''),
      fileResp.headers.get('content-type'))
    const missResp = await fetch(`${BASE}/api/photos/not-a-uid/file`)
    ok('a bad photo uid 404s, never an HTML 200 (spaHandler trap)',
      missResp.status === 404 && !/html/i.test(missResp.headers.get('content-type') || ''),
      `status ${missResp.status}`)

    // The Holdings grid offers a camera cell that opens the coin detail drawer.
    ok('Holdings rows offer a Photos (camera) button', (await page.locator('button[title="Photos"]').count()) > 0)
    await page.locator('button[title="Photos"]').first().click()
    const drawer = page.locator('[aria-label="Coin photos"]')
    await drawer.waitFor({ timeout: 5000 })
    ok('the coin detail drawer opens with an add-photo affordance',
      await drawer.getByRole('button', { name: 'Add a photo' }).isVisible())
    // om-9o4n.1: the add-photo affordance carries a role picker offering Receipt.
    ok('the upload affordance offers a role picker with a Receipt option',
      (await drawer.locator('select option[value="receipt"]').count()) > 0)
    await drawer.locator('button[title="Close"]').click()
    await drawer.waitFor({ state: 'detached', timeout: 5000 })

    // Soft delete: the photo leaves the gallery (its file survives — pinned in the Go suite).
    await apiDelete(`/photos/${photo.id}`)
    ok('a soft-deleted photo leaves the gallery',
      (await api(`/photos?owner_kind=lot&owner_uid=${photoLot.uid}`)).length === 0)

    // AC13: keepers carry NO photo affordance (a batch, not a specimen).
    await page.getByRole('button', { name: 'Keepers', exact: true }).click()
    await page.locator('section table thead th').first().waitFor({ timeout: 5000 })
    ok('keepers have NO photo affordance', (await page.locator('button[title="Photos"]').count()) === 0)
    await page.getByRole('button', { name: 'Holdings', exact: true }).click() // back to where we were
    await page.locator('section table thead th').first().waitFor({ timeout: 5000 })
  }

  // trophy feed surfaces it back on the Insights tab (analysis lives in Insights
  // since the ADR-012 IA refactor — not the read-only Overview).
  await page.getByRole('button', { name: 'Insights', exact: true }).click()
  await page.getByRole('heading', { name: 'Greatest hits' }).waitFor({ timeout: 5000 })
  ok('trophy feed shows the trophy', (await page.getByText('Mercury dime (trophy)', { exact: false }).count()) > 0)

  // === Settings editor (audit gap #8): edits persist via PUT /api/settings ===
  await page.locator('button[title="Settings"]').click()
  const dialog = page.locator('[role="dialog"]')
  await dialog.getByRole('heading', { name: 'Settings' }).waitFor({ timeout: 5000 })

  // === "Your data": the export bundle downloads, and the EXIF caveat is stated ===
  ok('settings offers a data export', (await dialog.getByRole('heading', { name: 'Your data' }).count()) > 0)
  ok('export warns that photo originals carry location data',
    await dialog.getByText('where the photo was taken', { exact: false }).isVisible())
  // om-6hlp N4: the EXIF strip toggle is present (default off/KEEP).
  ok('settings offers the EXIF strip toggle',
    await dialog.getByText('Strip camera metadata', { exact: false }).isVisible())
  const [download] = await Promise.all([
    page.waitForEvent('download', { timeout: 15000 }),
    dialog.getByRole('link', { name: 'Export my data' }).click(),
  ])
  const bundle = await download.path()
  const bytes = bundle ? statSync(bundle).size : 0
  ok('export downloads a non-empty zip bundle',
    bytes > 0 && /^coinrollhunter-export-\d{4}-\d{2}-\d{2}\.zip$/.test(download.suggestedFilename()),
    `${download.suggestedFilename()} — ${bytes} bytes`)

  await dialog.locator('input[type=number]').first().fill('0.85') // 90% buyback factor
  await page.getByRole('button', { name: 'Save settings' }).click()
  await dialog.waitFor({ state: 'detached', timeout: 5000 })
  const cfg = await api('/settings')
  ok('settings modal persists buyback factor', cfg.silver_buyback_factor_90pct === 0.85,
    `90pct ${cfg.silver_buyback_factor_90pct}`)

  // === Roll-txns grid: source-type is inert on 'return' rows (om-kn0f) ===
  await page.getByRole('button', { name: 'Edit' }).click()
  await page.getByRole('button', { name: 'Roll txns', exact: true }).click()
  await page.locator('section table thead th').first().waitFor({ timeout: 5000 })
  await awaitRowCount((await api('/roll-txns')).length) // wait for the rows before reading them
  // cells: date(0) bank(1) action(2) denom(3) unit(4) source(5); the first select
  // in a row is the action select. The draft row defaults to 'buy' so it's excluded.
  const returnSourceCells = await page.$$eval('section table tbody tr', (rows) =>
    rows
      .filter((r) => r.querySelector('select')?.value === 'return')
      .map((r) => r.cells[5]?.textContent?.trim()),
  )
  ok('source-type inert on return rows (om-kn0f)',
    returnSourceCells.length >= 1 && returnSourceCells.every((t) => t === '—'),
    JSON.stringify(returnSourceCells))

  // A denomless (mixed) return must bind to the "Mixed" option (value '') and
  // render it — not an out-of-range blank select. denom is the 2nd select in a
  // row (action, denom, unit); source(5) is an inert span on return rows.
  const returnDenomSelects = await page.$$eval('section table tbody tr', (rows) =>
    rows
      .filter((r) => r.querySelector('select')?.value === 'return')
      .map((r) => {
        const sel = r.querySelectorAll('select')[1]
        return { value: sel?.value, label: sel?.selectedOptions[0]?.textContent?.trim() }
      }),
  )
  ok('mixed return denom binds to "Mixed" (value "") in Edit grid',
    returnDenomSelects.length >= 1 && returnDenomSelects.every((c) => c.value === '' && c.label === 'Mixed'),
    JSON.stringify(returnDenomSelects))

  // === Holdings grid: custody Location (om-yhbr) ===
  // "Where IS the 1943-S?" — lots.location has been in the schema since migration
  // 0001 and wired through model/store/API, but was never surfaced. It mirrors
  // Source: free text with a datalist over your own distinct values (ADR-006).
  const goHoldings = async () => {
    // exact: the Edit view also renders an "edits save instantly…" hint button.
    await page.getByRole('button', { name: 'Edit', exact: true }).click()
    await page.getByRole('button', { name: 'Holdings', exact: true }).click()
    await page.locator('section table thead th').first().waitFor({ timeout: 5000 })
    // The header is up but rows arrive from an async load(); wait for at least one real
    // row (which also means the suggestion caches behind the datalists have landed).
    await awaitRowCount(1)
  }
  // Existing rows only — the trailing new-row draft has no Delete button.
  const locCells = () =>
    page.locator('tbody tr:has(button[title="Delete row"]) input[list="dl-holdings-location"]')
  const commit = async (input, value) => {
    await input.fill(value)
    // blur fires onchange → saveRow → PUT /api/lots/{id}; wait for that write to land,
    // not a fixed guess, before the caller re-reads the row from the API.
    const saved = page
      .waitForResponse((r) => r.request().method() === 'PUT' && /\/api\/lots\//.test(r.url()), { timeout: 5000 })
      .catch(() => {})
    await input.blur()
    await saved
  }

  await goHoldings()
  ok('Holdings grid shows a Location column',
    (await page.locator('section table thead th', { hasText: 'Location' }).count()) === 1)

  // Empty stays empty: an unfiled lot renders blank — not "null", not a placeholder value.
  const emptyLocs = await locCells().evaluateAll((els) => els.map((e) => e.value))
  ok('a lot with no location renders blank (not "null"/"undefined")',
    emptyLocs.length >= 1 && emptyLocs.every((v) => v === ''), JSON.stringify(emptyLocs))

  await commit(locCells().first(), 'home safe')
  const filed = (await api('/lots')).filter((l) => l.location === 'home safe')
  ok('editing Location persists to lots.location', filed.length === 1, `${filed.length} lot(s) @ home safe`)

  // …and survives a reload (it is stored, not just held in the grid's memory).
  await page.reload({ waitUntil: 'networkidle' })
  await goHoldings()
  const reloaded = await locCells().first().inputValue()
  ok('Location survives a reload', reloaded === 'home safe', `input "${reloaded}"`)

  // Autocomplete: the Location cell is bound to the grid's shared suggestion
  // datalist, wired exactly like Source — a `suggestions` closure over your own
  // distinct values (ADR-006 open vocabulary). Its *contents* are asserted at the
  // end of this file, with every other autocomplete in the app (om-rubx).
  const wiredToDatalist =
    (await locCells().first().getAttribute('list')) === 'dl-holdings-location' &&
    (await page.locator('datalist#dl-holdings-location').count()) === 1
  ok('Location is wired to the shared suggestion datalist (exactly as Source is)', wiredToDatalist)

  // Free text still wins: a value that is not in the suggestion list saves fine.
  await commit(locCells().nth(2), 'safe deposit box 214')
  ok('a novel (unsuggested) Location saves as free text',
    (await api('/lots')).some((l) => l.location === 'safe deposit box 214'))

  const sumAfterLoc = await api('/summary')
  ok('editing Location left the recorded sale intact',
    (sumAfterLoc.realized || []).length === 1 && Math.abs(sumAfterLoc.realized_gain - 250) < 0.01,
    `realized ${sumAfterLoc.realized?.length}, gain ${sumAfterLoc.realized_gain}`)

  // === Holdings grid: an edit cannot destroy a column it cannot see (om-kyq7) ===
  // The shipped bug: toHolding() rebuilt the whole lot from the grid's flat view and
  // the PUT was a full replace, so editing ANY cell — a quantity, a location — blanked
  // every column the grid does not model. notes (which the spreadsheet import fills, so
  // this hit new users on their first correction), insured_value, attributes; and on a
  // sold lot it wiped the disposal, resurrecting a completed sale. Nothing on screen
  // showed the damage. PUT is a merge now: a field the client never names is a field it
  // cannot destroy.
  //
  // The gesture below is the innocent one, and the earlier checks in this file used to
  // have to route AROUND it: type where a coin lives, on a row that happens to be a
  // completed sale. Disposed lots are not visually distinct in this grid, so a user has
  // no cue at all (that UX question is its own bead).
  const sold = (await api('/lots')).find((l) => l.disposed)
  ok('fixture: the gold eagle sale is on the books', !!sold, sold ? `lot ${sold.id}` : 'none disposed')

  // Give it the invisible fields a real user would have — an import writes notes.
  const NOTES = "grandfather's, do not sell"
  const ATTRS = '{"grade":"MS65"}'
  await apiPut(`/lots/${sold.id}`, { notes: NOTES, insured_value: 4500, attributes: ATTRS })

  await page.reload({ waitUntil: 'networkidle' })
  await goHoldings()
  const soldRow = (await api('/lots')).findIndex((l) => l.id === sold.id)
  await commit(locCells().nth(soldRow), 'wall safe')

  const edited = (await api('/lots')).find((l) => l.id === sold.id)
  ok('the Location edit itself lands', edited?.location === 'wall safe', `location "${edited?.location}"`)
  ok('a grid edit does not wipe notes', edited?.notes === NOTES, JSON.stringify(edited?.notes))
  ok('a grid edit does not wipe insured_value', edited?.insured_value === 4500, String(edited?.insured_value))
  ok('a grid edit does not wipe attributes', edited?.attributes === ATTRS, String(edited?.attributes))
  ok('a grid edit does not resurrect a sold lot',
    edited?.disposed === sold.disposed && edited?.disposed_usd === sold.disposed_usd,
    `disposed "${edited?.disposed}" @ ${edited?.disposed_usd}`)
  const sumAfterEdit = await api('/summary')
  ok('realized P&L survives an edit to the sold lot',
    (sumAfterEdit.realized || []).length === 1 && Math.abs(sumAfterEdit.realized_gain - 250) < 0.01,
    `realized ${sumAfterEdit.realized?.length}, gain ${sumAfterEdit.realized_gain}`)

  // === Autocomplete offers YOUR OWN values, not just the presets (om-rubx) ===
  // Every suggestion cache is filled by load() — i.e. after mount, once the network
  // answers. They were plain module-level `let`s, invisible to the renderer, and the
  // shared <datalist> lives under {#each columns}, a list that never changes: so the
  // block was evaluated exactly once, BEFORE load() resolved, and never again. Whatever
  // was already non-empty at module scope (the static presets) showed up; everything the
  // user had actually typed never did — in every grid at once. A probe of the old build
  // read: source [], location [], product = the 7 SILVER_PRESETS and nothing else. The
  // Bank field's entire purpose (nudge reuse of a branch you already have, instead of
  // forking a new one on a typo) was silently not happening.
  //
  // The caches are $state now, so the datalist re-renders when the values behind it
  // land. Assert that, against values this very suite typed in.
  await page.reload({ waitUntil: 'networkidle' })
  await goHoldings()

  const dlOptions = (id) =>
    page.locator(`datalist#${id} option`).evaluateAll((els) => els.map((e) => e.value))
  const lots = await api('/lots')
  const own = (field) => [...new Set(lots.map((l) => l[field]).filter(Boolean))]

  const locOpts = await dlOptions('dl-holdings-location')
  ok('Location autocomplete offers the locations you entered',
    own('location').length > 0 && own('location').every((v) => locOpts.includes(v)),
    `datalist ${JSON.stringify(locOpts)}`)

  const srcOpts = await dlOptions('dl-holdings-source')
  ok('Source autocomplete offers the sources you entered',
    own('source').length > 0 && own('source').every((v) => srcOpts.includes(v)),
    `datalist ${JSON.stringify(srcOpts)} vs lots ${JSON.stringify(own('source'))}`)

  // Product is the sharp one: on the old build this held the 7 presets and nothing
  // else, so a catalog entry you had just created did not suggest. Both must be there.
  const prodOpts = await dlOptions('dl-holdings-product')
  const catalogNames = (await api('/item-types')).map((t) => t.name)
  ok('Product autocomplete offers your catalog AND the presets',
    catalogNames.length > 0 &&
      catalogNames.every((n) => prodOpts.includes(n)) &&
      prodOpts.includes('90% dime (pre-1965)'),
    `${prodOpts.length} options; catalog ${JSON.stringify(catalogNames)}`)

  // Category must be probed with a category that is NOT one of the ten ADR-006 presets.
  // Every category this suite happens to enter ("Silver") is also a preset, so it shows
  // up in the datalist even on the broken build and proves nothing. Open vocabulary means
  // a word we never shipped comes back — so use one.
  const NOVEL_CAT = 'Civil War token'
  await apiPut(`/lots/${lots[0].id}`, { category: NOVEL_CAT })
  await page.reload({ waitUntil: 'networkidle' })
  await goHoldings()
  const catOpts = await dlOptions('dl-holdings-category')
  ok('Category autocomplete offers a category of your own, not just the ADR-006 vocab',
    catOpts.includes(NOVEL_CAT) && catOpts.length >= 11,
    `datalist ${JSON.stringify(catOpts)}`)

  // Bank, on a different grid: the branch you bought from must suggest itself back.
  await page.getByRole('button', { name: 'Roll txns', exact: true }).click()
  await page.locator('section table thead th').first().waitFor({ timeout: 5000 })
  await awaitRowCount((await api('/roll-txns')).length) // wait for load() to fill the bank suggestions
  // id is derived from the grid TITLE ('Roll transactions'), not the tab label.
  const bankOpts = await dlOptions('dl-roll-transactions-bank')
  const usedBanks = [...new Set((await api('/roll-txns')).map((r) => r.bank).filter(Boolean))]
  ok('Bank autocomplete offers the branches you have actually used',
    usedBanks.length > 0 && usedBanks.every((b) => bankOpts.includes(b)),
    `datalist ${JSON.stringify(bankOpts)} vs used ${JSON.stringify(usedBanks)}`)

  // === Holdings grid: a sold lot must not look like one you still own (om-5k35) ===
  // Not a data bug any more (the PUT merge preserves the disposal), an HONESTY one:
  // you could edit a completed sale without knowing, and changing its qty/basis moves
  // the cost basis under a realized gain you already booked. Marked, not locked — the
  // app cannot un-sell, so a mistyped sale still has to be fixable.
  await goHoldings()
  const soldLot = (await api('/lots')).find((l) => l.disposed)
  const liveLot = (await api('/lots')).find((l) => !l.disposed)
  const rowFor = (id) => page.locator(`tbody tr[data-row-index]`).filter({
    has: page.locator(`input[value="${id}"]`),
  })
  ok('fixture: there is a sold lot and a live one to compare', !!soldLot && !!liveLot,
    `sold ${soldLot?.id} / live ${liveLot?.id}`)

  // The cue rides on the <tr>, so it reaches the frozen columns too — which is the
  // only thing on screen once you scroll right to the cells you are about to edit.
  const classOfRowWithProduct = async (n) =>
    page.$$eval('tbody tr[data-row-index]', (trs) =>
      trs.map((t) => ({
        cls: t.className,
        struck: getComputedStyle(t.querySelector('input')).textDecorationLine,
      })))
  const rows = await classOfRowWithProduct()
  const anyStruck = rows.filter((r) => r.struck.includes('line-through'))
  const anyPlain = rows.filter((r) => !r.struck.includes('line-through'))
  ok('a sold lot is visibly marked in the Holdings grid (struck through + dimmed)',
    anyStruck.length > 0, `${anyStruck.length} marked, ${anyPlain.length} plain`)
  ok('a lot you still own is NOT marked', anyPlain.length > 0)

  const headers = await page.$$eval('section table thead th', (th) => th.map((h) => h.innerText.trim()))
  ok('the sale is surfaced: Holdings has Sold + Proceeds columns',
    headers.some((h) => h.startsWith('Sold')) && headers.some((h) => h.startsWith('Proceeds')),
    JSON.stringify(headers.filter((h) => h.startsWith('Sold') || h.startsWith('Proceeds'))))

  // Read-only: the disposal is shown, never typed. Selling stays the Sell action's job,
  // and naming disposed/disposed_usd in a PUT is what used to un-sell a lot.
  const soldColIdx = headers.findIndex((h) => h.startsWith('Sold'))
  const soldCells = await page.$$eval(
    `tbody tr[data-row-index] td:nth-child(${soldColIdx + 1})`,
    (tds) => tds.map((t) => t.querySelectorAll('input,select').length))
  ok('the Sold column is read-only (no editor in any cell)',
    soldCells.every((n) => n === 0), `${soldCells.filter((n) => n > 0).length} editable cells found`)

  // === Holdings grid: virtualization must not eat an in-flight edit (om-35ul) ===
  // The grid renders an editor per cell, so past ~60 lots it only mounts the rows
  // in view. A cell commits on `change`, which fires on BLUR — so a row that
  // unmounts while you are still typing in it would take the edit with it,
  // silently. That is the same class of grid data loss v0.3.0 shipped to fix, so
  // it is pinned here: type, scroll the row out of the window, and read the DB.
  const seed = (await api('/lots')).find((l) => !l.disposed)
  // Drop the identity AND the disposal — cloning a sold lot would seed 70 sold ones
  // and quietly change what the checks below are looking at.
  const { id: _drop, uid: _uid, disposed: _d, disposed_usd: _du, ...tmpl } = seed
  for (let i = 0; i < 70; i++) {
    const r = await fetch(BASE + '/api/lots', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ ...tmpl, qty: 1 }),
    })
    if (!r.ok) throw new Error(`seed lot ${i} → ${r.status}`)
  }
  const total = (await api('/lots')).length
  // The grid is already mounted from the checks above and load() does not re-run on a
  // tab click, so without this it would still be showing the pre-seed rows — and the
  // "renders a window" check would pass vacuously on stale data.
  await page.reload({ waitUntil: 'networkidle' })
  await goHoldings()
  await page.locator('tbody tr[data-row-index]').first().waitFor({ timeout: 5000 })

  const mounted = await page.locator('tbody tr[data-row-index]').count()
  // Bounded on BOTH sides: fewer than every row (it windows) but more than a handful
  // (it is really showing the seeded collection, not a stale pre-seed render).
  ok('Holdings grid virtualizes a large collection (renders a window, not every row)',
    mounted < total && mounted > 10, `${mounted} rows in the DOM of ${total} lots`)

  const qtyCol = await page.$$eval('section table thead th', (th) =>
    th.findIndex((h) => h.innerText.trim().startsWith('Qty')))
  const editRow = page.locator('tbody tr[data-row-index="0"]')
  const editId = await api('/lots').then((l) => l[0].id)
  const qtyInput = editRow.locator('td').nth(qtyCol).locator('input')
  await qtyInput.click()
  await qtyInput.fill('4242') // typed, NOT blurred — no Enter, no click away
  // Scroll far enough that row 0 falls outside the rendered window and unmounts.
  await page.evaluate(() => {
    document.querySelector('section table').parentElement.scrollTop = 15000
  })
  // Scrolling row 0 out of the window blurs its editor (commitIfLeaving) and unmounts it;
  // wait for the unmount, then for the blur-triggered PUT to land — not a fixed guess.
  await page.locator('tbody tr[data-row-index="0"]').waitFor({ state: 'detached', timeout: 5000 }).catch(() => {})
  ok('the edited row really did unmount (otherwise the guard is untested)',
    (await page.locator('tbody tr[data-row-index="0"]').count()) === 0)
  await awaitApi('/lots', (ls) => ls.find((l) => l.id === editId)?.qty === 4242)
  const afterScroll = (await api('/lots')).find((l) => l.id === editId)
  ok('an in-flight grid edit survives its row being virtualized away',
    afterScroll?.qty === 4242, `db qty ${afterScroll?.qty} (expected 4242)`)

  // === Deleting a row is gated by a confirm that NAMES it (om-lv4q) ===
  // The trash can was a one-click hard DELETE: no confirm, no undo, no soft-delete,
  // no server-side trash — the only way back from a misclick was restoring last
  // night's backup and losing everything since. Every editable grid funnels its
  // delete through EditableGrid, and `remove` is a required prop, so ONE guard covers
  // all seven; it is asserted once, here, on Supplies — a real cost row, and the only
  // grid this suite does not otherwise touch, so the check cannot disturb anything.
  const SUPPLY_GONE = 'QA tubes to delete'
  const SUPPLY_KEPT = 'QA flips to keep'
  await apiPost('/supplies', { date: '2026-01-05', item: SUPPLY_KEPT, cost_usd: 4.5 })
  await apiPost('/supplies', { date: '2026-01-06', item: SUPPLY_GONE, cost_usd: 13.37 })

  await page.reload({ waitUntil: 'networkidle' })
  await page.getByRole('button', { name: 'Edit', exact: true }).click()
  await page.getByRole('button', { name: 'Supplies', exact: true }).click()
  await page.locator('section table thead th').first().waitFor({ timeout: 5000 })

  // Existing rows only — the sticky draft row has no Delete button.
  const supplyRowSel = 'section table tbody tr:has(button[title="Delete row"])'
  const supplyRows = () => page.locator(supplyRowSel)
  const supplyItems = async () => (await api('/supplies')).map((s) => s.item)
  // The grid renders values into <input>s, so a row is found by input VALUE, not text.
  const trashFor = async (item) => {
    const idx = await supplyRows().evaluateAll(
      (rows, it) => rows.findIndex((r) => [...r.querySelectorAll('input')].some((i) => i.value === it)),
      item,
    )
    // findIndex → -1 on a miss, and Playwright's .nth(-1) selects the LAST row instead
    // of erroring — so a lookup miss would silently click the wrong row's trash. Fail loud.
    if (idx < 0) throw new Error(`trashFor: no supply row with an input value === ${JSON.stringify(item)}`)
    return supplyRows().nth(idx).locator('button[title="Delete row"]')
  }
  const confirmDlg = page.getByRole('dialog')
  const cancelBtn = () => confirmDlg.getByRole('button', { name: 'Cancel', exact: true })

  // Wait for the async GET /api/supplies to populate the grid BEFORE counting — the
  // `thead th` we waited on renders before load() resolves, so a bare count here would
  // race the fetch and could read 0 (the flake class this bead, om-yd1h, fixes). We seeded
  // exactly two rows into this otherwise-untouched grid, so 2 is the settled count. (Uses
  // the shared awaitRowCount, scoped to the Supplies delete-button rows.)
  await awaitRowCount(2, { sel: supplyRowSel })
  const rowsBefore = await supplyRows().count()
  ok('fixture: both QA supply rows are in the grid',
    rowsBefore === 2 && (await supplyItems()).includes(SUPPLY_GONE), `${rowsBefore} row(s)`)

  // --- The trash can opens a confirmation, and it says WHICH row ---
  await (await trashFor(SUPPLY_GONE)).click()
  await confirmDlg.waitFor({ timeout: 5000 })
  const dialogText = (await confirmDlg.innerText()).replace(/\s+/g, ' ')
  ok('the trash can opens a confirmation instead of deleting', await confirmDlg.isVisible())
  ok('the confirmation NAMES the specific row it will delete',
    dialogText.includes(SUPPLY_GONE) && dialogText.includes('$13.37') && !dialogText.includes(SUPPLY_KEPT),
    JSON.stringify(dialogText))

  // Default focus is on Cancel — asserted BEFORE any Tab moves it (a stray Enter, the
  // key already under your finger in a grid you commit cells with, then cancels).
  ok('Cancel is the default-focused control (so a stray Enter cancels, not deletes)',
    await cancelBtn().evaluate((el) => el === document.activeElement))

  // --- Focus is trapped: aria-modal is a promise the keyboard must keep ---
  // Tab from Cancel (Cancel → Delete → wrap back to Cancel). Without the trap, the
  // second Tab would walk out into the live grid — onto another row's trash button or
  // a hidden input whose blur fires a write behind the "modal".
  const inDialog = () =>
    confirmDlg.evaluate((el) => el.contains(document.activeElement) && document.activeElement.tagName === 'BUTTON')
  await page.keyboard.press('Tab')
  ok('Tab keeps focus inside the dialog (1st)', await inDialog())
  await page.keyboard.press('Tab')
  ok('Tab wraps within the dialog rather than escaping to the grid (2nd)', await inDialog())
  await page.keyboard.press('Shift+Tab')
  ok('Shift+Tab also stays inside the dialog', await inDialog())

  // --- Cancel KEEPS the row ---
  await cancelBtn().click()
  await confirmDlg.waitFor({ state: 'detached', timeout: 5000 })
  ok('Cancel keeps the row (grid)', (await supplyRows().count()) === rowsBefore)
  ok('Cancel keeps the row (REST API)', (await supplyItems()).includes(SUPPLY_GONE))

  // --- Escape keeps it too ---
  await (await trashFor(SUPPLY_GONE)).click()
  await confirmDlg.waitFor({ timeout: 5000 })
  await page.keyboard.press('Escape')
  await confirmDlg.waitFor({ state: 'detached', timeout: 5000 })
  ok('Escape keeps the row',
    (await supplyRows().count()) === rowsBefore && (await supplyItems()).includes(SUPPLY_GONE))
  // Closing returns focus to the trash button that opened it — not <body> — so the
  // keyboard user resumes where they were.
  ok('closing restores focus to the trash button that opened the dialog',
    await page.evaluate(() => document.activeElement?.getAttribute('title') === 'Delete row'))

  // --- Confirm REMOVES it — from the grid AND from the database ---
  await (await trashFor(SUPPLY_GONE)).click()
  await confirmDlg.waitFor({ timeout: 5000 })
  await confirmDlg.getByRole('button', { name: 'Delete', exact: true }).click()
  await confirmDlg.waitFor({ state: 'detached', timeout: 5000 })
  await awaitRowCount(rowsBefore - 1, { sel: supplyRowSel, exact: true }) // count DROPS — must be exact
  const rowsAfter = await supplyRows().count()
  const itemsAfter = await supplyItems()
  ok('Confirm removes the row (grid)', rowsAfter === rowsBefore - 1,
    `${rowsAfter} row(s), expected ${rowsBefore - 1}`)
  ok('Confirm removes the row (REST API)', !itemsAfter.includes(SUPPLY_GONE), JSON.stringify(itemsAfter))
  ok('…and only that row — the neighbouring row is untouched', itemsAfter.includes(SUPPLY_KEPT))
} catch (e) {
  ok('UNCAUGHT', false, e.message)
  await page.screenshot({ path: `${SHOT}/do-error.png`, fullPage: true }).catch(() => {})
} finally {
  ok('no console errors', consoleErrors.length === 0, consoleErrors.slice(0, 5).join(' | '))
  ok('no page errors', pageErrors.length === 0, pageErrors.slice(0, 5).join(' | '))
  const passed = results.filter((r) => r.pass).length
  console.log(`\n${passed}/${results.length} checks passed`)
  await browser.close()
  process.exit(passed === results.length ? 0 : 1)
}
