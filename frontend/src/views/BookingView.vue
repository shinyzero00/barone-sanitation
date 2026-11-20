<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { RouterLink, useRoute } from "vue-router";
import type { components } from "../api/schema";
import { ApiError, api, unwrap } from "../api/client";
import { price, dateTime, zone, statusLabel } from "../display";
import { useAccount } from "../stores/account";
import PersonAvatar from "../components/PersonAvatar.vue";

type Booking = components["schemas"]["Booking"];
type Event = components["schemas"]["BookingEvent"];
type Result = components["schemas"]["ConsultationResult"];
type Status = components["schemas"]["StatusChange"]["status"];

const route = useRoute();
const account = useAccount();
const booking = ref<Booking | null>(null);
const events = ref<Event[]>([]);
const result = ref<Result | null>(null);
const resultText = ref("");
const loading = ref(true);
const saving = ref(false);
const error = ref("");
const notice = ref("");
const transition = reactive<{ status: Status; reason: string }>({
  status: "confirmed",
  reason: "",
});
const id = computed(() => Number(route.params.id));
const role = computed(() => account.profile?.role ?? "");
const canResult = computed(
  () =>
    role.value === "consultant" &&
    booking.value?.status === "completed" &&
    !result.value,
);
const transitions = computed((): Status[] => {
  if (!booking.value) return [];
  const status = booking.value.status;
  if (role.value === "admin")
    return status === "pending"
      ? ["confirmed", "cancelled"]
      : status === "confirmed"
        ? ["completed", "no_show", "cancelled"]
        : [];
  return role.value === "client" &&
    (status === "pending" || status === "confirmed")
    ? ["cancelled"]
    : [];
});

function showError(reason: unknown) {
  error.value =
    reason instanceof Error ? reason.message : "Операция не выполнена.";
  notice.value = "";
}
async function load() {
  loading.value = true;
  error.value = "";
  try {
    booking.value = await unwrap(
      await api.GET("/api/v1/bookings/{id}", {
        params: { path: { id: id.value } },
      }),
    );
    const [history, savedResult] = await Promise.all([
      api.GET("/api/v1/bookings/{id}/events", {
        params: { path: { id: id.value } },
      }),
      api.GET("/api/v1/bookings/{id}/result", {
        params: { path: { id: id.value } },
      }),
    ]);
    events.value = (await unwrap(history)) ?? [];
    try {
      result.value = await unwrap(savedResult);
    } catch (reason) {
      if (!(reason instanceof ApiError) || reason.status !== 404) throw reason;
    }
    resultText.value = result.value?.consultation_result ?? "";
    if (transitions.value[0]) transition.status = transitions.value[0];
  } catch (reason) {
    showError(reason);
  } finally {
    loading.value = false;
  }
}
async function changeStatus() {
  if (!booking.value) return;
  saving.value = true;
  error.value = "";
  notice.value = "";
  try {
    booking.value = await unwrap(
      await api.POST("/api/v1/bookings/{id}/status", {
        params: { path: { id: id.value } },
        body: { status: transition.status, reason: transition.reason || null },
      }),
    );
    notice.value = "Статус записи обновлён.";
    await load();
  } catch (reason) {
    showError(reason);
  } finally {
    saving.value = false;
  }
}
async function saveResult() {
  if (!booking.value || !resultText.value.trim()) return;
  saving.value = true;
  error.value = "";
  notice.value = "";
  try {
    result.value = await unwrap(
      await api.POST("/api/v1/bookings/{id}/result", {
        params: { path: { id: id.value } },
        body: { consultation_result: resultText.value },
      }),
    );
    notice.value = "Заключение сохранено.";
  } catch (reason) {
    if (
      reason instanceof ApiError &&
      reason.code === "consultation_result_exists"
    )
      await load();
    showError(reason);
  } finally {
    saving.value = false;
  }
}
onMounted(load);
</script>

