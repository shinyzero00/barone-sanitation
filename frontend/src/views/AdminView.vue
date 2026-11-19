<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import type { components } from "../api/schema";
import { api, unwrap } from "../api/client";
import { price as formatMoney, zone } from "../display";

type Consultant = components["schemas"]["Consultant"];
type ConsultationType = components["schemas"]["ConsultationType"];
type Offering = components["schemas"]["Offering"];
type WorkInterval = components["schemas"]["WorkInterval"];

const consultants = ref<Consultant[]>([]);
const types = ref<ConsultationType[]>([]);
const offerings = ref<Offering[]>([]);
const intervals = ref<WorkInterval[]>([]);
const selectedConsultantId = ref<number | null>(null);
const loading = ref(true);
const saving = ref(false);
const error = ref("");
const notice = ref("");

const newType = reactive({
  title: "",
  description: "",
  duration_minutes: 30,
  price: "",
});
const typeDraft = reactive({
  title: "",
  description: "",
  duration_minutes: 30,
  price: "",
});
const editingTypeId = ref<number | null>(null);
const candidateEmail = ref("");
const candidate = ref<{ name: string; user_id: number } | null>(null);
const consultantBio = reactive<Record<number, string>>({});
const intervalDraft = reactive({ starts_at: "", ends_at: "" });
const editingIntervalId = ref<number | null>(null);

const selectedConsultant = computed(() =>
  consultants.value.find(
    (item) => item.consultant_id === selectedConsultantId.value,
  ),
);

function showError(reason: unknown) {
  error.value =
    reason instanceof Error
      ? reason.message
      : "Операция не выполнена. Попробуйте ещё раз.";
  notice.value = "";
}

