<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import type { UsageModelSpeedPoint, UsageReport } from '@modern/api/usage'
import { AppSvg, AppTooltip } from '@modern/components/ui'
import { dateFormatter } from '@modern/components/ui/intl-formatters'
import { formatCompactNumber } from '@modern/components/ui/format'

type SpeedBucket = Pick<UsageModelSpeedPoint, 'bucket_start_ms' | 'bucket_end_ms'>

const props = defineProps<{ report: UsageReport }>()
const { t, locale } = useI18n()
const host = ref<HTMLElement>()
const legendHost = ref<HTMLElement>()
const width = ref(720)
const legendHeight = ref(0)
const activeBucketIndex = ref<number>()
let observer: ResizeObserver | undefined
let legendObserver: ResizeObserver | undefined
const height = 240
const bottom = 204
const top = computed(() =>
  Math.min(bottom - 48, Math.max(12, legendHeight.value ? legendHeight.value + 8 : 12)),
)
const colors = [
  'var(--modern-accent)',
  'var(--modern-chart-cache)',
  'var(--modern-chart-write)',
  'var(--modern-chart-output)',
  'var(--modern-chart-input)',
]

onMounted(() => {
  observer = new ResizeObserver(([entry]) => {
    if (entry) width.value = Math.max(220, entry.contentRect.width)
  })
  if (host.value) observer.observe(host.value)
  legendObserver = new ResizeObserver(([entry]) => {
    legendHeight.value = entry?.contentRect.height ?? 0
  })
  if (legendHost.value) legendObserver.observe(legendHost.value)
})
onBeforeUnmount(() => {
  observer?.disconnect()
  legendObserver?.disconnect()
})
watch(legendHost, (current, previous) => {
  if (previous) legendObserver?.unobserve(previous)
  legendHeight.value = 0
  if (current) legendObserver?.observe(current)
})
watch(
  () => props.report,
  () => (activeBucketIndex.value = undefined),
)

function rate(point: UsageModelSpeedPoint): number {
  return (point.output_tokens / point.duration_ms) * 1000
}

function formatted(value: number): string {
  return new Intl.NumberFormat(locale.value, {
    maximumFractionDigits: value < 10 ? 1 : 0,
  }).format(value)
}

function niceCeiling(value: number): number {
  if (!Number.isFinite(value) || value <= 0) return 4
  const magnitude = 10 ** Math.floor(Math.log10(value))
  const normalized = value / magnitude
  const step = [1, 2, 5, 10].find((candidate) => candidate >= normalized) ?? 10
  return Math.max(4, step * magnitude)
}

const ceiling = computed(() =>
  niceCeiling(Math.max(0, ...props.report.model_speed.flatMap((item) => item.points.map(rate)))),
)
const left = computed(() =>
  Math.max(
    48,
    Math.min(
      108,
      Math.max(
        ...Array.from({ length: 5 }, (_, index) => formatted((ceiling.value * index) / 4).length),
      ) *
        7 +
        14,
    ),
  ),
)
const right = computed(() => width.value - 8)
const x = (point: SpeedBucket) =>
  left.value +
  ((right.value - left.value) *
    ((point.bucket_start_ms + point.bucket_end_ms) / 2 - props.report.from_ms)) /
    (props.report.to_ms - props.report.from_ms)
const y = (value: number) => bottom - ((bottom - top.value) * value) / ceiling.value