<template>
  <div class="booking-page stack">
    <header class="page-header inline">
      <div>
        <p class="eyebrow">Barone Sanitation / запись №{{ id }}</p>
        <h1>Детали консультации</h1>
        <p class="lede">История, условия и результат встречи.</p>
      </div>
      <span class="zone">Часовой пояс: {{ zone }}</span>
    </header>
    <RouterLink class="back-link" :to="{ name: 'bookings' }"
      >← Вернуться к списку</RouterLink
    >
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <p v-if="loading" class="panel muted">Загрузка записи…</p>
    <template v-else-if="booking">
      <section class="detail-grid">
        <article class="panel stack">
          <div class="section-title inline">
            <div>
              <p class="eyebrow">Условия</p>
              <h2>{{ booking.consultation_title }}</h2>
            </div>
            <span class="status">{{ statusLabel(booking.status) }}</span>
          </div>
          <dl class="facts">
            <div>
              <dt>Дата и время</dt>
              <dd>
                {{ dateTime(booking.starts_at) }}–{{
                  dateTime(booking.ends_at)
                }}
              </dd>
            </div>
            <div>
              <dt>Консультант</dt>
              <dd>{{ booking.consultant_name }}</dd>
            </div>
            <div>
              <dt>Клиент</dt>
              <dd class="inline"><PersonAvatar v-if="booking.client_avatar_url" :name="booking.client_name" :src="booking.client_avatar_url" />{{ booking.client_name }}</dd>
            </div>
            <div>
              <dt>Стоимость</dt>
              <dd>
                {{ price(booking.price) }} · {{ booking.duration_minutes }} мин
              </dd>
            </div>
            <div>
              <dt>Адрес</dt>
              <dd>{{ booking.address_line || "Адрес не выбран" }}</dd>
            </div>
          </dl>
        </article>
        <article class="panel stack">
          <div>
            <p class="eyebrow">Действия</p>
            <h2>Управление записью</h2>
          </div>
          <form
            v-if="transitions.length"
            class="stack"
            @submit.prevent="changeStatus"
          >
            <label
              >Новый статус<select v-model="transition.status">
                <option v-for="item in transitions" :key="item" :value="item">
                  {{ statusLabel(item) }}
                </option>
              </select></label
            ><label
              >Причина, если нужна<textarea
                v-model="transition.reason"
                rows="2"
                placeholder="Короткое пояснение"
              /></label
            ><button class="button" type="submit" :disabled="saving">
              Сохранить статус
            </button>
          </form>
          <p v-else class="muted">
            Для этой записи сейчас нет доступных переходов.
          </p>
        </article>
      </section>
      <section class="panel stack">
        <div>
          <p class="eyebrow">Хронология</p>
          <h2>История статусов</h2>
        </div>
        <div v-if="!events.length" class="empty">Событий пока нет.</div>
        <div
          v-for="event in events"
          :key="event.consultation_booking_status_event_id"
          class="event-row"
        >
          <strong>{{ statusLabel(event.status) }}</strong
          ><span>{{ dateTime(event.occurred_at) }}</span
          ><small v-if="event.reason">{{ event.reason }}</small>
        </div>
      </section>
      <section class="panel stack">
        <div>
          <p class="eyebrow">Результат</p>
          <h2>Заключение консультации</h2>
        </div>
        <p v-if="result" class="result-text">
          {{ result.consultation_result }}
        </p>
        <form v-else-if="canResult" class="stack" @submit.prevent="saveResult">
          <label
            >Текст заключения<textarea
              v-model="resultText"
              rows="7"
              required
              placeholder="Опишите результат консультации"
            /></label
          ><button
            class="button"
            type="submit"
            :disabled="saving || !resultText.trim()"
          >
            Сохранить заключение
          </button>
        </form>
        <p v-else class="muted">
          Заключение появится после завершения консультации.
        </p>
      </section>
    </template>
  </div>
</template>

<style scoped lang="scss">
.booking-page {
  max-width: 1100px;
  margin: auto;
  padding: 3rem clamp(1rem, 4vw, 4rem) 5rem;
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
  color: var(--muted);
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
  color: var(--muted);
  font-size: 0.8rem;
}
.back-link {
  color: #8b302f;
  font-weight: 700;
}
.panel {
  border: 1px solid #b9b8a8;
  background: #fbf8ef;
  padding: clamp(1rem, 2.2vw, 1.7rem);
  box-shadow: 5px 5px 0 rgba(33, 51, 43, 0.07);
}
.detail-grid {
  display: grid;
  grid-template-columns: 1.2fr 0.8fr;
  gap: 1rem;
}
.section-title {
  align-items: start;
  justify-content: space-between;
}
.status {
  padding: 0.25rem 0.5rem;
  background: #e9d8d2;
  color: #863d37;
  font-size: 0.68rem;
  font-weight: 700;
  text-transform: uppercase;
}
.facts {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 1rem;
  margin: 0;
}
.facts div {
  border-top: 1px solid #d4d0c0;
  padding-top: 0.65rem;
}
.facts dt {
  color: #687066;
  font-size: 0.76rem;
  font-weight: 700;
}
.facts dd {
  margin: 0.3rem 0 0;
}
.event-row {
  display: grid;
  grid-template-columns: 8rem 1fr;
  gap: 0.5rem;
  border-top: 1px solid #d4d0c0;
  padding: 0.75rem 0;
}
.event-row span,
.event-row small {
  color: #687066;
}
.event-row small {
  grid-column: 2;
}
.result-text {
  white-space: pre-wrap;
  line-height: 1.65;
}
label {
  display: flex;
  flex-direction: column;
  gap: 0.35rem;
  color: #687066;
  font-size: 0.78rem;
  font-weight: 700;
}
input,
textarea,
select {
  width: 100%;
  border: 1px solid #b9b8a8;
  border-radius: 0;
  background: #fffdf7;
  color: #21332b;
  padding: 0.55rem 0.65rem;
  resize: vertical;
}
button:focus-visible,
input:focus-visible,
textarea:focus-visible,
select:focus-visible {
  outline: 3px solid rgba(139, 48, 47, 0.35);
  outline-offset: 2px;
}
.button {
  border: 1px solid #8b302f;
  background: #8b302f;
  color: #fffaf0;
  padding: 0.6rem 0.9rem;
  cursor: pointer;
  font-weight: 700;
}
button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}
.empty {
  color: #687066;
  padding: 1rem 0;
}
.error,
.notice {
  border: 1px solid;
  padding: 0.75rem 1rem;
}
.error {
  border-color: #c89082;
  background: #f5e2dc;
  color: #8b302f;
}
.notice {
  border-color: #a2b69a;
  background: #e6efdf;
  color: #2d593b;
}
@media (max-width: 720px) {
  .page-header {
    align-items: stretch;
    flex-direction: column;
  }
  .page-header .zone {
    align-self: flex-start;
  }
  .detail-grid,
  .facts {
    grid-template-columns: 1fr;
  }
}
</style>
