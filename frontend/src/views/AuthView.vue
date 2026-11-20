<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { api, ApiError, unwrap } from "../api/client";
import { useAccount } from "../stores/account";

const route = useRoute();
const router = useRouter();
const account = useAccount();
const mode = computed(() => route.params.mode);
const step = ref(false);
const busy = ref(false);
const error = ref("");
const notice = ref("");
const email = ref("");
const password = ref("");
const newPassword = ref("");
const name = ref("");
const phone = ref("");
const code = ref("");
const codeField = ref<HTMLInputElement>();
const errorField = ref<HTMLElement>();
const resendAt = ref(0);
const expiresAt = ref(0);
const now = ref(Date.now());
const clock = window.setInterval(() => {
  now.value = Date.now();
}, 1000);
onUnmounted(() => window.clearInterval(clock));
const wait = computed(() =>
  Math.max(0, Math.ceil((resendAt.value - now.value) / 1000)),
);
const expired = computed(
  () => expiresAt.value > 0 && expiresAt.value <= now.value,
);
const heading = computed(() =>
  mode.value === "register"
    ? "Регистрация"
    : mode.value === "reset"
      ? "Восстановление доступа"
      : "Вход в кабинет",
);
const purpose = computed(() =>
  mode.value === "reset"
    ? ("reset_password" as const)
    : ("verify_email" as const),
);

function validateNewPassword(value: string) {
  const length = [...value].length;
  if (length < 12 || length > 128) throw new Error("Пароль должен содержать от 12 до 128 символов.");
}

async function issue(resend = false) {
  if (mode.value === "register" && !resend) validateNewPassword(password.value);
  const result =
    mode.value === "login"
      ? await api.POST("/api/v1/auth/login", {
          body: { email: email.value, password: password.value },
        })
      : mode.value === "register" && !resend
        ? await api.POST("/api/v1/auth/register", {
            body: {
              name: name.value,
              phone: phone.value,
              email: email.value,
              password: password.value,
            },
          })
        : await api.POST("/api/v1/auth/codes", {
            body: { email: email.value, purpose: purpose.value },
          });
  unwrap<unknown>(result);
  if (mode.value !== "login") password.value = "";
  expiresAt.value =
    Date.parse(result.response.headers.get("OTP-Expires-At") ?? "") || 0;
  resendAt.value =
    Date.parse(result.response.headers.get("OTP-Resend-At") ?? "") || 0;
  now.value = Date.now();
  code.value = "";
  step.value = true;
  notice.value =
    mode.value === "reset"
      ? "Если аккаунт существует, письмо с кодом отправлено."
      : "Проверьте почту: мы отправили одноразовый код.";
  await nextTick();
  codeField.value?.focus();
}

async function verify() {
  if (mode.value === "reset") validateNewPassword(newPassword.value);
  const body = { email: email.value, code: code.value };
  if (mode.value === "login") {
    unwrap(await api.POST("/api/v1/auth/login/verify", { body }));
    password.value = "";
    await account.refresh();
    const target = route.query.next;
    await router.replace(
      typeof target === "string" && /^\/(?![\\/])/.test(target)
        ? target
        : router.hasRoute("bookings")
          ? "/bookings"
          : "/",
    );
  } else {
    if (mode.value === "register")
      unwrap(await api.POST("/api/v1/auth/verify-email", { body }));
    else
      unwrap(
        await api.POST("/api/v1/auth/reset-password", {
          body: { ...body, password: newPassword.value },
        }),
      );
    await router.replace({
      path: "/login",
      query: { ready: mode.value === "register" ? "verified" : "reset" },
    });
  }
}

async function run(action: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    await action();
  } catch (e) {
    if (e instanceof ApiError && e.retryAfter > 0) {
      now.value = Date.now();
      resendAt.value = Math.max(
        resendAt.value,
        now.value + e.retryAfter * 1000,
      );
    }
    if (
      e instanceof ApiError &&
      e.code === "mail_unavailable" &&
      mode.value === "register" &&
      !step.value
    )
      step.value = true;
    error.value =
      e instanceof Error ? e.message : "Не удалось выполнить запрос.";
    await nextTick();
    errorField.value?.focus();
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <section class="auth panel stack" aria-labelledby="auth-heading">
    <h1 id="auth-heading">{{ heading }}</h1>
    <p v-if="mode === 'login' && route.query.ready" role="status">
      {{
        route.query.ready === "verified"
          ? "Почта подтверждена. Теперь можно войти."
          : "Пароль обновлён. Войдите с новым паролем."
      }}
    </p>
    <p v-if="error" ref="errorField" class="error" role="alert" tabindex="-1">
      {{ error }}
    </p>
    <form v-if="!step" class="stack" @submit.prevent="run(() => issue())">
      <label v-if="mode === 'register'"
        >Имя<input
          v-model.trim="name"
          required
          maxlength="200"
          autocomplete="name"
      /></label>
      <label
        >Электронная почта<input
          v-model.trim="email"
          type="email"
          required
          maxlength="254"
          autocomplete="email"
      /></label>
      <label v-if="mode === 'register'"
        >Телефон<input
          v-model.trim="phone"
          type="tel"
          required
          maxlength="40"
          autocomplete="tel"
      /></label>
      <label v-if="mode !== 'reset'"
        >Пароль<input
          v-model="password"
          type="password"
          required
          :autocomplete="
            mode === 'register' ? 'new-password' : 'current-password'
          "
      /></label>
      <div class="inline">
        <button :disabled="busy || wait > 0">
          {{ mode === "login" ? "Продолжить" : "Получить код" }}</button
        ><span v-if="wait">Повтор через {{ wait }} с</span>
      </div>
    </form>
    <form v-else class="stack" @submit.prevent="run(verify)">
      <p role="status">
        {{ notice }} <strong>{{ email }}</strong>
      </p>
      <p v-if="expired">Срок действия кода истёк. Запросите новый.</p>
      <label
        >Код из письма<input
          ref="codeField"
          v-model="code"
          required
          maxlength="6"
          pattern="[0-9]{6}"
          inputmode="numeric"
          autocomplete="one-time-code"
      /></label>
      <label v-if="mode === 'reset'"
        >Новый пароль<input
          v-model="newPassword"
          type="password"
          required
          autocomplete="new-password"
      /></label>
      <div class="inline">
        <button :disabled="busy || expired">
          {{ mode === "reset" ? "Сменить пароль" : "Подтвердить код" }}</button
        ><button
          type="button"
          :disabled="busy || wait > 0"
          @click="run(() => issue(true))"
        >
          {{ wait ? `Повтор через ${wait} с` : "Отправить код снова" }}
        </button>
      </div>
      <button
        type="button"
        :disabled="busy"
        @click="
          step = false;
          error = '';
          code = '';
          resendAt = 0;
        "
      >
        Изменить данные
      </button>
    </form>
    <nav class="inline" aria-label="Доступ к кабинету">
      <RouterLink v-if="mode !== 'login'" to="/login">Войти</RouterLink>
      <RouterLink v-if="mode !== 'register'" to="/register"
        >Регистрация</RouterLink
      >
      <RouterLink v-if="mode === 'login'" to="/reset"
        >Не помню пароль</RouterLink
      >
    </nav>
  </section>
</template>

<style scoped lang="scss">
.auth {
  max-width: 35rem;
  margin-inline: auto;
}
.auth nav {
  padding-top: 1rem;
  border-top: 1px solid var(--rule);
}
</style>