const geometry = computed(() =>
  props.report.model_speed.map((item, seriesIndex) => {
    const points = item.points.map((point) => ({
      source: point,
      value: rate(point),
      x: x(point),
      y: y(rate(point)),
    }))
    const segments: string[] = []
    let path = ''
    points.forEach((point, index) => {
      const previous = points[index - 1]
      if (!previous || previous.source.bucket_end_ms !== point.source.bucket_start_ms) {
        if (path) segments.push(path)
        path = `M${point.x},${point.y}`
      } else {
        path += ` L${point.x},${point.y}`
      }
    })
    if (path) segments.push(path)
    const outputTokens = item.points.reduce((total, point) => total + point.output_tokens, 0)
    const durationMS = item.points.reduce((total, point) => total + point.duration_ms, 0)
    return {
      ...item,
      color: colors[seriesIndex]!,
      points,
      segments,
      average: (outputTokens / durationMS) * 1000,
    }
  }),
)
const buckets = computed(() =>
  Array.from(
    {
      length: Math.ceil(
        (props.report.to_ms -
          Math.floor(props.report.from_ms / props.report.bucket_width_ms) *
            props.report.bucket_width_ms) /
          props.report.bucket_width_ms,
      ),
    },
    (_, index): SpeedBucket => {
      const alignedStart =
        Math.floor(props.report.from_ms / props.report.bucket_width_ms) *
          props.report.bucket_width_ms +
        index * props.report.bucket_width_ms
      return {
        bucket_start_ms: Math.max(props.report.from_ms, alignedStart),
        bucket_end_ms: Math.min(props.report.to_ms, alignedStart + props.report.bucket_width_ms),
      }
    },
  ),
)
const activeBucket = computed(() =>
  activeBucketIndex.value === undefined ? undefined : buckets.value[activeBucketIndex.value],
)
const activeRows = computed(() => {
  const bucket = activeBucket.value
  if (!bucket) return []
  return geometry.value.flatMap((item) => {
    const point = item.points.find(
      (candidate) => candidate.source.bucket_start_ms === bucket.bucket_start_ms,
    )
    return point ? [{ ...point, model: item.model, color: item.color }] : []
  })
})
const tooltip = computed(() => {
  const bucket = activeBucket.value ?? buckets.value[0]
  if (!bucket) return ''
  const rows = geometry.value.flatMap((item) => {
    const point = item.points.find(
      (candidate) => candidate.source.bucket_start_ms === bucket.bucket_start_ms,
    )
    return point ? [{ ...point, model: item.model }] : []
  })
  return [
    range(bucket),
    ...(rows.length
      ? rows.map(
          (row) =>
            `${row.model} ${formatted(row.value)} TPS · ${t('usage.speedRequests', {
              count: formatCompactNumber(row.source.request_count, locale.value),
            })}`,
        )
      : [t('usage.speedEmpty')]),
  ].join('\n')
})
const ticks = computed(() =>
  Array.from({ length: 5 }, (_, index) => ({
    value: (ceiling.value * index) / 4,
    y: y((ceiling.value * index) / 4),
  })),
)
const timeTicks = computed(() => {
  const count = width.value < 450 ? 3 : 5
  return Array.from({ length: count }, (_, index) => {
    const fraction = index / (count - 1)
    return {
      time: props.report.from_ms + fraction * (props.report.to_ms - props.report.from_ms),
      x: left.value + fraction * (right.value - left.value),
    }
  })
})

function clock(value: number): string {
  return dateFormatter(
    locale.value,
    props.report.to_ms - props.report.from_ms > 2 * 86_400_000
      ? { month: 'short', day: 'numeric' }
      : { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' },
  ).format(value)
}

function range(point: SpeedBucket): string {
  const day = dateFormatter(locale.value, { month: 'short', day: 'numeric' })
  const time = dateFormatter(locale.value, {
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
  })
  const endDay =
    day.format(point.bucket_start_ms) === day.format(point.bucket_end_ms)
      ? ''
      : `${day.format(point.bucket_end_ms)} `
  return `${day.format(point.bucket_start_ms)} ${time.format(point.bucket_start_ms)} – ${endDay}${time.format(point.bucket_end_ms)}`
}

function move(event: PointerEvent): void {
  const bounds = (event.currentTarget as HTMLElement).getBoundingClientRect()
  if (!bounds.width || !buckets.value.length) return
  const eventX = ((event.clientX - bounds.left) / bounds.width) * width.value
  activeBucketIndex.value = buckets.value.reduce(
    (nearest, bucket, index) =>
      Math.abs(x(bucket) - eventX) < Math.abs(x(buckets.value[nearest]!) - eventX)
        ? index
        : nearest,
    0,
  )
}

function focus(): void {
  activeBucketIndex.value ??= 0
}

function navigate(event: KeyboardEvent): void {
  if (event.altKey || event.ctrlKey || event.metaKey || !buckets.value.length) return
  if (event.key === 'Escape') {
    activeBucketIndex.value = undefined
    return
  }
  let next = activeBucketIndex.value ?? 0
  if (event.key === 'ArrowLeft') next--
  else if (event.key === 'ArrowRight') next++
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = buckets.value.length - 1
  else return
  event.preventDefault()
  activeBucketIndex.value = Math.max(0, Math.min(buckets.value.length - 1, next))
}
</script>

<template>
  <div
    ref="host"
    class="modern-speed-chart"
    role="group"
    :aria-label="t('usage.speedChartKeyboard')"
  >
    <div v-if="geometry.length" ref="legendHost" class="modern-speed-chart-legend">
      <span v-for="item in geometry" :key="item.model" class="modern-speed-chart-legend-item">
        <i :style="{ background: item.color }" aria-hidden="true" />
        <AppTooltip :label="item.model" side="top">
          <span>{{ item.model }}</span>
        </AppTooltip>
        <strong>{{ formatted(item.average) }} TPS</strong>
      </span>
    </div>
    <div class="modern-speed-chart-plot">
      <AppSvg :viewBox="`0 0 ${width} ${height}`" aria-hidden="true" focusable="false">
        <g v-for="tick in ticks" :key="tick.value" class="modern-speed-chart-gridline">
          <line :x1="left" :x2="right" :y1="tick.y" :y2="tick.y" />
          <text :x="left - 10" :y="tick.y + 4" text-anchor="end">
            {{ formatted(tick.value) }}
          </text>
        </g>
        <text :x="left" :y="top + 4" class="modern-speed-chart-unit">TPS</text>
        <g v-for="item in geometry" :key="item.model" :style="{ color: item.color }">
          <path
            v-for="(segment, index) in item.segments"
            :key="index"
            :d="segment"
            class="modern-speed-chart-line"
          />
          <circle
            v-for="point in item.points"
            :key="point.source.bucket_start_ms"
            :cx="point.x"
            :cy="point.y"
            r="2.5"
            class="modern-speed-chart-dot"
          />
        </g>
        <g v-if="activeBucket">
          <line
            :x1="x(activeBucket)"
            :x2="x(activeBucket)"
            :y1="top"
            :y2="bottom"
            class="modern-speed-chart-crosshair"
          />
          <circle
            v-for="row in activeRows"
            :key="row.model"
            :cx="row.x"
            :cy="row.y"
            r="4"
            class="modern-speed-chart-selected-dot"
            :style="{ color: row.color }"
          />
        </g>
        <text
          v-for="(tick, index) in timeTicks"
          :key="tick.time"
          :x="tick.x"
          :y="height - 10"
          :text-anchor="index === 0 ? 'start' : index === timeTicks.length - 1 ? 'end' : 'middle'"
          class="modern-speed-chart-axis"
        >
          {{ clock(tick.time) }}
        </text>
      </AppSvg>
      <span v-if="!geometry.length" class="modern-speed-chart-empty">
        {{ t('usage.speedEmpty') }}
      </span>
      <AppTooltip v-if="buckets.length" :label="tooltip" side="top">
        <span
          class="modern-speed-chart-hit"
          role="img"
          :aria-label="tooltip"
          tabindex="0"
          :style="{
            left: (left / width) * 100 + '%',
            width: ((right - left) / width) * 100 + '%',
            top: (top / height) * 100 + '%',
            height: ((bottom - top) / height) * 100 + '%',
          }"
          @pointermove="move"
          @pointerleave="activeBucketIndex = undefined"
          @focus="focus"
          @blur="activeBucketIndex = undefined"
          @keydown="navigate"
        />
      </AppTooltip>
    </div>
  </div>
