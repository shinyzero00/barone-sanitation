<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import type { components } from "../api/schema";
import { api, unwrap } from "../api/client";
import { useAccount } from "../stores/account";

type Profile = components["schemas"]["Profile"];
type Address = components["schemas"]["Address"];
type AssignedOffering = components["schemas"]["AssignedOffering"];

const account = useAccount();
const form = reactive({ name: "", phone: "", bio: "" });
const addresses = ref<Address[]>([]);
const assignments = ref<AssignedOffering[]>([]);
const addressDraft = reactive({ address_line: "", label: "" });
const editingAddressId = ref<number | null>(null);
const loading = ref(true);
const saving = ref(false);
const error = ref("");
const notice = ref("");

function showError(reason: unknown) {
  error.value =
    reason instanceof Error
      ? reason.message
      : "Операция не выполнена. Попробуйте ещё раз.";
  notice.value = "";
}
function syncForm(profile: Profile | null | undefined) {
  if (profile)
    Object.assign(form, {
      name: profile.name,
      phone: profile.phone ?? "",
      bio: profile.bio ?? "",
    });
}

async function load() {
  loading.value = true;
  try {
    await account.refresh();
    syncForm(account.profile);
    const addressResponse =
      account.profile?.role === "client"
        ? await api.GET("/api/v1/addresses")
        : null;
    addresses.value = addressResponse
      ? ((await unwrap(addressResponse)) ?? [])
      : [];
    assignments.value =
      account.profile?.role === "consultant"
        ? ((await unwrap(await api.GET("/api/v1/me/offerings"))) ?? [])
        : [];
  } catch (reason) {
    showError(reason);
  } finally {
    loading.value = false;
  }
}

async function saveProfile() {
  saving.value = true;
  error.value = "";
  try {
    account.profile = await unwrap(
      await api.PATCH("/api/v1/me", {
        body: {
          name: form.name,
          phone: form.phone || null,
          bio: form.bio || null,
        },
      }),
    );
    syncForm(account.profile);
    notice.value = "Профиль сохранён.";
  } catch (reason) {
    showError(reason);
  } finally {
    saving.value = false;
  }
}

function editAddress(address: Address) {
  editingAddressId.value = address.client_address_id;
  Object.assign(addressDraft, {
    address_line: address.address_line,
    label: address.label ?? "",
  });
}
function clearAddress() {
  editingAddressId.value = null;
  Object.assign(addressDraft, { address_line: "", label: "" });
}
async function saveAddress() {
  saving.value = true;
  error.value = "";
  try {
    const body = {
      address_line: addressDraft.address_line,
      label: addressDraft.label || null,
    };
    const saved =
      editingAddressId.value === null
        ? await unwrap(await api.POST("/api/v1/addresses", { body }))
        : await unwrap(
            await api.PUT("/api/v1/addresses/{id}", {
              params: { path: { id: editingAddressId.value } },
              body,
            }),
          );
    addresses.value =
      editingAddressId.value === null
        ? [...addresses.value, saved]
        : addresses.value.map((item) =>
            item.client_address_id === editingAddressId.value ? saved : item,
          );
    clearAddress();
    notice.value = "Адрес сохранён.";
  } catch (reason) {
    showError(reason);
  } finally {
    saving.value = false;
  }
}
async function deleteAddress(address: Address) {
  if (!window.confirm("Удалить этот адрес?")) return;
  saving.value = true;
  error.value = "";
  try {
    await unwrap(
      await api.DELETE("/api/v1/addresses/{id}", {
        params: { path: { id: address.client_address_id } },
      }),
    );
    addresses.value = addresses.value.filter(
      (item) => item.client_address_id !== address.client_address_id,
    );
    notice.value = "Адрес удалён.";
  } catch (reason) {
    showError(reason);
  } finally {
    saving.value = false;
  }
}

onMounted(load);
</script>

