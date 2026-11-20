<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { RouterLink } from "vue-router";
import type { components } from "../api/schema";
import { api, unwrap } from "../api/client";
import { price, dateTime, zone, statusLabel } from "../display";
import { useAccount } from "../stores/account";

type Booking = components["schemas"]["Booking"];

const account = useAccount();
const profile = computed(() => account.profile);
const bookings = ref<Booking[]>([]);
const loading = ref(true);
const error = ref("");
const offset = ref(0);
const limit = 10;
const range = reactive({ from: monthStart(-1), to: monthStart(1) });

const roleLabel = computed(() =>
  profile.value?.role === "consultant"
    ? "Назначенные консультации"
    : profile.value?.role === "admin"
      ? "Все записи"
      : "Мои записи",
);
const canGoNext = computed(() => bookings.value.length === limit);

function monthStart(delta: number) {
  const date = new Date();
  date.setMonth(date.getMonth() + delta, delta > 0 ? 1 : 1);
  date.setHours(0, 0, 0, 0);
  return localDate(date);
}

function localDate(date: Date) {
  const offsetMinutes = date.getTimezoneOffset() * 60000;
  return new Date(date.getTime() - offsetMinutes).toISOString().slice(0, 10);
}

function showError(reason: unknown) {
  error.value =
    reason instanceof Error ? reason.message : "Не удалось загрузить записи.";
}

async function load() {
  loading.value = true;
  error.value = "";
  try {
    bookings.value =
      (await unwrap(
        await api.GET("/api/v1/bookings", {
          params: {
            query: {
              from: new Date(`${range.from}T00:00:00`).toISOString(),
              to: new Date(`${range.to}T23:59:59`).toISOString(),
              limit,
              offset: offset.value,
            },
          },
        }),
      )) ?? [];
  } catch (reason) {
    showError(reason);
  } finally {
    loading.value = false;
  }
}

function applyRange() {
  offset.value = 0;
  load();
}
function page(delta: number) {
  offset.value = Math.max(0, offset.value + delta * limit);
  load();
}

onMounted(load);
</script>

<template>
  <div class="bookings-page stack">
    <header class="page-header inline">
      <div>
        <p class="eyebrow">Barone Sanitation / журнал</p>
        <h1>{{ roleLabel }}</h1>
        <p class="lede">Записи, сроки и текущие решения в одном списке.</p>
      </div>
      <span class="zone">Часовой пояс: {{ zone }}</span>
    </header>
    <section class="panel stack filters">
      <div class="inline filter-heading">
        <div>
          <p class="eyebrow">Период</p>
          <h2>Диапазон записей</h2>
        </div>
        <button
          class="button button-secondary"
          type="button"
          :disabled="loading"
          @click="load"
        >
          Обновить
        </button>
      </div>
      <form class="inline" @submit.prevent="applyRange">
        <label>С даты<input v-model="range.from" type="date" required /></label
        ><label>По дату<input v-model="range.to" type="date" required /></label
        ><button class="button" type="submit" :disabled="loading">
          Показать
        </button>
      </form>
    </section>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="loading" class="panel muted">Загрузка журнала…</p>
    <section v-else class="panel booking-list" aria-live="polite">
      <div v-if="!bookings.length" class="empty">
        За выбранный период записей нет.
      </div>
      <RouterLink
        v-for="booking in bookings"
        :key="booking.consultation_booking_id"
        class="booking-row"
        :to="{
          name: 'booking',
          params: { id: booking.consultation_booking_id },
        }"
      >
        <div class="booking-main">
          <strong>{{ booking.consultation_title }}</strong
          ><span class="status">{{ statusLabel(booking.status) }}</span
          ><small
            >{{ dateTime(booking.starts_at) }}–{{
              dateTime(booking.ends_at)
            }}</small
          >
        </div>
        <div class="booking-meta">
          <span>{{
            profile?.role === "consultant"
              ? booking.client_name
              : booking.consultant_name
          }}</span
          ><strong>{{ price(booking.price) }}</strong>
        </div>
      </RouterLink>
      <nav
        v-if="bookings.length || offset"
        class="pagination inline"
        aria-label="Пагинация"
      >
        <button
          class="button button-secondary"
          type="button"
          :disabled="loading || offset === 0"
          @click="page(-1)"
        >
          ← Назад</button
        ><span>Страница {{ offset / limit + 1 }}</span
        ><button
          class="button button-secondary"
          type="button"
          :disabled="loading || !canGoNext"
          @click="page(1)"
        >
          Вперёд →
        </button>
      </nav>
    </section>
  </div>