</template>

<style scoped>
.modern-speed-chart {
  position: relative;
  min-width: 0;
  height: 240px;
}
.modern-speed-chart-legend {
  position: absolute;
  z-index: var(--modern-layer-raised);
  top: 0;
  left: 0;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--modern-space-1) var(--modern-space-5);
  max-width: 100%;
  color: var(--modern-muted);
  font-size: var(--modern-font-size-small);
}
.modern-speed-chart-legend-item {
  display: inline-flex;
  min-width: 0;
  align-items: center;
  gap: var(--modern-space-1-5);
}
.modern-speed-chart-legend-item i {
  width: var(--modern-space-2);
  height: var(--modern-space-2);
  flex: none;
  border-radius: var(--modern-radius-round);
}
.modern-speed-chart-legend-item span {
  overflow: hidden;
  max-width: 180px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.modern-speed-chart-legend-item strong {
  color: var(--modern-text);
  font-weight: var(--modern-weight-medium);
  font-variant-numeric: tabular-nums;
}
.modern-speed-chart-plot {
  position: relative;
  min-width: 0;
}
.modern-speed-chart svg {
  display: block;
  width: 100%;
  height: 240px;
  overflow: visible;
}
.modern-speed-chart-gridline line {
  stroke: color-mix(in srgb, var(--modern-border) 75%, var(--modern-surface));
  stroke-width: var(--modern-line-width);
  stroke-dasharray: 2 5;
}
.modern-speed-chart-gridline text,
.modern-speed-chart-axis,
.modern-speed-chart-unit {
  fill: var(--modern-muted);
  font-size: var(--modern-font-size-small);
  font-variant-numeric: tabular-nums;
}
.modern-speed-chart-line {
  fill: none;
  stroke: currentColor;
  stroke-width: var(--modern-trend-stroke);
  stroke-linecap: round;
  stroke-linejoin: round;
}
.modern-speed-chart-dot,
.modern-speed-chart-selected-dot {
  fill: currentColor;
  stroke: var(--modern-surface);
  stroke-width: var(--modern-line-width);
}
.modern-speed-chart-crosshair {
  stroke: var(--modern-tooltip-border);
  stroke-width: var(--modern-line-width);
  stroke-dasharray: 3 4;
}
.modern-speed-chart-empty {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  pointer-events: none;
  color: var(--modern-muted);
  font-size: var(--modern-font-size-secondary);
}
.modern-speed-chart-hit {
  position: absolute;
  cursor: crosshair;
  outline: none;
}
.modern-speed-chart-hit:focus-visible {
  outline: var(--modern-line-width) dashed var(--modern-tooltip-border);
}
</style>
