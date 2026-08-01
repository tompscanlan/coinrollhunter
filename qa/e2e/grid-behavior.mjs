export async function runGridBehavior(h) {
  const { page, base, ok, api, apiPost, goHoldings, awaitRowCount, awaitApi } = h

  await verifySoldRows()
  await verifyVirtualizedEdit()
  await verifyDeleteConfirmation()

  async function verifySoldRows() {
    await goHoldings()
    const soldLot = (await api('/lots')).find((lot) => lot.disposed)
    const liveLot = (await api('/lots')).find((lot) => !lot.disposed)
    ok(
      'fixture: there is a sold lot and a live one',
      Boolean(soldLot && liveLot),
      `sold ${soldLot?.id} / live ${liveLot?.id}`,
    )

    const rowStyles = await page.$$eval('tbody tr[data-row-index]', (rows) =>
      rows.map((row) => ({
        struck: getComputedStyle(row.querySelector('input')).textDecorationLine,
      })),
    )
    const marked = rowStyles.filter((row) => row.struck.includes('line-through'))
    const plain = rowStyles.filter((row) => !row.struck.includes('line-through'))
    ok('a sold lot is visibly marked in Holdings', marked.length > 0, `${marked.length} marked, ${plain.length} plain`)
    ok('a lot still owned is not marked', plain.length > 0)

    const headers = await page.$$eval('section table thead th', (elements) =>
      elements.map((element) => element.innerText.trim()),
    )
    ok(
      'Holdings surfaces Sold and Proceeds columns',
      headers.some((header) => header.startsWith('Sold')) && headers.some((header) => header.startsWith('Proceeds')),
      JSON.stringify(headers.filter((header) => header.startsWith('Sold') || header.startsWith('Proceeds'))),
    )
    const soldColumn = headers.findIndex((header) => header.startsWith('Sold'))
    const editorCounts = await page.$$eval(
      `tbody tr[data-row-index] td:nth-child(${soldColumn + 1})`,
      (cells) => cells.map((cell) => cell.querySelectorAll('input,select').length),
    )
    ok(
      'the Sold column is read-only',
      editorCounts.every((count) => count === 0),
      `${editorCounts.filter((count) => count > 0).length} editable cells found`,
    )
  }

  async function verifyVirtualizedEdit() {
    const seed = (await api('/lots')).find((lot) => !lot.disposed)
    const { id: _id, uid: _uid, disposed: _disposed, disposed_usd: _proceeds, ...template } = seed
    for (let index = 0; index < 70; index++) {
      const response = await fetch(base + '/api/lots', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ ...template, qty: 1 }),
      })
      if (!response.ok) throw new Error(`seed lot ${index} → ${response.status}`)
    }

    const total = (await api('/lots')).length
    await page.reload({ waitUntil: 'networkidle' })
    await goHoldings()
    await page.locator('tbody tr[data-row-index]').first().waitFor({ timeout: 5000 })
    const mounted = await page.locator('tbody tr[data-row-index]').count()
    ok(
      'Holdings virtualizes a large collection',
      mounted < total && mounted > 10,
      `${mounted} rows in the DOM of ${total} lots`,
    )

    const quantityColumn = await page.$$eval('section table thead th', (headers) =>
      headers.findIndex((header) => header.innerText.trim().startsWith('Qty')),
    )
    const editRow = page.locator('tbody tr[data-row-index="0"]')
    const editId = await api('/lots').then((lots) => lots[0].id)
    const quantityInput = editRow.locator('td').nth(quantityColumn).locator('input')
    await quantityInput.click()
    await quantityInput.fill('4242')
    await page.evaluate(() => {
      document.querySelector('section table').parentElement.scrollTop = 15000
    })
    await page
      .locator('tbody tr[data-row-index="0"]')
      .waitFor({ state: 'detached', timeout: 5000 })
      .catch(() => {})
    ok(
      'the edited row really unmounted',
      (await page.locator('tbody tr[data-row-index="0"]').count()) === 0,
    )
    await awaitApi('/lots', (lots) => lots.find((lot) => lot.id === editId)?.qty === 4242)
    const edited = (await api('/lots')).find((lot) => lot.id === editId)
    ok('an in-flight edit survives virtualization', edited?.qty === 4242, `db qty ${edited?.qty}`)
  }

  async function verifyDeleteConfirmation() {
    const removable = 'QA tubes to delete'
    const neighbor = 'QA flips to keep'
    await apiPost('/supplies', { date: '2026-01-05', item: neighbor, cost_usd: 4.5 })
    await apiPost('/supplies', { date: '2026-01-06', item: removable, cost_usd: 13.37 })

    await page.reload({ waitUntil: 'networkidle' })
    await page.getByRole('button', { name: 'Edit', exact: true }).click()
    await page.getByRole('button', { name: 'Supplies', exact: true }).click()
    await page.locator('section table thead th').first().waitFor({ timeout: 5000 })

    const rowSelector = 'section table tbody tr:has(button[title="Delete row"])'
    const rows = () => page.locator(rowSelector)
    const items = async () => (await api('/supplies')).map((supply) => supply.item)
    const trashFor = async (item) => {
      const index = await rows().evaluateAll(
        (elements, expected) =>
          elements.findIndex((row) => [...row.querySelectorAll('input')].some((input) => input.value === expected)),
        item,
      )
      if (index < 0) throw new Error(`no supply row found for ${JSON.stringify(item)}`)
      return rows().nth(index).locator('button[title="Delete row"]')
    }
    const dialog = page.getByRole('dialog')
    const cancel = () => dialog.getByRole('button', { name: 'Cancel', exact: true })

    await awaitRowCount(2, { selector: rowSelector })
    const before = await rows().count()
    ok('fixture: both QA supply rows are loaded', before === 2 && (await items()).includes(removable), `${before} rows`)

    await (await trashFor(removable)).click()
    await dialog.waitFor({ timeout: 5000 })
    const text = (await dialog.innerText()).replace(/\s+/g, ' ')
    ok('the trash can opens a confirmation', await dialog.isVisible())
    ok(
      'the confirmation names the specific row',
      text.includes(removable) && text.includes('$13.37') && !text.includes(neighbor),
      JSON.stringify(text),
    )
    ok('Cancel receives default focus', await cancel().evaluate((element) => element === document.activeElement))

    const focusInside = () =>
      dialog.evaluate(
        (element) => element.contains(document.activeElement) && document.activeElement.tagName === 'BUTTON',
      )
    await page.keyboard.press('Tab')
    ok('Tab keeps focus inside the dialog', await focusInside())
    await page.keyboard.press('Tab')
    ok('Tab wraps inside the dialog', await focusInside())
    await page.keyboard.press('Shift+Tab')
    ok('Shift+Tab stays inside the dialog', await focusInside())

    await cancel().click()
    await dialog.waitFor({ state: 'detached', timeout: 5000 })
    ok('Cancel keeps the row in the grid', (await rows().count()) === before)
    ok('Cancel keeps the row in the database', (await items()).includes(removable))

    await (await trashFor(removable)).click()
    await dialog.waitFor({ timeout: 5000 })
    await page.keyboard.press('Escape')
    await dialog.waitFor({ state: 'detached', timeout: 5000 })
    ok('Escape keeps the row', (await rows().count()) === before && (await items()).includes(removable))
    ok(
      'closing restores focus to the opening trash button',
      await page.evaluate(() => document.activeElement?.getAttribute('title') === 'Delete row'),
    )

    await (await trashFor(removable)).click()
    await dialog.waitFor({ timeout: 5000 })
    await dialog.getByRole('button', { name: 'Delete', exact: true }).click()
    await dialog.waitFor({ state: 'detached', timeout: 5000 })
    await awaitRowCount(before - 1, { selector: rowSelector, exact: true })
    const after = await rows().count()
    const remaining = await items()
    ok('Confirm removes the row from the grid', after === before - 1, `${after} rows`)
    ok('Confirm removes the row from the database', !remaining.includes(removable), JSON.stringify(remaining))
    ok('the neighboring row is untouched', remaining.includes(neighbor))
  }
}
