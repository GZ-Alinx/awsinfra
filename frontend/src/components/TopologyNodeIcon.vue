<template>
  <span class="topology-icon" :class="`family-${family}`" :style="{ '--icon-color': color }" :title="title">
    <svg v-if="family === 'domain'" viewBox="0 0 32 32" aria-hidden="true">
      <circle cx="16" cy="16" r="11" /><path d="M5 16h22M16 5c4 4 4 18 0 22M16 5c-4 4-4 18 0 22" />
    </svg>
    <svg v-else-if="family === 'gateway'" viewBox="0 0 32 32" aria-hidden="true">
      <path d="M16 4 27 9v7c0 7-4.6 10.4-11 12-6.4-1.6-11-5-11-12V9l11-5Z" /><path d="m10 16 4 4 8-9" />
    </svg>
    <svg v-else-if="family === 'service' || family === 'registry'" viewBox="0 0 32 32" aria-hidden="true">
      <circle cx="7" cy="16" r="3" /><circle cx="25" cy="8" r="3" /><circle cx="25" cy="24" r="3" /><path d="m10 15 12-6M10 17l12 6" />
    </svg>
    <svg v-else-if="family === 'database'" viewBox="0 0 32 32" aria-hidden="true">
      <ellipse cx="16" cy="7" rx="10" ry="4" /><path d="M6 7v9c0 2.2 4.5 4 10 4s10-1.8 10-4V7M6 16v9c0 2.2 4.5 4 10 4s10-1.8 10-4v-9" />
    </svg>
    <svg v-else-if="family === 'cache'" viewBox="0 0 32 32" aria-hidden="true">
      <path d="M18 3 8 17h7l-1 12 10-15h-7l1-11Z" />
    </svg>
    <svg v-else-if="family === 'queue'" viewBox="0 0 32 32" aria-hidden="true">
      <path d="M5 9h18M5 16h22M5 23h15" /><path d="m22 20 5 3-5 3" />
    </svg>
    <svg v-else-if="family === 'storage'" viewBox="0 0 32 32" aria-hidden="true">
      <path d="M8 11h16l3 5v10H5V16l3-5Z" /><path d="M10 11V6h12v5M12 20h8" />
    </svg>
    <svg v-else-if="family === 'observability'" viewBox="0 0 32 32" aria-hidden="true">
      <path d="M4 17h5l3-8 5 15 4-10 3 3h4" />
    </svg>
    <svg v-else-if="family === 'delivery'" viewBox="0 0 32 32" aria-hidden="true">
      <circle cx="8" cy="7" r="3" /><circle cx="24" cy="16" r="3" /><circle cx="8" cy="25" r="3" /><path d="M8 10v12M11 8c7 0 5 8 10 8" />
    </svg>
    <svg v-else viewBox="0 0 32 32" aria-hidden="true">
      <rect x="5" y="6" width="9" height="8" rx="2" /><rect x="18" y="6" width="9" height="8" rx="2" /><rect x="11.5" y="19" width="9" height="8" rx="2" /><path d="M9.5 14v2h13v-2M16 16v3" />
    </svg>
    <b>{{ badge }}</b>
  </span>
</template>

<script setup lang="ts">
import type { TopologyIconFamily } from '@/services/topologyPresentation';

defineProps<{
  family: TopologyIconFamily;
  badge: string;
  title: string;
  color: string;
}>();
</script>

<style scoped>
.topology-icon {
  width: 38px;
  height: 38px;
  display: grid;
  place-items: center;
  position: relative;
  color: var(--icon-color);
  border: 1px solid color-mix(in srgb, var(--icon-color) 24%, #dce5ee);
  border-radius: 10px;
  background: color-mix(in srgb, var(--icon-color) 8%, #fff);
  box-shadow: inset 0 1px 0 rgb(255 255 255 / 75%);
}
.topology-icon svg {
  width: 23px;
  height: 23px;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.8;
  stroke-linecap: round;
  stroke-linejoin: round;
}
.topology-icon b {
  min-width: 18px;
  height: 11px;
  padding: 0 3px;
  display: grid;
  place-items: center;
  position: absolute;
  right: -5px;
  bottom: -4px;
  color: #fff;
  border: 2px solid #fff;
  border-radius: 5px;
  background: var(--icon-color);
  font-size: 6px;
  font-weight: 800;
  line-height: 1;
  letter-spacing: -.02em;
}
.family-cache svg { fill: color-mix(in srgb, var(--icon-color) 15%, transparent); }
</style>
