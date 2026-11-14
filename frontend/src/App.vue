<script setup lang="ts">
import { ref, nextTick, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAccount } from './stores/account'
import { loadCurrency } from './display'

const account = useAccount()
const router = useRouter()
const route = useRoute()
const main = ref<HTMLElement>()
const error = ref('')
const busy = ref(false)
void loadCurrency().catch(() => {})
watch(() => route.path, async () => { await nextTick(); main.value?.focus() })
async function logout() {
  busy.value = true
  error.value = ''
  try { await account.logout(); await router.push('/') }
  catch { error.value = 'Не удалось выйти. Попробуйте ещё раз.' }
  finally { busy.value = false }
}
</script>

<template>
  <a class="skip" href="#content">К содержанию</a>
  <header class="masthead">
    <div class="wrap masthead-inner">
      <RouterLink class="wordmark" to="/">Barone <span>Sanitation</span></RouterLink>
      <nav aria-label="Основная навигация" class="inline">
        <RouterLink to="/">Каталог</RouterLink>
        <template v-if="account.profile">
          <RouterLink v-if="router.hasRoute('bookings')" to="/bookings">{{ account.profile.role === 'client' ? 'Мои записи' : 'Журнал записей' }}</RouterLink>
          <RouterLink v-if="account.profile.role === 'admin' && router.hasRoute('admin')" to="/admin">Управление</RouterLink>
          <RouterLink v-if="router.hasRoute('profile')" to="/profile">Профиль</RouterLink>
          <button class="quiet" :disabled="busy" @click="logout">Выйти</button>
        </template>
        <RouterLink v-else-if="router.hasRoute('auth')" to="/login">Войти</RouterLink>
      </nav>
    </div>
  </header>
  <main id="content" ref="main" class="wrap content" tabindex="-1">
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <RouterView :key="route.path" />
  </main>
  <footer class="wrap footer"><b>Barone Sanitation</b><span>Служба консультаций по вывозу отходов</span></footer>
</template>
