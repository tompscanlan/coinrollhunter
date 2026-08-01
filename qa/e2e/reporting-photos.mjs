import { statSync } from 'node:fs'

const TEST_PNG_B64 =
  'iVBORw0KGgoAAAANSUhEUgAAADAAAAAwCAIAAADYYG7QAAAAUklEQVR4nOzOoQ3AMAADQYN0745edYKnAXeyjP9s77Pds/PfTQQVQUVQEVQEFUFFUBFUBBVBRVARVAQVQUVQEVQEFUFFUBFUBBVBRVD5AgAA//9IYgMXBWAfpAAAAABJRU5ErkJggg=='
const PDF_BYTES = '%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF\n'

export async function runReportingAndPhotos(h, screenshotDir) {
  const { page, base, ok, api, apiPost, apiDelete, awaitApi, awaitRowCount } = h

  await verifyReports()
  const photoLot = await createTrophyLot()
  await verifyPhotoLifecycle(photoLot)
  await verifyDocumentOnlyTrophy(photoLot)
  await verifySettingsAndExport()
  await verifyDataHealthPanel()

  async function verifyReports() {
    await page.getByRole('button', { name: 'Edit' }).click()
    await page.getByRole('button', { name: 'Losses', exact: true }).click()
    await page.locator('section table thead th').first().waitFor({ timeout: 5000 })
    const apiLosses = (await api('/losses')).length
    await awaitRowCount(apiLosses)
    const lossRows = await page.locator('section table tbody tr:has(button[title="Delete row"])').count()
    ok('Losses grid round-trips', lossRows === apiLosses && apiLosses >= 1, `dom ${lossRows} vs api ${apiLosses}`)

    await page.getByRole('button', { name: 'Overview', exact: true }).click()
    await page.getByText('All cashed in', { exact: false }).first().waitFor({ timeout: 5000 })
    ok('overview reconciliation banner shows lost', (await page.getByText('lost', { exact: false }).count()) > 0)
    ok('Overview shows both CRH verdicts', (await page.getByText('Lifetime', { exact: false }).count()) > 0)

    const summary = await api('/summary')
    ok('KPI: buy_count present', summary.buy_count >= 1, `buy_count ${summary.buy_count}`)
    ok(
      'KPI cards render (Buys/Branches/Avg buy)',
      (await page.getByText('Buys', { exact: true }).count()) > 0 &&
        (await page.getByText('Branches', { exact: true }).count()) > 0 &&
        (await page.getByText('Avg buy', { exact: true }).count()) > 0,
    )
    ok('spot freshness chip visible', await page.locator('span[title*="background"]').first().isVisible())

    const findsReport = await api('/finds-report')
    ok(
      'finds-report endpoint shape',
      typeof findsReport.total_face_searched === 'number' && Array.isArray(findsReport.denoms),
      `face ${findsReport.total_face_searched}, denoms ${findsReport.denoms?.length}`,
    )
    await page.screenshot({ path: `${screenshotDir}/do-overview.png`, fullPage: true }).catch(() => {})
    await page.getByRole('button', { name: 'Insights', exact: true }).click()
    await page.getByRole('heading', { name: 'Hit rate — 1 per face $' }).waitFor({ timeout: 5000 })
    ok('hit-rate grid heading renders', await page.getByRole('heading', { name: 'Hit rate — 1 per face $' }).isVisible())
  }

  async function createTrophyLot() {
    await page.getByRole('button', { name: 'Edit' }).click()
    await page.getByRole('button', { name: 'Holdings', exact: true }).click()
    await page.locator('section table thead th').first().waitFor({ timeout: 5000 })
    const newRow = page.locator('tbody tr').filter({ has: page.locator('button[title="Add row"]') })
    await newRow.locator('select').first().selectOption('crh')
    await newRow.getByPlaceholder('1 oz Gold Eagle').fill('Mercury dime (trophy)')
    await newRow.getByPlaceholder('Silver').fill('Silver')
    await newRow.getByPlaceholder('Mercury').fill('Mercury')
    await newRow.locator('input[type=checkbox]').first().check()
    await newRow.locator('button[title="Add row"]').click()

    const isTrophy = (lot) => lot.category === 'Silver' && lot.subcategory === 'Mercury' && lot.trophy === true
    await awaitApi('/lots', (lots) => lots.some(isTrophy))
    const lot = (await api('/lots')).find(isTrophy)
    ok('Holdings taxonomy persists (category/subcategory/trophy)', Boolean(lot), lot ? `lot ${lot.id}` : 'not found')
    const summary = await api('/summary')
    ok(
      'summary survives a zero-basis find (unreal_pct null, not +Inf)',
      Array.isArray(summary.lots) && summary.lots.length >= 1,
      `lots ${summary.lots?.length}`,
    )
    ok('a fresh lot carries a stable uid for photos', /^[0-9a-f-]{36}$/.test(lot?.uid || ''), lot?.uid)
    return lot
  }

  async function upload(ownerUid, role, bytes, type, filename) {
    const form = new FormData()
    form.append('owner_kind', 'lot')
    form.append('owner_uid', ownerUid)
    form.append('role', role)
    form.append('file', new Blob([bytes], { type }), filename)
    const response = await fetch(base + '/api/photos', { method: 'POST', body: form })
    return { response, photo: await response.json() }
  }

  async function verifyPhotoLifecycle(lot) {
    const png = Buffer.from(TEST_PNG_B64, 'base64')
    const { response, photo } = await upload(lot.uid, 'obverse', png, 'image/png', 'coin.png')
    ok(
      'photo upload creates the first sequenced photo',
      response.status === 201 && photo.seq === 1 && /^[0-9a-f-]{36}$/.test(photo.uid || ''),
      `status ${response.status} seq ${photo.seq}`,
    )
    ok('the gallery lists the uploaded photo', (await api(`/photos?owner_kind=lot&owner_uid=${lot.uid}`)).length === 1)

    const receiptUpload = await upload(lot.uid, 'receipt', png, 'image/png', 'receipt.png')
    ok(
      'a receipt-tagged upload stores role=receipt',
      receiptUpload.response.status === 201 && receiptUpload.photo.role === 'receipt',
      `status ${receiptUpload.response.status} role ${receiptUpload.photo.role}`,
    )
    await apiDelete(`/photos/${receiptUpload.photo.id}`)

    const pdfUpload = await upload(lot.uid, 'receipt', PDF_BYTES, 'application/pdf', 'invoice.pdf')
    const pdf = pdfUpload.photo
    ok(
      'a PDF receipt stores as ext=pdf without imaging',
      pdfUpload.response.status === 201 && pdf.ext === 'pdf' && pdf.role === 'receipt',
      `status ${pdfUpload.response.status} ext ${pdf.ext} role ${pdf.role}`,
    )
    const pdfFile = await fetch(`${base}/api/photos/${pdf.uid}/file?variant=original`)
    ok(
      'a PDF original serves safely as an attachment',
      pdfFile.status === 200 &&
        pdfFile.headers.get('content-type') === 'application/pdf' &&
        pdfFile.headers.get('x-content-type-options') === 'nosniff' &&
        /^attachment/.test(pdfFile.headers.get('content-disposition') || ''),
      `status ${pdfFile.status} ct ${pdfFile.headers.get('content-type')}`,
    )
    const pdfThumb = await fetch(`${base}/api/photos/${pdf.uid}/file?variant=thumb`)
    ok(
      'a PDF thumbnail is a non-HTML 404',
      pdfThumb.status === 404 && !/html/i.test(pdfThumb.headers.get('content-type') || ''),
      `status ${pdfThumb.status}`,
    )
    await apiDelete(`/photos/${pdf.id}`)

    const fileResponse = await fetch(`${base}/api/photos/${photo.uid}/file?variant=display`)
    ok(
      'the photo route serves an image instead of the SPA',
      fileResponse.ok && /image\//.test(fileResponse.headers.get('content-type') || ''),
      fileResponse.headers.get('content-type'),
    )
    const missing = await fetch(`${base}/api/photos/not-a-uid/file`)
    ok(
      'a bad photo uid is a non-HTML 404',
      missing.status === 404 && !/html/i.test(missing.headers.get('content-type') || ''),
      `status ${missing.status}`,
    )

    ok('Holdings rows offer a Photos button', (await page.locator('button[title="Photos"]').count()) > 0)
    await page.locator('button[title="Photos"]').first().click()
    const drawer = page.locator('[aria-label="Coin photos"]')
    await drawer.waitFor({ timeout: 5000 })
    ok('the detail drawer offers Add a photo', await drawer.getByRole('button', { name: 'Add a photo' }).isVisible())
    ok('the upload role picker offers Receipt', (await drawer.locator('select option[value="receipt"]').count()) > 0)
    await drawer.locator('button[title="Close"]').click()
    await drawer.waitFor({ state: 'detached', timeout: 5000 })

    await apiDelete(`/photos/${photo.id}`)
    ok('a soft-deleted photo leaves the gallery', (await api(`/photos?owner_kind=lot&owner_uid=${lot.uid}`)).length === 0)
    await page.getByRole('button', { name: 'Keepers', exact: true }).click()
    await page.locator('section table thead th').first().waitFor({ timeout: 5000 })
    ok('keepers have no photo affordance', (await page.locator('button[title="Photos"]').count()) === 0)
    await page.getByRole('button', { name: 'Holdings', exact: true }).click()
    await page.locator('section table thead th').first().waitFor({ timeout: 5000 })
  }

  async function verifyDocumentOnlyTrophy(lot) {
    const { photo } = await upload(lot.uid, 'receipt', PDF_BYTES, 'application/pdf', 'invoice.pdf')
    const brokenImageRequests = []
    const trackResponse = (response) => {
      if (response.url().includes(photo.uid) && /variant=(display|thumb)/.test(response.url())) {
        brokenImageRequests.push(`${response.status()} ${response.url()}`)
      }
    }
    page.on('response', trackResponse)
    const feedListed = page
      .waitForResponse((response) => response.url().includes(`/photos?owner_kind=lot&owner_uid=${lot.uid}`), {
        timeout: 5000,
      })
      .catch(() => null)
    await page.getByRole('button', { name: 'Insights', exact: true }).click()
    await page.getByRole('heading', { name: 'Greatest hits' }).waitFor({ timeout: 5000 })
    ok('trophy feed shows the trophy', (await page.getByText('Mercury dime (trophy)', { exact: false }).count()) > 0)
    await feedListed
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
    ok('the trophy feed never requests an image variant for a PDF', brokenImageRequests.length === 0, brokenImageRequests.join(' ; '))
    ok('a document-only trophy contributes no hero image', (await page.locator('section:has(h2:has-text("Greatest hits")) img').count()) === 0)
    page.off('response', trackResponse)
    await apiDelete(`/photos/${photo.id}`)
  }

  async function verifySettingsAndExport() {
    await page.locator('button[title="Settings"]').click()
    const dialog = page.locator('[role="dialog"]')
    await dialog.getByRole('heading', { name: 'Settings' }).waitFor({ timeout: 5000 })
    ok('settings offers a data export', (await dialog.getByRole('heading', { name: 'Your data' }).count()) > 0)
    ok('export warns about photo location data', await dialog.getByText('where the photo was taken', { exact: false }).isVisible())
    ok('settings offers the EXIF strip toggle', await dialog.getByText('Strip camera metadata', { exact: false }).isVisible())

    const [download] = await Promise.all([
      page.waitForEvent('download', { timeout: 15000 }),
      dialog.getByRole('link', { name: 'Export my data' }).click(),
    ])
    const bundle = await download.path()
    const bytes = bundle ? statSync(bundle).size : 0
    ok(
      'export downloads a non-empty zip bundle',
      bytes > 0 && /^coinrollhunter-export-\d{4}-\d{2}-\d{2}\.zip$/.test(download.suggestedFilename()),
      `${download.suggestedFilename()} — ${bytes} bytes`,
    )

    await dialog.locator('input[type=number]').first().fill('0.85')
    await page.getByRole('button', { name: 'Save settings' }).click()
    await dialog.waitFor({ state: 'detached', timeout: 5000 })
    const settings = await api('/settings')
    ok(
      'settings modal persists buyback factor',
      settings.silver_buyback_factor_90pct === 0.85,
      `90pct ${settings.silver_buyback_factor_90pct}`,
    )
  }

  // The panel had no browser coverage, and its whole job is to speak to a user whose
  // ledger is damaged — so a render that dies on a finding fails silently in exactly
  // the case it exists for (a keyed `each` over non-unique findings did precisely
  // that: a permanent "Checking…" and nothing else). The suite's page-error gate is
  // the guard; this block's job is to make the panel actually render a finding.
  //
  // Honest limit: the fixture is an ORPHAN, because that is the only finding class
  // reachable through the API. Invalid rows exist only in databases that went around
  // the validators, which no HTTP client can do — those are covered in Go
  // (internal/doctor) and cannot be reproduced from here.
  async function verifyDataHealthPanel() {
    const box = await apiPost('/roll-txns', {
      date: '2026-01-07', bank: 'QA Health Bank', action: 'buy',
      denom: 'dimes', unit: 'box', amount: 1, face_usd: 250,
    })
    const find = await apiPost('/lots', {
      item_type_id: (await api('/item-types'))[0].id, roll_txn_id: box.id,
      activity: 'crh', qty: 1, basis_usd: 0.1, face_value_usd: 0.1, acquired: '2026-01-07',
    })
    // The everyday correction workflow — delete the box you just entered wrong. The find
    // now points at a uid no row has, and every read path resolves that to a BLANK box,
    // which is indistinguishable from a find that never had one. Only this scan can see it.
    await apiDelete('/roll-txns/' + box.id)

    await page.locator('button[title^="Data health"]').click()
    const health = page.locator('[role="dialog"]')
    await health.getByRole('heading', { name: 'Data health' }).waitFor({ timeout: 5000 })
    await health.getByText('These point at something that was deleted', { exact: false })
      .waitFor({ timeout: 5000 })
    ok(
      'the data-health panel renders its findings (not a stuck "Checking…")',
      !(await health.getByText('Checking…', { exact: false }).isVisible()),
    )
    ok(
      'a finding names the row to go fix, not just a count',
      await health.getByText(`lots #${find.id}`, { exact: false }).isVisible(),
    )
    // No repair affordance, deliberately: heuristic repair false-positives on correct
    // data, and a false positive on a ledger is unrecoverable money loss with no undo.
    ok(
      'the panel promises read-only and offers no "fix it" button',
      (await health.getByText('never changes anything', { exact: false }).count()) > 0 &&
        (await health.getByRole('button', { name: /fix|repair/i }).count()) === 0,
    )

    await health.getByRole('button', { name: 'Close' }).click()
    await health.waitFor({ state: 'detached', timeout: 5000 })
    // Closing re-scans: the user's next move after reading this is to go fix a row, and
    // a stale dot would tell them their fix did nothing.
    await page.waitForFunction(
      () => /\d+ thing/.test(document.querySelector('button[title^="Data health"]')?.title || ''),
      null, { timeout: 5000 })
    ok(
      'closing the panel re-checks, so the header dot is not stale',
      /\d+ thing/.test(await page.locator('button[title^="Data health"]').getAttribute('title')),
    )

    // Isolate: drop the orphan so the grid modules that run after this one see the lots
    // they did before it — they select rows positionally.
    await apiDelete('/lots/' + find.id)
  }
}
