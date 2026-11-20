<script setup lang="ts">
import { computed, ref, watch } from 'vue'

const props = defineProps<{ name: string; src?: string | null }>()
const failed = ref(false)
watch(() => props.src, () => { failed.value = false })
const initials = computed(() => props.name.trim().split(/\s+/u).slice(0, 2)
  .map(word => [...word][0] ?? '').join('').toUpperCase())
</script>

<template>
  <span class="avatar" aria-hidden="true">
    <img v-if="src && !failed" :src="src" alt="" loading="lazy" @error="failed = true" />
    <span v-else>{{ initials }}</span>
  </span>
</template>

<style scoped lang="scss">
.avatar {
  display: inline-grid;
  place-items: center;
  inline-size: var(--avatar-size, 2.5rem);
  aspect-ratio: 1;
  flex: none;
  overflow: hidden;
  border: 1px solid var(--rule);
  background: var(--green);
  color: var(--paper);
  font-family: Georgia, serif;
}
.avatar img { width: 100%; height: 100%; object-fit: cover; }
</style>
