// End-to-end regression guard for the workflow-first "Do" tab (bead om-tuu9).
// Drives every tile in a real headless browser against a running `serve`
// instance, asserting both the UI flow and the recorded rows (via the REST API),
// and fails on ANY console/page error. Assumes a freshly-migrated DB with a spot
// price seeded — see run.sh, which sets that up and tears it down.
//
// Run standalone:  BASE_URL=http://127.0.0.1:8799 node do-tab.e2e.mjs
import { chromium } from 'playwright'

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
  await page.waitForTimeout(400)
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
  await page.getByRole('button', { name: 'Done' }).click()

  // === Edit layer: Losses grid round-trips ===
  await page.getByRole('button', { name: 'Edit' }).click()
  await page.getByRole('button', { name: 'Losses', exact: true }).click()
  await page.locator('section table thead th').first().waitFor({ timeout: 5000 })
  const lossRows = await page.locator('section table tbody tr:has(button[title="Delete row"])').count()
  const apiLosses = (await api('/losses')).length
  ok('Losses grid round-trips', lossRows === apiLosses && apiLosses >= 1, `dom ${lossRows} vs api ${apiLosses}`)

  // === Overview reflects shrinkage ===
  await page.getByRole('button', { name: 'Overview', exact: true }).click()
  await page.getByText('All cashed in', { exact: false }).first().waitFor({ timeout: 5000 })
  ok('overview reconciliation banner shows lost', (await page.getByText('lost', { exact: false }).count()) > 0)

  // === ADR-006/007 surfacing (om-5tl2) ===
  // KPI cards from /api/summary
  const sumKpi = await api('/summary')
  ok('KPI: buy_count present', sumKpi.buy_count >= 1, `buy_count ${sumKpi.buy_count}`)
  ok('KPI cards render (Buys/Branches/Avg buy)',
    (await page.getByText('Buys', { exact: true }).count()) > 0 &&
    (await page.getByText('Branches', { exact: true }).count()) > 0 &&
    (await page.getByText('Avg buy', { exact: true }).count()) > 0)
  // spot freshness chip — source label varies (manual seed vs. the ADR-007 poller),
  // so match the chip by its unique title, not the source string.
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
  await newRow.locator('input[type=checkbox]').check()
  await newRow.locator('button[title="Add row"]').click()
  await page.waitForTimeout(500)
  const taxLot = (await api('/lots')).find((l) => l.category === 'Silver' && l.subcategory === 'Mercury' && l.trophy === true)
  ok('Holdings taxonomy persists (category/subcategory/trophy)', !!taxLot, taxLot ? `lot ${taxLot.id}` : 'not found')
  // The new find has basis 0 (grid default) — summary must still serialize (no +Inf in unreal_pct).
  const sumZeroBasis = await api('/summary')
  ok('summary survives a zero-basis find (unreal_pct null, not +Inf)',
    Array.isArray(sumZeroBasis.lots) && sumZeroBasis.lots.length >= 1,
    `lots ${sumZeroBasis.lots?.length}`)

  // trophy feed surfaces it back on the Insights tab (analysis lives in Insights
  // since the ADR-012 IA refactor — not the read-only Overview).
  await page.getByRole('button', { name: 'Insights', exact: true }).click()
  await page.getByRole('heading', { name: 'Greatest hits' }).waitFor({ timeout: 5000 })
  ok('trophy feed shows the trophy', (await page.getByText('Mercury dime (trophy)', { exact: false }).count()) > 0)

  // === Settings editor (audit gap #8): edits persist via PUT /api/settings ===
  await page.locator('button[title="Settings"]').click()
  const dialog = page.locator('[role="dialog"]')
  await dialog.getByRole('heading', { name: 'Settings' }).waitFor({ timeout: 5000 })
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
  }
  // Existing rows only — the trailing new-row draft has no Delete button.
  const locCells = () =>
    page.locator('tbody tr:has(button[title="Delete row"]) input[list="dl-holdings-location"]')
  const commit = async (input, value) => {
    await input.fill(value)
    await input.blur() // onchange → saveRow
    await page.waitForTimeout(500)
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
  // distinct values (ADR-006 open vocabulary).
  //
  // The datalist's *contents* are deliberately NOT asserted here. EditableGrid
  // renders the datalist once at mount, before load() has filled the suggestion
  // caches, so NO autocomplete in the app (Source, Product, Category, Bank, and
  // therefore Location) currently offers your own entries — only the static
  // presets. That is a shared-renderer bug that predates this column and spans six
  // grids; it is tracked as om-rubx, and Location starts suggesting the moment it
  // lands, with no change here.
  const wiredToDatalist =
    (await locCells().first().getAttribute('list')) === 'dl-holdings-location' &&
    (await page.locator('datalist#dl-holdings-location').count()) === 1
  ok('Location is wired to the shared suggestion datalist (exactly as Source is)', wiredToDatalist)

  // Free text still wins: a value that is not in the suggestion list saves fine.
  //
  // Target a LIVE lot. Row 1 is the gold eagle that step 6 sold in full, and editing
  // a disposed lot through the grid RESURRECTS it — toHolding() omits disposed/
  // disposed_usd and the PUT is a full replace, so the sale record is destroyed
  // (om-kyq7 owns that bug and its regression test). Row 2 is the Mercury dime,
  // which is still held.
  await commit(locCells().nth(2), 'safe deposit box 214')
  ok('a novel (unsuggested) Location saves as free text',
    (await api('/lots')).some((l) => l.location === 'safe deposit box 214'))

  // Fixture integrity: none of the above may have touched a sold lot. If a future
  // edit here retargets a disposed row, om-kyq7 silently eats the sale and every
  // check appended after this point inherits a corrupted fixture — so pin it.
  const sumAfterLoc = await api('/summary')
  ok('editing Location left the recorded sale intact',
    (sumAfterLoc.realized || []).length === 1 && Math.abs(sumAfterLoc.realized_gain - 250) < 0.01,
    `realized ${sumAfterLoc.realized?.length}, gain ${sumAfterLoc.realized_gain}`)
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
