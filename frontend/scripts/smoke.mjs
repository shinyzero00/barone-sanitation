import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { readFile, writeFile, mkdir } from 'node:fs/promises'
import { setTimeout as sleep } from 'node:timers/promises'
import axe from 'axe-core'

const origin = process.env.UI_ORIGIN ?? 'https://localhost:5173'
const driver = process.env.WEBDRIVER_URL ?? 'http://127.0.0.1:4444'
const artifacts = process.env.UI_ARTIFACTS ?? '.artifacts'
const log = process.env.UI_OTP_LOG
assert(log && process.env.DATABASE_URL, 'Set UI_OTP_LOG and DATABASE_URL for a disposable backend')
const password = 'Disposable test password 42'
const suffix = Date.now()
const email = role => `${role}-${suffix}@example.test`
let session
let stage = 'start'

async function call(method, path, body) {
  const response = await fetch(driver + path, { method, body: body === undefined ? undefined : JSON.stringify(body), headers: { 'Content-Type': 'application/json' } })
  const { value } = await response.json()
  if (!response.ok) throw new Error(`${stage}: ${value.error}: ${value.message}`)
  return value
}
const wd = (path, body) => call(body === undefined ? 'GET' : 'POST', `/session/${session}${path}`, body)
const run = (script, ...args) => wd('/execute/sync', { script, args })
const go = path => wd('/url', { url: origin + path })
async function until(fn, label) {
  for (let n = 0; n < 70; n++) {
    const result = await fn()
    if (result) return result
    await sleep(150)
  }
  throw new Error(`${stage}: waiting for ${label}`)
}
const selector = 'element-6066-11e4-a52e-4f735466cecf'
const find = (value, using = 'css selector') => until(async () => (await wd('/elements', { using, value }))[0]?.[selector], value)
const text = value => until(() => run('return document.body.innerText.includes(arguments[0])', value), value)
const click = async css => wd(`/element/${await find(css)}/click`, {})
const button = async name => wd(`/element/${await find(`//button[normalize-space(.)=${JSON.stringify(name)} and not(@disabled)]`, 'xpath')}/click`, {})
const key = (id, text) => wd(`/element/${id}/value`, { text })
async function fill(css, value) {
  const id = await find(css)
  await wd(`/element/${id}/clear`, {})
  await key(id, value)
}
const set = (css, value) => run(`const el = document.querySelector(arguments[0]); el.value = arguments[1]; el.dispatchEvent(new Event('input', {bubbles:true})); el.dispatchEvent(new Event('change', {bubbles:true}));`, css, value)
async function otp(role, purpose, index = 0) {
  const marker = `OTP email=${email(role)} purpose=${purpose} code=`
  return until(async () => (await readFile(log, 'utf8')).split('\n').filter(line => line.startsWith(marker))[index]?.slice(marker.length), `test ${purpose} code`)
}
async function register(role, name = role) {
  stage = `register ${role}`
  await go('/register')
  if (role === 'staff') {
    const skip = await find('.skip')
    await run('document.querySelector(".skip").focus()')
    await key(skip, '\uE007')
    assert(await run('return document.activeElement.id === "content"'), 'Skip link focuses main content')
    await capture('registration-mobile', 390)
  }
  await fill('[autocomplete=name]', name)
  await fill('[autocomplete=email]', email(role))
  await fill('[autocomplete=tel]', '12025550100')
  await fill('[autocomplete=new-password]', password)
  await button('Получить код')
  await fill('[autocomplete=one-time-code]', await otp(role, 'verify_email'))
  await button('Подтвердить код')
  await text('Почта подтверждена')
}
async function login(role, index = 0, next = '', pass = password) {
  stage = `login ${role}`
  await go('/login' + (next ? `?next=${encodeURIComponent(next)}` : ''))
  await fill('[autocomplete=email]', email(role))
  await fill('[autocomplete=current-password]', pass)
  await button('Продолжить')
  await fill('[autocomplete=one-time-code]', await otp(role, 'login', index))
  await button('Подтвердить код')
  await text('Выйти')
  return Date.now() + Number(process.env.UI_RESEND_MS ?? 60000)
}
async function capture(name, width = 1360) {
  stage = `capture ${name}`
  await wd('/window/rect', { width, height: 1000 })
  await run('window.scrollTo(0, 0)')
  assert(await run('return document.querySelectorAll("main").length === 1 && document.querySelectorAll("h1").length === 1'), 'Expected one main landmark and heading')
  assert(await run('return document.documentElement.scrollWidth <= innerWidth + 1'), `${name}: horizontal overflow`)
  await writeFile(`${artifacts}/${name}.png`, Buffer.from(await wd('/screenshot'), 'base64'))
  await run(axe.source + '\nreturn true;')
  const violations = await wd('/execute/async', { script: 'const done = arguments[0]; axe.run().then(r => done(r.violations.map(v => v.id + ": " + v.nodes.map(n => n.target).join(", ")))).catch(e => done([String(e)]));', args: [] })
  assert.deepEqual(violations, [], name)
  console.log(`${name}: markup, overflow and axe passed`)
}
function localTime(delta) {
  const date = new Date(Date.now() + delta * 3600000)
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16)
}