</template>

<style scoped lang="scss">
.bookings-page {
  max-width: 1100px;
  margin: auto;
  padding: 3rem clamp(1rem, 4vw, 4rem) 5rem;
}
h1,
h2 {
  margin: 0;
  font-family: "Source Serif 4", serif;
}
h1 {
  font-size: clamp(2.4rem, 5vw, 4rem);
  letter-spacing: -0.04em;
}
h2 {
  font-size: 1.4rem;
}
.lede {
  margin: 0.4rem 0 0;
  color: #687066;
}
.eyebrow {
  margin: 0 0 0.35rem;
  color: #8b302f;
  font-size: 0.69rem;
  font-weight: 700;
  letter-spacing: 0.14em;
  text-transform: uppercase;
}
.page-header {
  align-items: end;
  justify-content: space-between;
  border-bottom: 3px solid #21332b;
  padding-bottom: 1.5rem;
}
.zone,
.muted {
  color: #687066;
  font-size: 0.8rem;
}
.stack {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
.inline {
  display: flex;
  align-items: center;
  gap: 0.7rem;
}
.panel {
  border: 1px solid #b9b8a8;
  background: #fbf8ef;
  padding: clamp(1rem, 2.2vw, 1.7rem);
  box-shadow: 5px 5px 0 rgba(33, 51, 43, 0.07);
}
.filter-heading {
  justify-content: space-between;
}
.filters form {
  align-items: end;
}
.filters label {
  flex: 1;
}
label {
  display: flex;
  flex-direction: column;
  gap: 0.35rem;
  color: #687066;
  font-size: 0.78rem;
  font-weight: 700;
}
input {
  width: 100%;
  border: 1px solid #b9b8a8;
  border-radius: 0;
  background: #fffdf7;
  padding: 0.55rem 0.65rem;
}
.button {
  border: 1px solid #8b302f;
  background: #8b302f;
  color: #fffaf0;
  padding: 0.6rem 0.9rem;
  cursor: pointer;
  font-weight: 700;
}
.button-secondary {
  border-color: #21332b;
  background: transparent;
  color: #21332b;
}
button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}
button:focus-visible,
input:focus-visible {
  outline: 3px solid rgba(139, 48, 47, 0.35);
  outline-offset: 2px;
}
.booking-row {
  display: flex;
  justify-content: space-between;
  gap: 1rem;
  border-top: 1px solid #d4d0c0;
  padding: 1rem 0;
  color: inherit;
  text-decoration: none;
}
.booking-row:hover {
  background: #f3eedf;
}
.booking-main,
.booking-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.55rem;
}
.booking-main small {
  width: 100%;
  color: #687066;
}
.booking-meta {
  justify-content: end;
  color: #687066;
  font-size: 0.85rem;
}
.booking-meta strong {
  color: #21332b;
}
.status {
  padding: 0.2rem 0.45rem;
  background: #e9d8d2;
  color: #863d37;
  font-size: 0.68rem;
  font-weight: 700;
  text-transform: uppercase;
}
.pagination {
  justify-content: space-between;
  border-top: 1px solid #d4d0c0;
  padding-top: 1rem;
}
.empty {
  color: #687066;
  padding: 1rem 0;
}
.error {
  border: 1px solid #c89082;
  background: #f5e2dc;
  color: #8b302f;
  padding: 0.75rem 1rem;
}
@media (max-width: 620px) {
  .page-header,
  .filters form,
  .booking-row {
    align-items: stretch;
    flex-direction: column;
  }
  .page-header .zone {
    align-self: flex-start;
  }
  .filters .button,
  .filters form .button {
    width: 100%;
  }
  .booking-meta {
    justify-content: start;
  }
  .pagination {
    flex-direction: column;
  }
  .pagination .button {
    width: 100%;
  }
}
</style>
