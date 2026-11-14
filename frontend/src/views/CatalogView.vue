<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { api, unwrap } from '../api/client'
import type { components } from '../api/schema'
import { price } from '../display'

type Schema = components['schemas']
const route = useRoute()
const people = ref<Schema['Consultant'][]>([])
const types = ref<Schema['ConsultationType'][]>([])
const offerings = ref<Schema['Offering'][]>([])
const person = computed(() => people.value.find(c => c.consultant_id === Number(route.params.id)))
const available = computed(() => route.params.id ? types.value.filter(type => offerings.value.some(o => o.consultant_id === person.value?.consultant_id && o.consultation_type_id === type.consultation_type_id)) : types.value)
const error = ref('')
const loading = ref(false)
async function load() {
  loading.value = true
  error.value = ''
  try {
    const [c, t, o] = await Promise.all([api.GET('/api/v1/consultants'), api.GET('/api/v1/consultation-types'), api.GET('/api/v1/offerings')])
    people.value = unwrap(c) ?? []
    types.value = unwrap(t) ?? []
    offerings.value = unwrap(o) ?? []
  } catch (e) { error.value = e instanceof Error ? e.message : 'Каталог недоступен.' }
  finally { loading.value = false }
}
void load()
</script>

<template>
  <section v-if="!route.params.id" class="hero">
    <div class="stack">
      <p class="eyebrow">Бюро консультаций · Нью-Джерси</p>
      <h1>Вывоз отходов.<br>Начнём с разговора.</h1>
      <p>Выберите услугу и время. Консультант поможет разобраться с вывозом отходов для дома или бизнеса.</p>
    </div>
    <img src="/images/yard-placeholder.svg" alt="" width="560" height="360">
  </section>
  <div v-else class="stack"><RouterLink to="/">В каталог</RouterLink><h1>{{ person?.name ?? (loading ? 'Консультант' : 'Консультант не найден') }}</h1><p v-if="person">{{ person.bio || 'Описание пока не добавлено.' }}</p></div>
  <p v-if="loading" role="status">Загружаем каталог…</p>
  <div v-else-if="error" role="alert"><p class="error">{{ error }}</p><button @click="load">Повторить</button></div>
  <template v-else-if="!route.params.id || person">
    <section aria-labelledby="services"><h2 id="services">Услуги и запись</h2>
      <p v-if="!available.length">Доступных услуг сейчас нет.</p>
      <ul v-else class="ledger">
        <li v-for="type in available" :key="type.consultation_type_id">
          <div class="stack"><h3>{{ type.title }}</h3><p>{{ type.description }}</p></div>
          <span class="terms">{{ type.duration_minutes }} мин. · {{ price(type.price) }}</span>
          <RouterLink v-if="$router.hasRoute('reserve')" class="button" :aria-label="`Записаться: ${type.title}`" :to="{ name: 'reserve', query: { type: type.consultation_type_id, consultant: person?.consultant_id } }">Записаться</RouterLink>
        </li>
      </ul>
    </section>
    <section v-if="!route.params.id && people.length" aria-labelledby="staff"><h2 id="staff">Консультанты</h2>
      <ul class="ledger"><li v-for="c in people" :key="c.consultant_id"><RouterLink :to="`/consultants/${c.consultant_id}`">{{ c.name }}</RouterLink></li></ul>
    </section>
  </template>
</template>

<style scoped lang="scss">
.hero { display: grid; grid-template-columns: 1.15fr 1fr; gap: 2rem; align-items: center; border-block: 3px double var(--rule); padding-block: 1.5rem; }
.hero img { width: 100%; height: auto; border: 1px solid var(--rule); }
.eyebrow { font-size: .85rem; text-transform: uppercase; letter-spacing: .12em; color: var(--muted); }
.ledger { padding: 0; list-style: none; border-top: 1px solid var(--rule); }
.ledger li { display: grid; grid-template-columns: 1fr auto auto; gap: 1rem; align-items: center; border-bottom: 1px solid var(--rule); padding: 1rem 0; }
.terms { font-variant-numeric: tabular-nums; }
@media (max-width: 640px) { .hero, .ledger li { grid-template-columns: 1fr; } .ledger .button { justify-self: start; } }
</style>
