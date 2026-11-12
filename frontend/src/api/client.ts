import createClient from 'openapi-fetch'
import type { paths } from './schema'

export const api = createClient<paths>({ credentials: 'same-origin' })

const messages: Record<string, string> = {
  unauthorized: 'Войдите снова, чтобы продолжить.', forbidden: 'Недостаточно прав.',
  not_found: 'Запись не найдена.', invalid_credentials: 'Проверьте почту, пароль и подтверждение почты.',
  invalid_code: 'Код неверен, истёк или больше не действует.', code_locked: 'Попытки исчерпаны. Запросите новый код после паузы.',
  code_cooldown: 'Новый код пока нельзя запросить.', rate_limited: 'Слишком много запросов. Повторите позже.',
  invalid_request: 'Проверьте заполнение полей.', booking_overlap: 'Это время уже занято.',
  outside_work_interval: 'Это время больше недоступно.', inactive_offering: 'Услуга больше недоступна.',
  consultation_result_exists: 'Заключение уже сохранено.', mail_unavailable: 'Не удалось отправить письмо. Повторите отправку позже.',
  invalid_transition: 'Статус уже изменился или переход недоступен.', invalid_phone: 'Проверьте номер телефона.',
}

export class ApiError extends Error {
  constructor(public status: number, public code: string, public retryAfter: number) {
    super(messages[code] ?? (status === 422 ? 'Проверьте заполнение полей.' : `Не удалось выполнить запрос (${status}).`))
  }
}

export function unwrap<T>(result: { data?: T; error?: unknown; response: Response }): T {
  if (!result.response.ok) {
    const code = (result.error as { error?: string } | undefined)?.error ?? ''
    throw new ApiError(result.response.status, code, Number(result.response.headers.get('Retry-After')) || 0)
  }
  return result.data as T
}

let csrf: Promise<string> | undefined
api.use({
  async onRequest({ request }) {
    if (['GET', 'HEAD', 'OPTIONS'].includes(request.method)) return
    csrf ??= fetch('/csrf', { credentials: 'same-origin' }).then(async response => {
      if (!response.ok) throw new Error('Не удалось подготовить защищённый запрос.')
      return (await response.json() as { csrf_token: string }).csrf_token
    }).catch(error => { csrf = undefined; throw error })
    request.headers.set('X-CSRF-Token', await csrf)
  },
  async onResponse({ response }) {
    if (response.status === 403 && (await response.clone().json().catch(() => null))?.error === 'invalid_csrf') csrf = undefined
  },
})
