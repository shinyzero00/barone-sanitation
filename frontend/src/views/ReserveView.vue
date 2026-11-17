<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import type { components } from '../api/schema'
import { ApiError, api, unwrap } from '../api/client'
import { dateTime, price, zone } from '../display'

type Offering = components['schemas']['Offering']
type ConsultationType = components['schemas']['ConsultationType']
type Consultant = components['schemas']['Consultant']
type Slot = components['schemas']['Slot']
type Address = components['schemas']['Address']

const route = useRoute()
const router = useRouter()
const offerings = ref<Offering[]>([])
const types = ref<ConsultationType[]>([])
const consultants = ref<Consultant[]>([])
const addresses = ref<Address[]>([])
const slots = ref<Slot[]>([])
const selectedOfferingId = ref<number | null>(null)
const selectedSlot = ref<Slot | null>(null)
const selectedAddressId = ref<number | null>(null)
const from = ref(new Date().toISOString().slice(0, 10))
const to = ref(new Date(Date.now() + 1000 * 60 * 60 * 24 * 30).toISOString().slice(0, 10))
const loading = ref(true)
const loadingSlots = ref(false)
const submitting = ref(false)
const error = ref('')
const notice = ref('')
let slotsRequest = 0

const typeId = Number(route.query.type) || null
const consultantId = Number(route.query.consultant) || null
const selectedOffering = computed(() => offerings.value.find((item) => item.consultation_offering_id === selectedOfferingId.value))
const selectedType = computed(() => types.value.find((item) => item.consultation_type_id === selectedOffering.value?.consultation_type_id))
const selectedConsultant = computed(() => consultants.value.find((item) => item.consultant_id === selectedOffering.value?.consultant_id))

function showError(reason: unknown) {
  error.value = reason instanceof Error ? reason.message : 'Операция не выполнена. Попробуйте ещё раз.'
  notice.value = ''
}

function isoDay(value: string, end = false) {
  return new Date(`${value}T${end ? '23:59:59' : '00:00:00'}`).toISOString()
}

async function loadSlots() {
  if (!selectedOfferingId.value || !from.value || !to.value) return
  const request = ++slotsRequest
  loadingSlots.value = true
  selectedSlot.value = null
  try {
    const response = await api.GET('/api/v1/offerings/{id}/slots', { params: { path: { id: selectedOfferingId.value }, query: { from: isoDay(from.value), to: isoDay(to.value, true) } } })
    const result = (await unwrap(response)) ?? []
    if (request === slotsRequest) slots.value = result
  } catch (reason) {
    if (request === slotsRequest) showError(reason)
  } finally {
    if (request === slotsRequest) loadingSlots.value = false
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const responses = await Promise.all([
      api.GET('/api/v1/offerings'),
      api.GET('/api/v1/consultation-types'),
      api.GET('/api/v1/consultants'),
      api.GET('/api/v1/addresses'),
    ])
    offerings.value = (await unwrap(responses[0])) ?? []
    types.value = (await unwrap(responses[1])) ?? []
    consultants.value = (await unwrap(responses[2])) ?? []
    addresses.value = (await unwrap(responses[3])) ?? []
    selectedOfferingId.value = offerings.value.find((item) => item.consultation_type_id === typeId && (!consultantId || item.consultant_id === consultantId))?.consultation_offering_id ?? null
    if (selectedOfferingId.value) await loadSlots()
  } catch (reason) {
    showError(reason)
  } finally {
    loading.value = false
  }
}

async function submit() {
  if (!selectedOfferingId.value || !selectedSlot.value) return
  submitting.value = true
  error.value = ''
  try {
    const booking = await unwrap(await api.POST('/api/v1/bookings', { body: { consultation_offering_id: selectedOfferingId.value, starts_at: selectedSlot.value.starts_at, client_address_id: selectedAddressId.value } }))
    await router.push({ name: 'booking', params: { id: booking.consultation_booking_id } })
  } catch (reason) {
    showError(reason)
    if (reason instanceof ApiError && reason.status === 409) await loadSlots()
  } finally {
    submitting.value = false
  }
}

watch([from, to], loadSlots)
onMounted(load)
</script>