await mkdir(artifacts, { recursive: true })
const created = await call('POST', '/session', { capabilities: { alwaysMatch: { browserName: 'firefox', acceptInsecureCerts: true, 'moz:firefoxOptions': { args: ['-headless'] } } } })
session = created.sessionId
try {
  const staffName = `Пол Гуалтьери ${String(suffix).slice(-5)}`
  await register('staff', staffName)
  await register('admin', 'Диспетчер')
  execFileSync('go', ['run', './cmd/server', 'admin', email('admin')], { cwd: new URL('../../', import.meta.url), stdio: 'pipe' })
  const adminResendAt = await login('admin', 0, '/admin')
  stage = 'configure catalog and schedule'
  await text('Виды консультаций')
  await fill('input[type=email]', email('staff'))
  await button('Найти')
  await button('Создать консультанта')
  await text('Консультант добавлен')
  const title = `Вывоз строймусора ${String(suffix).slice(-4)}`
  await fill('.form-block input:not([type])', title)
  await fill('.form-block input[inputmode=decimal]', '125.00')
  await button('Добавить вид')
  await text(title)
  const assignment = await find(`//div[@class="consultant-row"][.//strong[normalize-space(.)=${JSON.stringify(staffName)}]]//label[span[normalize-space(.)=${JSON.stringify(title)}]]/input`, 'xpath')
  await wd(`/element/${assignment}/click`, {})
  await text('Назначение обновлено')
  const staffOption = await find(`//select/option[normalize-space(.)=${JSON.stringify(staffName)}]`, 'xpath')
  await wd(`/element/${staffOption}/click`, {})
  await find('input[type=datetime-local]')
  await set('label:first-child input[type=datetime-local]', localTime(24))
  await set('label:nth-child(2) input[type=datetime-local]', localTime(26))
  await button('Добавить интервал')
  await text('Расписание сохранено')
  await capture('admin-desktop')
  await button('Выйти')
  await register('client', 'Клиент проверки')
  await login('client', 0, '/profile')
  stage = 'save address and book'
  await text('Мои адреса')
  await fill('.address-form input[required]', 'Мэдисон-стрит, 14')
  await button('Добавить')
  await text('Адрес сохранён')
  await go('/')
  await text(title)
  await capture('catalog-desktop')
  await capture('catalog-mobile', 390)
  await click(`a[aria-label="Записаться: ${title}"]`)
  await find('.slot')
  await click('.slot')
  const address = await find('//option[contains(., "Мэдисон-стрит, 14")]', 'xpath')
  await wd(`/element/${address}/click`, {})
  await capture('reservation-mobile', 390)
  await button('Подтвердить запись')
  await text('Детали консультации')
  await text('Мэдисон-стрит, 14')
  const bookingPath = await run('return location.pathname')
  await button('Выйти')
  await go(bookingPath)
  await text('Вход в кабинет')
  // Use the existing admin challenge cooldown if the first visit lasted less than a minute.
  await sleep(Math.max(0, adminResendAt - Date.now() + 1000))
  await login('admin', 1, bookingPath)
  stage = 'complete booking as admin'
  await button('Сохранить статус')
  await text('Статус записи обновлён')
  await until(() => run("return document.querySelector('select')?.value === 'completed'"), 'completed option')
  await button('Сохранить статус')
  await text('Статус записи обновлён')
  await button('Выйти')
  await login('staff', 0, bookingPath)
  stage = 'save consultant result'
  await fill('textarea[required]', 'Согласовали отдельный контейнер для строительных отходов.')
  await button('Сохранить заключение')
  await text('Заключение сохранено')
  await capture('result-desktop')
  await button('Выйти')
  await login('client', 1, bookingPath)
  await text('Согласовали отдельный контейнер')
  console.log('Firefox smoke passed for all three roles')
} finally {
  await call('DELETE', `/session/${session}`)
}
