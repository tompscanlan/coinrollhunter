export async function runGridPersistence(h) {
  const { page, ok, api, apiPut, goHoldings, awaitRowCount } = h

  await verifyReturnGrid()
  await verifyLocationsAndMergeWrites()
  await verifyAutocompletes()

  async function verifyReturnGrid() {
    await page.getByRole('button', { name: 'Edit' }).click()
    await page.getByRole('button', { name: 'Roll txns', exact: true }).click()
    await page.locator('section table thead th').first().waitFor({ timeout: 5000 })
    await awaitRowCount((await api('/roll-txns')).length)

    const returnSourceCells = await page.$$eval('section table tbody tr', (rows) =>
      rows
        .filter((row) => row.querySelector('select')?.value === 'return')
        .map((row) => row.cells[5]?.textContent?.trim()),
    )
    ok(
      'source type is inert on return rows',
      returnSourceCells.length >= 1 && returnSourceCells.every((text) => text === '—'),
      JSON.stringify(returnSourceCells),
    )

    const returnDenoms = await page.$$eval('section table tbody tr', (rows) =>
      rows
        .filter((row) => row.querySelector('select')?.value === 'return')
        .map((row) => {
          const select = row.querySelectorAll('select')[1]
          return { value: select?.value, label: select?.selectedOptions[0]?.textContent?.trim() }
        }),
    )
    ok(
      'mixed return denom binds to the Mixed option',
      returnDenoms.length >= 1 && returnDenoms.every((denom) => denom.value === '' && denom.label === 'Mixed'),
      JSON.stringify(returnDenoms),
    )
  }

  async function verifyLocationsAndMergeWrites() {
    const locationCells = () =>
      page.locator('tbody tr:has(button[title="Delete row"]) input[list="dl-holdings-location"]')
    const commit = async (input, value) => {
      await input.fill(value)
      const saved = page
        .waitForResponse(
          (response) => response.request().method() === 'PUT' && /\/api\/lots\//.test(response.url()),
          { timeout: 5000 },
        )
        .catch(() => {})
      await input.blur()
      await saved
    }

    await goHoldings()
    ok(
      'Holdings grid shows a Location column',
      (await page.locator('section table thead th', { hasText: 'Location' }).count()) === 1,
    )
    const emptyLocations = await locationCells().evaluateAll((elements) => elements.map((element) => element.value))
    ok(
      'an unfiled lot renders a blank location',
      emptyLocations.length >= 1 && emptyLocations.every((value) => value === ''),
      JSON.stringify(emptyLocations),
    )

    await commit(locationCells().first(), 'home safe')
    const filed = (await api('/lots')).filter((lot) => lot.location === 'home safe')
    ok('editing Location persists', filed.length === 1, `${filed.length} lot(s) @ home safe`)
    await page.reload({ waitUntil: 'networkidle' })
    await goHoldings()
    ok('Location survives a reload', (await locationCells().first().inputValue()) === 'home safe')

    const wiredToDatalist =
      (await locationCells().first().getAttribute('list')) === 'dl-holdings-location' &&
      (await page.locator('datalist#dl-holdings-location').count()) === 1
    ok('Location is wired to its suggestion datalist', wiredToDatalist)
    await commit(locationCells().nth(2), 'safe deposit box 214')
    ok(
      'a novel Location saves as free text',
      (await api('/lots')).some((lot) => lot.location === 'safe deposit box 214'),
    )

    const summary = await api('/summary')
    ok(
      'editing Location leaves the recorded sale intact',
      (summary.realized || []).length === 1 && Math.abs(summary.realized_gain - 250) < 0.01,
      `realized ${summary.realized?.length}, gain ${summary.realized_gain}`,
    )

    const sold = (await api('/lots')).find((lot) => lot.disposed)
    ok('fixture: the gold eagle sale is on the books', Boolean(sold), sold ? `lot ${sold.id}` : 'none disposed')
    const notes = "grandfather's, do not sell"
    const attributes = '{"grade":"MS65"}'
    await apiPut(`/lots/${sold.id}`, { notes, insured_value: 4500, attributes })

    await page.reload({ waitUntil: 'networkidle' })
    await goHoldings()
    const soldRow = (await api('/lots')).findIndex((lot) => lot.id === sold.id)
    await commit(locationCells().nth(soldRow), 'wall safe')
    const edited = (await api('/lots')).find((lot) => lot.id === sold.id)
    ok('the Location edit lands', edited?.location === 'wall safe', `location "${edited?.location}"`)
    ok('a grid edit does not wipe notes', edited?.notes === notes, JSON.stringify(edited?.notes))
    ok('a grid edit does not wipe insured value', edited?.insured_value === 4500, String(edited?.insured_value))
    ok('a grid edit does not wipe attributes', edited?.attributes === attributes, String(edited?.attributes))
    ok(
      'a grid edit does not resurrect a sold lot',
      edited?.disposed === sold.disposed && edited?.disposed_usd === sold.disposed_usd,
      `disposed "${edited?.disposed}" @ ${edited?.disposed_usd}`,
    )
    const afterEdit = await api('/summary')
    ok(
      'realized P&L survives a sold-lot edit',
      (afterEdit.realized || []).length === 1 && Math.abs(afterEdit.realized_gain - 250) < 0.01,
      `realized ${afterEdit.realized?.length}, gain ${afterEdit.realized_gain}`,
    )
  }

  async function verifyAutocompletes() {
    await page.reload({ waitUntil: 'networkidle' })
    await goHoldings()
    const options = (id) =>
      page.locator(`datalist#${id} option`).evaluateAll((elements) => elements.map((element) => element.value))
    const lots = await api('/lots')
    const ownValues = (field) => [...new Set(lots.map((lot) => lot[field]).filter(Boolean))]

    const locationOptions = await options('dl-holdings-location')
    ok(
      'Location autocomplete offers entered values',
      ownValues('location').length > 0 && ownValues('location').every((value) => locationOptions.includes(value)),
      `datalist ${JSON.stringify(locationOptions)}`,
    )
    const sourceOptions = await options('dl-holdings-source')
    ok(
      'Source autocomplete offers entered values',
      ownValues('source').length > 0 && ownValues('source').every((value) => sourceOptions.includes(value)),
      `datalist ${JSON.stringify(sourceOptions)} vs lots ${JSON.stringify(ownValues('source'))}`,
    )
    const productOptions = await options('dl-holdings-product')
    const catalogNames = (await api('/item-types')).map((type) => type.name)
    ok(
      'Product autocomplete offers catalog entries and presets',
      catalogNames.length > 0 &&
        catalogNames.every((name) => productOptions.includes(name)) &&
        productOptions.includes('90% dime (pre-1965)'),
      `${productOptions.length} options; catalog ${JSON.stringify(catalogNames)}`,
    )

    const novelCategory = 'Civil War token'
    await apiPut(`/lots/${lots[0].id}`, { category: novelCategory })
    await page.reload({ waitUntil: 'networkidle' })
    await goHoldings()
    const categoryOptions = await options('dl-holdings-category')
    ok(
      'Category autocomplete offers a user-created category',
      categoryOptions.includes(novelCategory) && categoryOptions.length >= 11,
      `datalist ${JSON.stringify(categoryOptions)}`,
    )

    await page.getByRole('button', { name: 'Roll txns', exact: true }).click()
    await page.locator('section table thead th').first().waitFor({ timeout: 5000 })
    await awaitRowCount((await api('/roll-txns')).length)
    const bankOptions = await options('dl-roll-transactions-bank')
    const usedBanks = [...new Set((await api('/roll-txns')).map((row) => row.bank).filter(Boolean))]
    ok(
      'Bank autocomplete offers used branches',
      usedBanks.length > 0 && usedBanks.every((bank) => bankOptions.includes(bank)),
      `datalist ${JSON.stringify(bankOptions)} vs used ${JSON.stringify(usedBanks)}`,
    )
  }
}
