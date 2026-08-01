export async function runWorkflows(h) {
  const { page, ok, api, apiDelete, goDo, tile, awaitApi } = h

  await page.goto(h.base, { waitUntil: 'networkidle' })
  await page.getByRole('heading', { name: 'CoinRollHunter' }).waitFor({ timeout: 8000 })
  ok('app shell renders', await page.getByText('Local-first coins').isVisible())

  await goDo()
  for (const title of [
    'Bought a box',
    'Logged finds',
    'Returned to bank',
    'Reconcile',
    'New coin',
    'Sold something',
  ]) {
    ok(`tile present: ${title}`, (await tile(title).count()) > 0)
  }

  await recordBox()
  await recordFinds()
  await verifyKeptFind()
  await recordBullion()
  await recordReturn()
  await reconcile()
  await recordSale()

  async function recordBox() {
    await tile('Bought a box').click()
    await page.getByRole('heading', { name: 'Bought a box / rolls' }).waitFor()
    await page.getByPlaceholder('Stock Yards').fill('Stock Yards')
    await page
      .locator('select')
      .filter({ has: page.locator('option[value="customer_roll"]') })
      .selectOption('customer_roll')
    await page.getByLabel('Also log the bank trip (gas + time)').check()
    await page.locator('input[type=number]').last().fill('0.5')
    await page.getByRole('button', { name: /Log .* bought/ }).click()
    await page.getByText('Logged', { exact: false }).first().waitFor({ timeout: 5000 })

    const buys = (await api('/roll-txns')).filter((row) => row.action === 'buy')
    ok('buy recorded', buys.length === 1, `${buys.length} buys`)
    ok('buy face = $500 (auto)', buys[0]?.face_usd === 500, `face ${buys[0]?.face_usd}`)
    ok('buy source type persisted', buys[0]?.source_type === 'customer_roll', `src ${buys[0]?.source_type}`)
    ok('optional trip recorded', (await api('/trips')).length === 1)
    await page.getByRole('button', { name: 'Done' }).click()
  }

  async function recordFinds() {
    await goDo()
    await tile('Logged finds').click()
    await page.getByRole('heading', { name: 'Logged finds' }).waitFor()
    const product = page.getByPlaceholder('90% half (1964 & earlier)')
    await product.fill('90% half (1964 & earlier)')
    await product.dispatchEvent('input')
    const spinbuttons = () => page.getByRole('spinbutton')
    await spinbuttons().nth(0).fill('3')
    await spinbuttons().nth(0).dispatchEvent('input')
    await page.getByRole('button', { name: 'Add a keeper' }).click()
    await spinbuttons().nth(2).fill('10')
    await spinbuttons().nth(2).dispatchEvent('input')
    await page.getByRole('button', { name: 'Save finds' }).click()
    await page.getByText('in find face', { exact: false }).waitFor({ timeout: 5000 })

    const buys = (await api('/roll-txns')).filter((row) => row.action === 'buy')
    const finds = (await api('/lots')).filter((row) => row.activity === 'crh')
    const keepers = await api('/keepers')
    ok('find recorded (crh holding)', finds.length === 1, `${finds.length} finds`)
    ok('find linked to box', finds[0]?.roll_txn_id === buys[0].id, `roll_txn_id ${finds[0]?.roll_txn_id}`)
    ok('keeper recorded', keepers.length === 1, `${keepers.length} keepers`)
    ok(
      'keeper face auto (10 halves=$5)',
      Math.abs((keepers[0]?.face_usd ?? 0) - 5) < 0.01,
      `face ${keepers[0]?.face_usd}`,
    )
    await page.getByRole('button', { name: 'Done' }).click()
    const summary = await api('/summary')
    ok('box yield computed', (summary.box_yields || []).some((row) => row.find_count > 0))
  }

  async function verifyKeptFind() {
    await goDo()
    await tile('Logged finds').click()
    await page.getByRole('heading', { name: 'Logged finds' }).waitFor()
    const keepersBefore = (await api('/keepers')).length
    const summaryBefore = await api('/summary')
    const product = page.getByPlaceholder('90% half (1964 & earlier)')
    await product.fill('1943-S Merc (kept)')
    await product.dispatchEvent('input')
    const spinbuttons = () => page.getByRole('spinbutton')
    await spinbuttons().nth(0).fill('1')
    await spinbuttons().nth(0).dispatchEvent('input')
    await spinbuttons().nth(1).fill('2')
    await spinbuttons().nth(1).dispatchEvent('input')
    ok('Keep defaults on for a logged find', await page.getByRole('checkbox').first().isChecked())
    await page.getByRole('button', { name: 'Save finds' }).click()
    await page.getByText('in find face', { exact: false }).waitFor({ timeout: 5000 })

    const keepersAfter = (await api('/keepers')).length
    const keptFind = (await api('/lots')).find(
      (row) => Math.abs((row.face_value_usd ?? 0) - 2) < 0.01 && row.activity === 'crh',
    )
    const summaryAfter = await api('/summary')
    ok('a kept find created zero keeper rows', keepersAfter === keepersBefore, `${keepersBefore} -> ${keepersAfter}`)
    ok('the find stores the kept flag', keptFind?.kept === true, `kept ${JSON.stringify(keptFind?.kept)}`)
    ok(
      'kept face rose by exactly the find face ($2)',
      Math.abs(summaryAfter.kept_face - summaryBefore.kept_face - 2) < 0.01,
      `delta ${(summaryAfter.kept_face - summaryBefore.kept_face).toFixed(2)}`,
    )
    ok(
      'clad face did not move',
      Math.abs(summaryAfter.clad_face - summaryBefore.clad_face) < 0.01,
      `delta ${(summaryAfter.clad_face - summaryBefore.clad_face).toFixed(2)}`,
    )
    if (keptFind?.id) await apiDelete(`/lots/${keptFind.id}`)
    await page.getByRole('button', { name: 'Done' }).click()
  }

  async function recordBullion() {
    await goDo()
    await tile('New coin').click()
    await page.getByRole('heading', { name: 'New coin / bullion' }).waitFor()
    await page.getByPlaceholder('1 oz American Gold Eagle').fill('1 oz Gold Eagle')
    await page.getByLabel('Fine oz / unit').fill('1')
    await page.getByLabel('Quantity').fill('1')
    const acquiredInput = page.getByLabel('Acquired')
    const basisInput = page.getByLabel('Total paid (basis) $')
    const premiumInput = page.getByLabel('Premium paid $')

    await acquiredInput.fill('2025-12-30')
    await basisInput.fill('3800')
    const noHistorySettled = await page
      .getByText('No stored spot record exists', { exact: false })
      .waitFor({ timeout: 5000 })
      .then(() => true)
      .catch(() => false)
    ok(
      'an acquisition before all spot history has no suggestion',
      noHistorySettled && Number(await premiumInput.inputValue()) === 0,
      `premium ${await premiumInput.inputValue()}`,
    )

    await acquiredInput.fill('2026-01-01')
    const belowMeltSettled = await page
      .getByText('below melt', { exact: false })
      .waitFor({ timeout: 5000 })
      .then(() => true)
      .catch(() => false)
    ok(
      'a below-melt purchase is described instead of called zero premium',
      belowMeltSettled && Number(await premiumInput.inputValue()) === 0,
      `premium ${await premiumInput.inputValue()}`,
    )

    await basisInput.fill('4150')
    const suggestionSettled = await page
      .getByText('premium suggested', { exact: false })
      .waitFor({ timeout: 5000 })
      .then(() => true)
      .catch(() => false)
    ok(
      'premium uses the acquisition-date manual correction, not latest spot',
      suggestionSettled && Number(await premiumInput.inputValue()) === 200,
      `premium ${await premiumInput.inputValue()}`,
    )
    await premiumInput.fill('125')
    await page.getByRole('button', { name: 'Add to stack' }).click()
    await page.getByText('Added', { exact: false }).first().waitFor({ timeout: 5000 })

    const bullion = (await api('/lots')).filter((row) => row.activity === 'bullion')
    ok('bullion holding added', bullion.length === 1)
    ok(
      'premium override is stored separately from basis',
      bullion[0]?.basis_usd === 4150 && bullion[0]?.premium_usd === 125,
      `basis ${bullion[0]?.basis_usd}, premium ${bullion[0]?.premium_usd}`,
    )
    await page.getByRole('button', { name: 'Done' }).click()
  }

  async function recordReturn() {
    await goDo()
    await tile('Returned to bank').click()
    await page.getByRole('heading', { name: 'Return culls to the bank' }).waitFor()
    await page.getByRole('spinbutton').first().fill('400')
    await page.getByRole('button', { name: /Return .* to bank/ }).click()
    await page.getByText('Recorded', { exact: false }).first().waitFor({ timeout: 5000 })
    const returns = (await api('/roll-txns')).filter((row) => row.action === 'return')
    ok('return recorded', returns.length === 1 && returns[0].face_usd === 400, `@ ${returns[0]?.face_usd}`)
    ok('return is denomless (mixed pile)', returns[0]?.denom === '', `denom ${JSON.stringify(returns[0]?.denom)}`)
    await page.getByRole('button', { name: 'Done' }).click()
  }

  async function reconcile() {
    const before = await api('/summary')
    ok('float still open before reconcile', before.to_redeposit > 0.01, `to_redeposit ${before.to_redeposit}`)
    await goDo()
    await tile('Reconcile').click()
    await page.getByRole('heading', { name: 'Reconcile / close the books' }).waitFor()
    await page.getByRole('spinbutton').first().fill('5')
    await page.getByRole('button', { name: 'Add', exact: true }).first().click()
    await awaitApi('/keepers', (rows) => rows.length === 2)
    ok('reconcile recorded forgotten keeper', (await api('/keepers')).length === 2)
    const middle = await api('/summary')
    ok(
      'keeper reduced float (not a loss)',
      (middle.losses ?? 0) === 0 && middle.to_redeposit < before.to_redeposit,
      `losses ${middle.losses}, float ${middle.to_redeposit}`,
    )
    await page.getByRole('button', { name: /Book .* loss/ }).click()
    await page.getByText('books closed', { exact: false }).waitFor({ timeout: 5000 })
    const after = await api('/summary')
    ok('loss booked', (after.losses ?? 0) > 0, `losses ${after.losses}`)
    ok('float reconciled to ~$0', Math.abs(after.to_redeposit) < 0.01, `to_redeposit ${after.to_redeposit}`)
    ok('reconciled flag true', after.reconciled === true)
    ok(
      'CRH net dropped by the loss',
      Math.abs(middle.crh_net_real - after.losses - after.crh_net_real) < 0.01,
      `mid ${middle.crh_net_real} loss ${after.losses} after ${after.crh_net_real}`,
    )
    await page.getByRole('button', { name: 'Done' }).click()
  }

  async function recordSale() {
    await goDo()
    await tile('Sold something').click()
    await page.getByRole('heading', { name: 'Sold something' }).waitFor()
    await page.locator('select').first().selectOption({ index: 1 })
    await page.getByRole('spinbutton').nth(1).fill('4400')
    await page.getByRole('button', { name: 'Record sale' }).click()
    await page.getByText('Realized', { exact: false }).first().waitFor({ timeout: 5000 })
    const summary = await api('/summary')
    ok('sale recorded (realized)', (summary.realized || []).length === 1)
    ok('realized gain ~ +$250', Math.abs(summary.realized_gain - 250) < 0.01, `gain ${summary.realized_gain}`)
    ok(
      'bullion sale does not move CRH lifetime',
      Math.abs(summary.crh_net_lifetime - summary.crh_net_real) < 0.01,
      `lifetime ${summary.crh_net_lifetime} vs live ${summary.crh_net_real}`,
    )
    ok(
      'realized split adds up',
      Math.abs(summary.realized_gain - (summary.realized_gain_crh + summary.realized_gain_bullion)) < 0.01,
      `${summary.realized_gain} vs ${summary.realized_gain_crh}+${summary.realized_gain_bullion}`,
    )
    await page.getByRole('button', { name: 'Done' }).click()
  }
}