function clearMessage() {
  error.value = "";
  notice.value = "";
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat("ru-RU", {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}

function inputDate(value: string) {
  const date = new Date(value);
  const offset = date.getTimezoneOffset() * 60000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}

function isoDate(value: string) {
  return new Date(value).toISOString();
}

async function loadPages<T>(getPage: (query: { limit: number; offset: number }) => Promise<T[] | null>) {
  const limit = 500;
  const all: T[] = [];
  for (let offset = 0; ; offset += limit) {
    const page = (await getPage({ limit, offset })) ?? [];
    all.push(...page);
    if (page.length < limit) return all;
  }
}

async function load() {
  loading.value = true;
  clearMessage();
  try {
    const [allConsultants, allTypes, allOfferings] =
      await Promise.all([
        loadPages(async query => unwrap(await api.GET("/api/v1/admin/consultants", { params: { query } }))),
        loadPages(async query => unwrap(await api.GET("/api/v1/admin/consultation-types", { params: { query } }))),
        loadPages(async query => unwrap(await api.GET("/api/v1/admin/offerings", { params: { query } }))),
      ]);
    consultants.value = allConsultants;
    types.value = allTypes;
    offerings.value = allOfferings;
    consultants.value.forEach((item) => {
      consultantBio[item.consultant_id] = item.bio ?? "";
    });
    if (selectedConsultantId.value === null && consultants.value[0])
      selectedConsultantId.value = consultants.value[0].consultant_id;
    if (selectedConsultantId.value !== null)
      await loadIntervals(selectedConsultantId.value);
  } catch (reason) {
    showError(reason);
  } finally {
    loading.value = false;
  }
}

async function loadIntervals(consultantId: number) {
  selectedConsultantId.value = consultantId;
  try {
    intervals.value =
      (await unwrap(
        await api.GET("/api/v1/consultants/{consultant_id}/work-intervals", {
          params: { path: { consultant_id: consultantId } },
        }),
      )) ?? [];
  } catch (reason) {
    showError(reason);
  }
}

async function createType() {
  saving.value = true;
  clearMessage();
  try {
    const created = await unwrap(
      await api.POST("/api/v1/admin/consultation-types", {
        body: { ...newType },
      }),
    );
    types.value.unshift(created);
    Object.assign(newType, {
      title: "",
      description: "",
      duration_minutes: 30,
      price: "",
    });
    notice.value = "Вид консультации добавлен.";
  } catch (reason) {
    showError(reason);
  } finally {
    saving.value = false;
  }
}

function startTypeEdit(type: ConsultationType) {
  editingTypeId.value = type.consultation_type_id;
  Object.assign(typeDraft, type);
}

async function replaceType() {
  if (editingTypeId.value === null) return;
  saving.value = true;
  clearMessage();
  try {
    const replacement = await unwrap(
      await api.PUT("/api/v1/admin/consultation-types/{id}", {
        params: { path: { id: editingTypeId.value } },
        body: { ...typeDraft },
      }),
    );
    types.value = types.value.map((type) =>
      type.consultation_type_id === editingTypeId.value ? replacement : type,
    );
    editingTypeId.value = null;
    notice.value = "Новая версия сохранена.";
  } catch (reason) {
    showError(reason);
  } finally {
    saving.value = false;
  }
}

async function deactivateType(type: ConsultationType) {
  if (!window.confirm(`Деактивировать «${type.title}»?`)) return;
  saving.value = true;
  clearMessage();
  try {
    await unwrap(
      await api.DELETE("/api/v1/admin/consultation-types/{id}", {
        params: { path: { id: type.consultation_type_id } },
      }),
    );
    type.is_active = false;
    notice.value = "Вид консультации деактивирован.";
  } catch (reason) {
    showError(reason);
  } finally {
    saving.value = false;
  }
}

async function findCandidate() {
  candidate.value = null;
  clearMessage();
  try {
    candidate.value = await unwrap(
      await api.GET("/api/v1/admin/consultant-candidates", {
        params: { query: { email: candidateEmail.value } },
      }),
    );
  } catch (reason) {
    showError(reason);
  }
}

async function provisionConsultant() {
  if (!candidate.value) return;
  saving.value = true;
  clearMessage();
  try {
    const created = await unwrap(
      await api.POST("/api/v1/admin/consultants", {
        body: { user_id: candidate.value.user_id },
      }),
    );
    consultants.value.push(created);
    consultantBio[created.consultant_id] = created.bio ?? "";
    candidate.value = null;
    candidateEmail.value = "";
    notice.value = "Консультант добавлен.";
  } catch (reason) {
    showError(reason);
  } finally {
    saving.value = false;
  }
}

async function saveConsultant(consultant: Consultant) {
  saving.value = true;
  clearMessage();
  try {
    await unwrap(
      await api.PUT("/api/v1/admin/consultants/{id}/bio", {
        params: { path: { id: consultant.consultant_id } },
        body: { bio: consultantBio[consultant.consultant_id] ?? "" },
      }),
    );
    await unwrap(
      await api.PATCH("/api/v1/admin/consultants/{id}", {
        params: { path: { id: consultant.consultant_id } },
        body: { is_active: consultant.is_active },
      }),
    );
    notice.value = "Профиль консультанта обновлён.";
  } catch (reason) {
    showError(reason);
  } finally {
    saving.value = false;
  }
}

function offeringFor(consultantId: number, typeId: number) {
  return offerings.value.find(
    (item) =>
      item.consultant_id === consultantId &&
      item.consultation_type_id === typeId,
  );
}

async function toggleOffering(consultantId: number, type: ConsultationType) {
  const current = offeringFor(consultantId, type.consultation_type_id);
  saving.value = true;
  clearMessage();
  try {
    const updated = await unwrap(
      await api.PUT("/api/v1/admin/offerings", {
        body: {
          consultant_id: consultantId,
          consultation_type_id: type.consultation_type_id,
          is_active: !(current?.is_active ?? false),
        },
      }),
    );
    const index = offerings.value.findIndex(
      (item) =>
        item.consultation_offering_id === updated.consultation_offering_id,
    );
    if (index >= 0) offerings.value[index] = updated;
    else offerings.value.push(updated);
    notice.value = "Назначение обновлено.";
  } catch (reason) {
    showError(reason);
  } finally {
    saving.value = false;
  }
}

function startIntervalEdit(interval: WorkInterval) {
  editingIntervalId.value = interval.work_interval_id;
  intervalDraft.starts_at = inputDate(interval.starts_at);
  intervalDraft.ends_at = inputDate(interval.ends_at);
}

async function saveInterval() {
  if (selectedConsultantId.value === null) return;
  saving.value = true;
  clearMessage();
  try {
    const body = {
      starts_at: isoDate(intervalDraft.starts_at),
      ends_at: isoDate(intervalDraft.ends_at),
    };
    const response =
      editingIntervalId.value === null
        ? await api.POST("/api/v1/consultants/{consultant_id}/work-intervals", {
            params: { path: { consultant_id: selectedConsultantId.value } },
            body,
          })
        : await api.PUT(
            "/api/v1/consultants/{consultant_id}/work-intervals/{id}",
            {
              params: {
                path: {
                  consultant_id: selectedConsultantId.value,
                  id: editingIntervalId.value,
                },
              },
              body,
            },
          );
    const saved = await unwrap(response);
    if (editingIntervalId.value === null) intervals.value.push(saved);
    else
      intervals.value = intervals.value.map((item) =>
        item.work_interval_id === editingIntervalId.value ? saved : item,
      );
    editingIntervalId.value = null;
    Object.assign(intervalDraft, { starts_at: "", ends_at: "" });
    notice.value = "Расписание сохранено.";
  } catch (reason) {
    showError(reason);
  } finally {
    saving.value = false;
  }
}

async function deleteInterval(interval: WorkInterval) {
  if (
    selectedConsultantId.value === null ||
    !window.confirm("Удалить рабочий интервал?")
  )
    return;
  saving.value = true;
  clearMessage();
  try {
    await unwrap(
      await api.DELETE(
        "/api/v1/consultants/{consultant_id}/work-intervals/{id}",
        {
          params: {
            path: {
              consultant_id: selectedConsultantId.value,
              id: interval.work_interval_id,
            },
          },
        },
      ),
    );
    intervals.value = intervals.value.filter(
      (item) => item.work_interval_id !== interval.work_interval_id,
    );
    notice.value = "Интервал удалён.";
  } catch (reason) {
    showError(reason);
  } finally {
    saving.value = false;
  }
}

onMounted(load);
</script>

<template>
  <div class="admin-workspace stack">
    <header class="admin-header inline">
      <div>
        <p class="eyebrow">Barone Sanitation / back office</p>
        <h1>Администрация</h1>
        <p class="lede">Каталог, люди и рабочие часы в одном журнале.</p>
      </div>
      <button
        class="button button-secondary"
        type="button"
        :disabled="loading || saving"
        @click="load"
      >
        Обновить данные
      </button>
    </header>

    <p v-if="error" class="message message-error" role="alert">{{ error }}</p>
    <p v-if="notice" class="message message-success" role="status">
      {{ notice }}
    </p>
    <p v-if="loading" class="panel loading">
      Загрузка административных данных…
    </p>

    <template v-else>
      <section class="workspace-grid">
        <article class="panel catalog-panel">
          <div class="section-heading inline">
            <div>
              <p class="eyebrow">01 / каталог</p>
              <h2>Виды консультаций</h2>
            </div>
            <span class="count">{{ types.length }}</span>
          </div>
          <form class="stack form-block" @submit.prevent="createType">
            <h3>Новая версия</h3>
            <div class="field-grid">
              <label>Название<input v-model="newType.title" required /></label
              ><label
                >Цена<input
                  v-model="newType.price"
                  required
                  inputmode="decimal"
                  placeholder="150.00" /></label
              ><label
                >Длительность, мин<input
                  v-model.number="newType.duration_minutes"
                  required
                  type="number"
                  min="1"
              /></label>
            </div>
            <label
              >Описание<textarea v-model="newType.description" rows="2" />
            </label>
            <button class="button" type="submit" :disabled="saving">
              Добавить вид
            </button>
          </form>
          <div v-if="!types.length" class="empty">Каталог пока пуст.</div>
          <div
            v-for="type in types"
            :key="type.consultation_type_id"
            class="catalog-row"
          >
            <div class="row-main">
              <strong>{{ type.title }}</strong
              ><span
                :class="['status', type.is_active ? 'status-on' : 'status-off']"
                >{{ type.is_active ? "активен" : "неактивен" }}</span
              ><small
                >{{ type.duration_minutes }} мин ·
                {{ formatMoney(type.price) }}</small
              >
            </div>
            <p>{{ type.description || "Описание не задано." }}</p>
            <div class="inline row-actions">
              <button
                class="button button-small button-secondary"
                type="button"
                @click="startTypeEdit(type)"
              >
                Новая версия</button
              ><button
                v-if="type.is_active"
                class="text-button"
                type="button"
                :disabled="saving"
                @click="deactivateType(type)"
              >
                Деактивировать
              </button>
            </div>
          </div>
          <form
            v-if="editingTypeId !== null"
            class="edit-drawer stack"
            @submit.prevent="replaceType"
          >
            <h3>Редактирование версии</h3>
            <div class="field-grid">
              <label>Название<input v-model="typeDraft.title" required /></label
              ><label>Цена<input v-model="typeDraft.price" required /></label
              ><label
                >Длительность<input
                  v-model.number="typeDraft.duration_minutes"
                  required
                  type="number"
                  min="1"
              /></label>
            </div>
            <label
              >Описание<textarea v-model="typeDraft.description" rows="2" />
            </label>
            <div class="inline">
              <button class="button" type="submit" :disabled="saving">
                Сохранить версию</button
              ><button
                class="button button-secondary"
                type="button"
                @click="editingTypeId = null"
              >
                Отмена
              </button>
            </div>
          </form>
        </article>

        <article class="panel people-panel">
          <div class="section-heading inline">
            <div>
              <p class="eyebrow">02 / команда</p>
              <h2>Консультанты</h2>
            </div>
            <span class="count">{{ consultants.length }}</span>
          </div>
          <form class="inline provision-form" @submit.prevent="findCandidate">
            <label class="grow"
              >Email пользователя<input
                v-model="candidateEmail"
                required
                type="email"
                placeholder="name@example.com" /></label
            ><button
              class="button button-secondary"
              type="submit"
              :disabled="saving"
            >
              Найти
            </button>
          </form>
          <div v-if="candidate" class="candidate inline">
            <span
              ><strong>{{ candidate.name }}</strong> · ID
              {{ candidate.user_id }}</span
            ><button
              class="button button-small"
              type="button"
              :disabled="saving"
              @click="provisionConsultant"
            >
              Создать консультанта
            </button>
          </div>
          <div v-if="!consultants.length" class="empty">
            Консультантов пока нет.
          </div>
          <div
            v-for="consultant in consultants"
            :key="consultant.consultant_id"
            class="consultant-row"
          >
            <div class="inline row-title">
              <div>
                <strong>{{ consultant.name }}</strong
                ><small>{{
                  consultant.is_active ? "активен" : "неактивен"
                }}</small>
              </div>
              <label class="switch"
                ><input v-model="consultant.is_active" type="checkbox" /><span
                  >Работает</span
                ></label
              >
            </div>
            <label
              >Биография<textarea
                v-model="consultantBio[consultant.consultant_id]"
                rows="2"
                placeholder="Короткое описание специалиста"
              />
            </label>
            <button
              class="button button-small"
              type="button"
              :disabled="saving"
              @click="saveConsultant(consultant)"
            >
              Сохранить профиль
            </button>
            <div class="assignment">
              <p class="eyebrow">Специализации</p>
              <label
                v-for="type in types"
                :key="type.consultation_type_id"
                class="check-row"
                ><input
                  :checked="
                    offeringFor(
                      consultant.consultant_id,
                      type.consultation_type_id,
                    )?.is_active ?? false
                  "
                  type="checkbox"
                  @change="toggleOffering(consultant.consultant_id, type)"
                /><span>{{ type.title }}</span></label
              ><span v-if="!types.length" class="muted"
                >Сначала добавьте вид консультации.</span
              >
            </div>
          </div>
        </article>
      </section>

      <section class="panel schedule-panel">
        <div class="section-heading inline">
          <div>
            <p class="eyebrow">03 / расписание</p>
          <h2>Рабочие интервалы</h2>
          <p>Часовой пояс: {{ zone }}</p>
          </div>
          <select
            v-model="selectedConsultantId"
            aria-label="Консультант для расписания"
            @change="
              selectedConsultantId && loadIntervals(selectedConsultantId)
            "
          >
            <option :value="null" disabled>Выберите консультанта</option>
            <option
              v-for="consultant in consultants"
              :key="consultant.consultant_id"
              :value="consultant.consultant_id"
            >
              {{ consultant.name }}
            </option>
          </select>
        </div>
        <p v-if="selectedConsultant" class="muted">
          Расписание: <strong>{{ selectedConsultant.name }}</strong>. Конфликты
          с записями сообщит сервер.
        </p>
        <form
          v-if="selectedConsultantId !== null"
          class="inline interval-form"
          @submit.prevent="saveInterval"
        >
          <label
            >Начало<input
              v-model="intervalDraft.starts_at"
              required
              type="datetime-local" /></label
          ><label
            >Окончание<input
              v-model="intervalDraft.ends_at"
              required
              type="datetime-local" /></label
          ><button class="button" type="submit" :disabled="saving">
            {{
              editingIntervalId === null
                ? "Добавить интервал"
                : "Сохранить изменения"
            }}</button
          ><button
            v-if="editingIntervalId !== null"
            class="button button-secondary"
            type="button"
            @click="editingIntervalId = null"
          >
            Отмена
          </button>
        </form>
        <div
          v-if="selectedConsultantId === null || !intervals.length"
          class="empty"
        >
          {{
            selectedConsultantId === null
              ? "Выберите консультанта, чтобы увидеть расписание."
              : "Рабочих интервалов пока нет."
          }}
        </div>
        <div
          v-for="interval in intervals"
          :key="interval.work_interval_id"
          class="interval-row inline"
        >
          <div>
            <strong>{{ formatDate(interval.starts_at) }}</strong
            ><span>до {{ formatDate(interval.ends_at) }}</span>
          </div>
          <div class="inline">
            <button
              class="text-button"
              type="button"
              @click="startIntervalEdit(interval)"
            >
              Изменить</button
            ><button
              class="text-button danger"
              type="button"
              :disabled="saving"
              @click="deleteInterval(interval)"
            >
              Удалить
            </button>
          </div>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped lang="scss">
.admin-workspace {
  max-width: 1380px;
  margin: 0 auto;
  padding: 3rem clamp(1rem, 4vw, 4rem) 5rem;
}
.admin-header {
  align-items: end;
  justify-content: space-between;
  border-bottom: 3px solid #21332b;
  padding-bottom: 1.5rem;
}
h1,
h2,
h3 {
  margin: 0;
  font-family: "Source Serif 4", serif;
  font-weight: 600;
}
h1 {
  font-size: clamp(2.4rem, 5vw, 4.3rem);
  letter-spacing: -0.04em;
}
h2 {
  font-size: 1.65rem;
}
h3 {
  font-size: 1.1rem;
}
.lede {
  margin: 0.45rem 0 0;
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
.workspace-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.05fr) minmax(0, 0.95fr);
  gap: 1rem;
}
.panel {
  border: 1px solid #b9b8a8;
  background: #fbf8ef;
  padding: clamp(1rem, 2.2vw, 1.7rem);
  box-shadow: 5px 5px 0 rgba(33, 51, 43, 0.07);
}
.section-heading {
  align-items: start;
  justify-content: space-between;
  margin-bottom: 1.25rem;
}
.count {
  border: 1px solid #b9b8a8;
  padding: 0.25rem 0.55rem;
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
.form-block,
.edit-drawer {
  border-top: 1px solid #d4d0c0;
  padding-top: 1rem;
}
.field-grid {
  display: grid;
  grid-template-columns: 1fr 0.75fr 0.75fr;
  gap: 0.7rem;
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
}
textarea {
  resize: vertical;
}
input:focus,
textarea:focus,
select:focus,
button:focus-visible {
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
.button:hover:not(:disabled) {
  background: #6d2525;
}
.button-secondary {
  border-color: #21332b;
  background: transparent;
  color: #21332b;
}
.button-secondary:hover:not(:disabled) {
  background: #e5e0d0;
}
.button-small {
  min-height: 2.2rem;
  padding: 0.4rem 0.65rem;
  font-size: 0.78rem;
}
button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}
.text-button {
  border: 0;
  background: transparent;
  color: #8b302f;
  cursor: pointer;
  padding: 0.25rem 0;
  font-size: 0.78rem;
  font-weight: 700;
  text-decoration: underline;
}
.danger {
  color: #9b2f2f;
}
.catalog-row,
.consultant-row,
.interval-row {
  border-top: 1px solid #d4d0c0;
  padding: 1rem 0;
}
.catalog-row p {
  margin: 0.4rem 0 0.75rem;
  color: #687066;
  font-size: 0.88rem;
}
.row-main {
  display: flex;
  align-items: center;
  gap: 0.55rem;
  flex-wrap: wrap;
}
.row-main small,
.row-title small {
  width: 100%;
  color: #687066;
  font-size: 0.78rem;
}
.status {
  padding: 0.2rem 0.4rem;
  font-size: 0.68rem;
  font-weight: 700;
  text-transform: uppercase;
}
.status-on {
  background: #dce8d7;
  color: #2d593b;
}
.status-off {
  background: #e9d8d2;
  color: #863d37;
}
.row-actions {
  justify-content: flex-start;
}
.provision-form {
  align-items: end;
  margin-bottom: 0.8rem;
}
.grow {
  flex: 1;
}
.candidate {
  justify-content: space-between;
  border: 1px solid #a2b69a;
  background: #e9f0e4;
  padding: 0.7rem;
  margin-bottom: 0.8rem;
  font-size: 0.82rem;
}
.row-title {
  justify-content: space-between;
  align-items: start;
}
.switch {
  flex-direction: row;
  align-items: center;
  gap: 0.35rem;
}
.switch input {
  width: auto;
  min-height: auto;
}
.assignment {
  margin-top: 1rem;
  border-left: 3px solid #8b302f;
  padding-left: 0.8rem;
}
.check-row {
  flex-direction: row;
  align-items: center;
  margin: 0.45rem 0;
  font-weight: 500;
}
.check-row input {
  width: auto;
  min-height: auto;
}
.schedule-panel {
  grid-column: 1 / -1;
}
.schedule-panel select {
  max-width: 280px;
}
.interval-form {
  align-items: end;
  border-top: 1px solid #d4d0c0;
  padding-top: 1rem;
  margin-top: 1rem;
}
.interval-form label {
  flex: 1;
}
.interval-row {
  justify-content: space-between;
}
.interval-row div:first-child {
  display: flex;
  flex-direction: column;
  gap: 0.2rem;
}
.interval-row span,
.muted {
  color: #687066;
  font-size: 0.8rem;
}
.empty,
.loading {
  color: #687066;
  padding: 1.2rem 0;
  font-size: 0.88rem;
}
.message {
  padding: 0.75rem 1rem;
  border: 1px solid;
}
.message-error {
  color: #8b302f;
  background: #f5e2dc;
  border-color: #c89082;
}
.message-success {
  color: #2d593b;
  background: #e6efdf;
  border-color: #a2b69a;
}
@media (max-width: 900px) {
  .workspace-grid {
    grid-template-columns: 1fr;
  }
  .schedule-panel {
    grid-column: auto;
  }
}
@media (max-width: 600px) {
  .admin-header,
  .interval-form,
  .provision-form {
    align-items: stretch;
    flex-direction: column;
  }
  .admin-header .button,
  .interval-form .button,
  .provision-form .button {
    width: 100%;
  }
  .field-grid {
    grid-template-columns: 1fr;
  }
  .section-heading {
    align-items: flex-start;
  }
  .interval-row {
    align-items: flex-start;
    flex-direction: column;
  }
  .interval-row .inline {
    width: 100%;
    justify-content: flex-end;
  }
}
@media (prefers-reduced-motion: reduce) {
  * {
    scroll-behavior: auto !important;
  }
}
</style>
