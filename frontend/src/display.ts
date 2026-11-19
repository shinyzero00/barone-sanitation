import { ref } from 'vue'
import { api, unwrap } from './api/client'

const currency = ref('')
export async function loadCurrency() {
  currency.value = unwrap(await api.GET('/api/v1/config')).price_currency
}
export const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
const statuses: Record<string, string> = {
  pending: 'Ожидает подтверждения', confirmed: 'Подтверждена', completed: 'Завершена',
  cancelled: 'Отменена', no_show: 'Неявка',
}
export const statusLabel = (status: string) => statuses[status] ?? status
export function dateTime(value: string) {
  return new Intl.DateTimeFormat('ru', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}
export function price(value: string) {
  const code = currency.value
  return new Intl.NumberFormat('ru', { minimumFractionDigits: 2, maximumFractionDigits: 2,
    ...(code ? { style: 'currency', currency: code } : {}) }).format(Number(value))
}
