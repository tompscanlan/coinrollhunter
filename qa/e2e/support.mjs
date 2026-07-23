export function createHarness(page, base) {
  const results = []
  const ok = (name, condition, extra = '') => {
    results.push({ name, pass: Boolean(condition), extra })
    console.log(`${condition ? 'PASS' : 'FAIL'}  ${name}${extra ? `  — ${extra}` : ''}`)
  }

  const request = async (method, path, body) => {
    const response = await fetch(base + '/api' + path, {
      method,
      headers: body === undefined ? undefined : { 'content-type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    if (!response.ok) throw new Error(`${method} ${path} → ${response.status}`)
    if (response.status === 204) return undefined
    const text = await response.text()
    return text ? JSON.parse(text) : undefined
  }

  const api = (path) => request('GET', path)
  const apiPut = (path, body) => request('PUT', path, body)
  const apiPost = (path, body) => request('POST', path, body)
  const apiDelete = (path) => request('DELETE', path)

  const goDo = async () => {
    await page.getByRole('button', { name: 'Do' }).click()
    await page.getByRole('heading', { name: 'What did you do?' }).waitFor({ timeout: 5000 })
  }

  const tile = (name) => page.locator('button.group', { hasText: name })
  const dataRowSelector = 'section table tbody tr[data-row-index]'

  const awaitRowCount = (count, { selector = dataRowSelector, exact = false } = {}) =>
    page
      .waitForFunction(
        ([rowSelector, expected, requireExact]) => {
          const current = document.querySelectorAll(rowSelector).length
          return requireExact ? current === expected : current >= expected
        },
        [selector, count, exact],
        { timeout: 5000 },
      )
      .catch(() => {})

  const awaitApi = async (path, predicate, { tries = 60, gap = 100 } = {}) => {
    let value
    for (let attempt = 0; attempt < tries; attempt++) {
      value = await api(path)
      if (predicate(value)) break
      await new Promise((resolve) => setTimeout(resolve, gap))
    }
    return value
  }

  const goHoldings = async () => {
    await page.getByRole('button', { name: 'Edit', exact: true }).click()
    await page.getByRole('button', { name: 'Holdings', exact: true }).click()
    await page.locator('section table thead th').first().waitFor({ timeout: 5000 })
    await awaitRowCount(1)
  }

  return {
    page,
    base,
    results,
    ok,
    api,
    apiPut,
    apiPost,
    apiDelete,
    goDo,
    goHoldings,
    tile,
    awaitRowCount,
    awaitApi,
  }
}