<template>
  <div class="profile-view stack">
    <header>
      <p class="eyebrow">Barone Sanitation / аккаунт</p>
      <h1>Мой профиль</h1>
      <p class="lede">Контактные данные, адреса и назначения консультанта.</p>
    </header>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <p v-if="loading" class="panel">Загрузка профиля…</p>
    <template v-else
      ><section class="panel stack">
        <div>
          <p class="eyebrow">01 / данные</p>
          <h2>Личная информация</h2>
        </div>
        <form class="stack" @submit.prevent="saveProfile">
          <label>Электронная почта<input :value="account.profile?.email" type="email" readonly /></label>
          <label>Имя<input v-model="form.name" required /></label
          ><label>Телефон<input v-model="form.phone" type="tel" /></label
          ><label v-if="account.profile?.role === 'consultant'"
            >Биография<textarea
              v-model="form.bio"
              rows="4"
              placeholder="Расскажите о специализации"
            /></label
          ><button class="button" type="submit" :disabled="saving">
            Сохранить профиль
          </button>
        </form>
      </section>
      <section v-if="account.profile?.role === 'client'" class="panel stack">
        <div>
          <p class="eyebrow">02 / адреса</p>
          <h2>Мои адреса</h2>
        </div>
        <form class="inline address-form" @submit.prevent="saveAddress">
          <label class="grow"
            >Адрес<input
              v-model="addressDraft.address_line"
              required
              placeholder="Улица, дом, квартира" /></label
          ><label
            >Метка<input
              v-model="addressDraft.label"
              placeholder="Дом" /></label
          ><button class="button" type="submit" :disabled="saving">
            {{ editingAddressId === null ? "Добавить" : "Сохранить" }}</button
          ><button
            v-if="editingAddressId !== null"
            class="button secondary"
            type="button"
            @click="clearAddress"
          >
            Отмена
          </button>
        </form>
        <div v-if="!addresses.length" class="muted">Адресов пока нет.</div>
        <div
          v-for="address in addresses"
          :key="address.client_address_id"
          class="address-row inline"
        >
          <span
            ><strong v-if="address.label">{{ address.label }}: </strong
            >{{ address.address_line }}</span
          ><span class="inline"
            ><button
              class="text-button"
              type="button"
              @click="editAddress(address)"
            >
              Изменить</button
            ><button
              class="text-button danger"
              type="button"
              :disabled="saving"
              @click="deleteAddress(address)"
            >
              Удалить
            </button></span
          >
        </div>
      </section>
      <section
        v-if="account.profile?.role === 'consultant'"
        class="panel stack"
      >
        <div>
          <p class="eyebrow">02 / назначения</p>
          <h2>Мои услуги</h2>
        </div>
        <div v-if="!assignments.length" class="muted">
          Назначенных услуг пока нет.
        </div>
        <div
          v-for="item in assignments"
          :key="item.consultation_offering_id"
          class="assignment"
        >
          <strong>{{ item.title }}</strong
          ><span
            >{{
              item.is_available ? "Доступна для записи" : "Сейчас недоступна"
            }}
            · {{ item.is_active ? "активна" : "неактивна" }}</span
          >
        </div>
      </section></template
    >
  </div>
</template>

<style scoped lang="scss">
.profile-view {
  max-width: 900px;
  margin: 0 auto;
  padding: 3rem 1rem 5rem;
}
.stack {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
h1,
h2 {
  margin: 0;
  font-family: "Source Serif 4", serif;
}
h1 {
  font-size: clamp(2.3rem, 6vw, 4rem);
  letter-spacing: -0.04em;
}
h2 {
  font-size: 1.5rem;
}
.lede,
.muted,
.assignment span {
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
.panel {
  border: 1px solid #b9b8a8;
  background: #fbf8ef;
  padding: clamp(1rem, 3vw, 1.7rem);
  box-shadow: 5px 5px 0 rgba(33, 51, 43, 0.07);
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
textarea {
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
.inline {
  display: flex;
  align-items: end;
  gap: 0.7rem;
}
.grow {
  flex: 1;
}
.address-form label {
  flex: 0 1 210px;
}
.address-form .grow {
  flex: 1;
}
.button {
  border: 1px solid #8b302f;
  background: #8b302f;
  color: #fffaf0;
  padding: 0.65rem 0.9rem;
  cursor: pointer;
  font-weight: 700;
}
.secondary {
  border-color: #21332b;
  background: transparent;
  color: #21332b;
}
.button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}
.address-row {
  justify-content: space-between;
  border-top: 1px solid #d4d0c0;
  padding-top: 0.8rem;
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
.assignment {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  border-top: 1px solid #d4d0c0;
  padding-top: 0.8rem;
}
.error,
.notice {
  padding: 0.75rem 1rem;
  border: 1px solid;
}
.error {
  color: #8b302f;
  background: #f5e2dc;
  border-color: #c89082;
}
.notice {
  color: #2d593b;
  background: #e6efdf;
  border-color: #a2b69a;
}
input:focus,
textarea:focus,
button:focus-visible {
  outline: 3px solid rgba(139, 48, 47, 0.35);
  outline-offset: 2px;
}
@media (max-width: 600px) {
  .inline,
  .address-form {
    align-items: stretch;
    flex-direction: column;
  }
  .address-form label,
  .button {
    width: 100%;
  }
  .address-row {
    align-items: flex-start;
  }
  .address-row .inline {
    flex-direction: row;
  }
}
</style>
