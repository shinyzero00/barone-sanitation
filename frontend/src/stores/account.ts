import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, ApiError, unwrap } from '../api/client'
import type { components } from '../api/schema'

export const useAccount = defineStore('account', () => {
  const profile = ref<components['schemas']['Profile'] | null>(null)
  let pending: Promise<void> | undefined
  function refresh() {
    pending ??= api.GET('/api/v1/me').then(result => {
      profile.value = result.response.status === 401 ? null : unwrap(result)
    }).finally(() => { pending = undefined })
    return pending
  }
  async function logout() {
    try { unwrap(await api.POST('/api/v1/auth/logout')) }
    catch (error) { if (!(error instanceof ApiError && error.status === 401)) throw error }
    profile.value = null
  }
  return { profile, refresh, logout }
})
