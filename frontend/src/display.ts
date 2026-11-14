import { ref } from 'vue'
import { api, unwrap } from './api/client'

const currency = ref('')
export async function loadCurrency() {
  currency.value = unwrap(await api.GET('/api/v1/config')).price_currency
}
export const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
export function dateTime(value: string) {
  return new Intl.DateTimeFormat('ru', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}
export function price(value: string) {
  const code = currency.value
  return new Intl.NumberFormat('ru', { minimumFractionDigits: 2, maximumFractionDigits: 2,
    ...(code ? { style: 'currency', currency: code } : {}) }).format(Number(value))
}