<template>
  <main class="reserve-view stack">
    <header><p class="eyebrow">Barone Sanitation / запись</p><h1>Забронировать консультацию</h1><p class="lede">Выберите специалиста, время и адрес, если консультация проходит очно.</p><span class="zone">Часовой пояс: {{ zone }}</span></header>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <p v-if="loading" class="panel">Загрузка доступных консультаций…</p>
    <template v-else>
      <section class="panel stack">
        <div class="section-heading"><div><p class="eyebrow">01 / услуга</p><h2>Что нужно забронировать</h2></div></div>
        <label>Услуга<select v-model="selectedOfferingId" @change="loadSlots"><option :value="null">Выберите услугу</option><option v-for="offering in offerings" :key="offering.consultation_offering_id" :value="offering.consultation_offering_id">{{ types.find((item) => item.consultation_type_id === offering.consultation_type_id)?.title }} · {{ consultants.find((item) => item.consultant_id === offering.consultant_id)?.name }}</option></select></label>
        <p v-if="selectedType" class="muted">{{ selectedType.title }} · {{ selectedType.duration_minutes }} мин · {{ price(selectedType.price) }}</p>
        <p v-if="selectedConsultant" class="muted">Консультант: {{ selectedConsultant.name }}<span v-if="selectedConsultant.bio"> — {{ selectedConsultant.bio }}</span></p>
      </section>
      <section class="panel stack">
        <div><p class="eyebrow">02 / время</p><h2>Свободные окна</h2></div>
        <div class="inline dates"><label>С<input v-model="from" type="date" /></label><label>По<input v-model="to" type="date" /></label></div>
        <p v-if="loadingSlots" class="muted">Обновляем расписание…</p>
        <div v-else-if="!slots.length" class="muted">На выбранные даты свободных окон нет.</div>
        <div v-else class="slots"><button v-for="slot in slots" :key="slot.starts_at" :class="['slot', { selected: selectedSlot?.starts_at === slot.starts_at }]" type="button" @click="selectedSlot = slot"><strong>{{ dateTime(slot.starts_at) }}</strong><small>до {{ dateTime(slot.ends_at) }}</small></button></div>
      </section>
      <section class="panel stack">
        <div><p class="eyebrow">03 / адрес</p><h2>Место консультации</h2></div>
        <label>Адрес<select v-model="selectedAddressId"><option :value="null">Без адреса</option><option v-for="address in addresses" :key="address.client_address_id" :value="address.client_address_id">{{ address.label ? `${address.label}: ` : '' }}{{ address.address_line }}</option></select></label>
        <div class="review"><p class="eyebrow">Проверка</p><strong>{{ selectedType?.title || 'Услуга не выбрана' }}</strong><span v-if="selectedSlot">{{ dateTime(selectedSlot.starts_at) }} · {{ selectedConsultant?.name }}</span><span v-else>Время не выбрано</span></div>
        <button class="button" type="button" :disabled="submitting || !selectedOfferingId || !selectedSlot" @click="submit">{{ submitting ? 'Создаём запись…' : 'Подтвердить запись' }}</button>
      </section>
    </template>
  </main>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=DM+Sans:wght@400;500;700&family=Source+Serif+4:wght@500;600;700&display=swap');
:global(body) { margin: 0; background: #f3eedf; color: #21332b; font-family: 'DM Sans', sans-serif; }
:global(button), :global(input), :global(select) { font: inherit; min-height: 2.7rem; box-sizing: border-box; }
.reserve-view { max-width: 900px; margin: 0 auto; padding: 3rem 1rem 5rem; }.stack { display: flex; flex-direction: column; gap: 1rem; }
h1, h2 { margin: 0; font-family: 'Source Serif 4', serif; } h1 { font-size: clamp(2.3rem, 6vw, 4rem); letter-spacing: -.04em; } h2 { font-size: 1.5rem; }.lede, .muted, .zone { color: #687066; }.zone { display: block; margin-top: .8rem; font-size: .8rem; }.eyebrow { margin: 0 0 .35rem; color: #8b302f; font-size: .69rem; font-weight: 700; letter-spacing: .14em; text-transform: uppercase; }.panel { border: 1px solid #b9b8a8; background: #fbf8ef; padding: clamp(1rem, 3vw, 1.7rem); box-shadow: 5px 5px 0 rgba(33,51,43,.07); }.section-heading { display: flex; justify-content: space-between; } label { display: flex; flex-direction: column; gap: .35rem; color: #687066; font-size: .78rem; font-weight: 700; } input, select { width: 100%; border: 1px solid #b9b8a8; border-radius: 0; background: #fffdf7; color: #21332b; padding: .55rem .65rem; }.inline { display: flex; gap: .7rem; }.dates label { flex: 1; }.slots { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: .6rem; }.slot { display: flex; flex-direction: column; align-items: flex-start; gap: .3rem; border: 1px solid #b9b8a8; background: #fffdf7; color: #21332b; padding: .75rem; cursor: pointer; text-align: left; }.slot small { color: #687066; }.slot.selected { border-color: #8b302f; outline: 3px solid rgba(139,48,47,.2); }.review { display: flex; flex-direction: column; gap: .3rem; border-left: 3px solid #8b302f; padding: .75rem 1rem; background: #f5e8dc; }.button { border: 1px solid #8b302f; background: #8b302f; color: #fffaf0; padding: .65rem .9rem; cursor: pointer; font-weight: 700; }.button:disabled { cursor: not-allowed; opacity: .55; }.error, .notice { padding: .75rem 1rem; border: 1px solid; }.error { color: #8b302f; background: #f5e2dc; border-color: #c89082; }.notice { color: #2d593b; background: #e6efdf; border-color: #a2b69a; } select:focus, input:focus, button:focus-visible { outline: 3px solid rgba(139,48,47,.35); outline-offset: 2px; }
@media (max-width: 560px) { .inline { flex-direction: column; }.button { width: 100%; } }
</style>
