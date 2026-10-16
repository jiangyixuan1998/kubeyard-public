<template>
  <svg :width="size" :height="size" viewBox="0 0 24 24" aria-hidden="true">
    <defs><linearGradient id="ky-logo-g" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0" stop-color="#5B7CFF" /><stop offset="1" stop-color="#3E5CE8" />
    </linearGradient></defs>
    <rect width="24" height="24" rx="6" fill="url(#ky-logo-g)" />
    <!-- 船锚：锚环 + 锚杆 + 横杆 + 锚爪 -->
    <g fill="none" stroke="#fff" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
      <circle cx="12" cy="5.2" r="1.9" />
      <line x1="12" y1="7.1" x2="12" y2="18.6" />
      <line x1="8.4" y1="9.6" x2="15.6" y2="9.6" />
      <path d="M5.6 13.2 C5.6 17.6 9 19.6 12 19.6 C15 19.6 18.4 17.6 18.4 13.2" />
      <path d="M4.2 15.4 L5.6 13.2 L7.4 14.4" />
      <path d="M19.8 15.4 L18.4 13.2 L16.6 14.4" />
    </g>
  </svg>
</template>
<script setup lang="ts">
withDefaults(defineProps<{ size?: number }>(), { size: 28 })
</script>
