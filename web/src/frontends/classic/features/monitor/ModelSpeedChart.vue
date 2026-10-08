<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import type { UsageModelSpeedPointDto, UsageModelSpeedSeriesDto } from '@/app/resources/usage'
import { formatInteger, formatLocalTimeRange } from '@/lib/format'

const props = defineProps<{
  series: readonly UsageModelSpeedSeriesDto[]
  rangeStart: number
  rangeEnd: number
  locale: string
}>()

const { t } = useI18n()
const descriptionID = `model-speed-description-${useId()}`
const chartElement = ref<HTMLElement>()
const activeBucketIndex = ref<number | null>(null)
const width = 1000
const height = 210
const left = 70
const right = 990
const top = 12
const bottom = 174
const colors = [
  'var(--color-action)',
  'var(--color-success)',
  'var(--color-warning)',
  'var(--color-danger)',
  'var(--color-chart-violet)',
]

function rate(point: UsageModelSpeedPointDto): number {
  return (point.output_tokens / point.duration_ms) * 1000
}

function formatRate(value: number): string {
  return new Intl.NumberFormat(props.locale, {
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
  niceCeiling(Math.max(0, ...props.series.flatMap((item) => item.points.map(rate)))),
)
const x = (point: UsageModelSpeedPointDto) =>
  left +
  ((right - left) * ((point.bucket_start_ms + point.bucket_end_ms) / 2 - props.rangeStart)) /
    (props.rangeEnd - props.rangeStart)
const y = (value: number) => bottom - ((bottom - top) * value) / ceiling.value

const geometry = computed(() =>
  props.series.map((item, seriesIndex) => {
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
  [
    ...new Map(
      props.series
        .flatMap((item) => item.points)
        .map((point) => [point.bucket_start_ms, point] as const),
    ).values(),
  ].sort((a, b) => a.bucket_start_ms - b.bucket_start_ms),
)
const activeBucket = computed(() => {
  const index = activeBucketIndex.value
  return index === null ? undefined : buckets.value[index]
})
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
const ticks = computed(() =>
  Array.from({ length: 5 }, (_, index) => ({
    value: (ceiling.value * index) / 4,
    y: y((ceiling.value * index) / 4),
  })),
)
const timeTicks = computed(() =>
  Array.from({ length: 5 }, (_, index) => ({
    time: props.rangeStart + ((props.rangeEnd - props.rangeStart) * index) / 4,
    x: left + ((right - left) * index) / 4,
  })),
)
const chartKey = computed(() =>
  props.series
    .map((item) =>
      [item.model, ...item.points.map((point) => `${point.bucket_start_ms}:${rate(point)}`)].join(
        ':',
      ),
    )
    .join('|'),
)

watch(chartKey, () => (activeBucketIndex.value = null))

function formatTickTime(value: number): string {
  return new Intl.DateTimeFormat(props.locale, {
    ...(props.rangeEnd - props.rangeStart > 2 * 86_400_000
      ? { month: 'short', day: 'numeric' }
      : { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }),
  }).format(value)
}

function selectNearest(event: PointerEvent | MouseEvent): void {
  const element = chartElement.value
  if (!element || buckets.value.length === 0) return
  const bounds = element.getBoundingClientRect()
  if (bounds.width <= 0) return
  const eventX = ((event.clientX - bounds.left) / bounds.width) * width
  activeBucketIndex.value = buckets.value.reduce(
    (nearest, bucket, index) =>
      Math.abs(x(bucket) - eventX) < Math.abs(x(buckets.value[nearest]!) - eventX)
        ? index
        : nearest,
    0,
  )
}

function onKeydown(event: KeyboardEvent): void {
  if (event.altKey || event.ctrlKey || event.metaKey || buckets.value.length === 0) return
  if (event.key === 'Escape') {
    activeBucketIndex.value = null
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
  <figure
    ref="chartElement"
    class="model-speed-chart"
    tabindex="0"
    role="group"
    :aria-label="t('monitor.usage.speed.title')"
    :aria-describedby="descriptionID"
    @blur="activeBucketIndex = null"
    @keydown="onKeydown"
    @pointerleave="activeBucketIndex = null"
    @pointermove="selectNearest"
  >
    <span :id="descriptionID" class="model-speed-chart__visually-hidden">
      {{ t('monitor.usage.speed.accessibleDescription') }}
    </span>
    <div v-if="geometry.length" class="model-speed-chart__legend">
      <span v-for="item in geometry" :key="item.model" class="model-speed-chart__legend-item">
        <i :style="{ background: item.color }" aria-hidden="true"></i>
        <span :title="item.model">{{ item.model }}</span>
        <strong>{{ formatRate(item.average) }} TPS</strong>
      </span>
    </div>
    <div class="model-speed-chart__plot">
      <svg :viewBox="`0 0 ${width} ${height}`" aria-hidden="true">
        <g v-for="tick in ticks" :key="tick.value" class="model-speed-chart__grid">
          <line :x1="left" :x2="right" :y1="tick.y" :y2="tick.y" />
          <text :x="left - 12" :y="tick.y + 4" text-anchor="end">
            {{ formatRate(tick.value) }}
          </text>
        </g>
        <text :x="left" :y="top + 4" class="model-speed-chart__unit">TPS</text>
        <g v-for="item in geometry" :key="item.model" :style="{ color: item.color }">
          <path
            v-for="(segment, index) in item.segments"
            :key="index"
            :d="segment"
            class="model-speed-chart__line"
          />
          <circle
            v-for="point in item.points"
            :key="point.source.bucket_start_ms"
            :cx="point.x"
            :cy="point.y"
            r="3"
            class="model-speed-chart__dot"
          />
        </g>
        <g v-if="activeBucket">
          <line
            class="model-speed-chart__guide"
            :x1="x(activeBucket)"
            :x2="x(activeBucket)"
            :y1="top"
            :y2="bottom"
          />
          <circle
            v-for="row in activeRows"
            :key="row.model"
            :cx="row.x"
            :cy="row.y"
            r="5"
            class="model-speed-chart__active-dot"
            :style="{ color: row.color }"
          />
        </g>
        <text
          v-for="(tick, index) in timeTicks"
          :key="tick.time"
          :x="tick.x"
          :y="height - 8"
          :text-anchor="index === 0 ? 'start' : index === timeTicks.length - 1 ? 'end' : 'middle'"
          class="model-speed-chart__axis"
        >
          {{ formatTickTime(tick.time) }}
        </text>
      </svg>
      <div
        v-if="activeBucket && activeRows.length"
        class="model-speed-chart__tooltip"
        :class="{
          'model-speed-chart__tooltip--start': x(activeBucket) < width * 0.2,
          'model-speed-chart__tooltip--end': x(activeBucket) > width * 0.8,
        }"
        :style="{ left: `${(x(activeBucket) / width) * 100}%` }"
        role="tooltip"
      >
        <time>{{
          formatLocalTimeRange(activeBucket.bucket_start_ms, activeBucket.bucket_end_ms, locale)
        }}</time>
        <span v-for="row in activeRows" :key="row.model" class="model-speed-chart__tooltip-row">
          <span><i :style="{ background: row.color }"></i>{{ row.model }}</span>
          <strong>{{ formatRate(row.value) }} TPS</strong>
          <small>
            {{
              t('monitor.usage.speed.requests', {
                count: formatInteger(row.source.request_count, locale),
              })
            }}
          </small>
        </span>
      </div>
      <span v-if="!geometry.length" class="model-speed-chart__empty">
        {{ t('monitor.usage.speed.empty') }}
      </span>
    </div>
  </figure>
</template>

<style scoped>
.model-speed-chart {
  display: grid;
  min-width: 0;
  margin: 0;
  outline: none;
}
.model-speed-chart:focus-visible {
  outline: 1px dashed var(--color-border-strong);
  outline-offset: 4px;
}
.model-speed-chart__visually-hidden {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
}
.model-speed-chart__legend {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 20px;
  margin-bottom: 10px;
  color: var(--color-text-muted);
  font-size: var(--text-xs);
}
.model-speed-chart__legend-item {
  display: inline-flex;
  min-width: 0;
  align-items: center;
  gap: 6px;
}
.model-speed-chart__legend-item i,
.model-speed-chart__tooltip-row i {
  width: 8px;
  height: 8px;
  flex: none;
  border-radius: 50%;
}
.model-speed-chart__legend-item span {
  overflow: hidden;
  max-width: 210px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.model-speed-chart__legend-item strong {
  color: var(--color-text);
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
}
.model-speed-chart__plot {
  position: relative;
  min-height: 210px;
  cursor: crosshair;
}
.model-speed-chart svg {
  display: block;
  width: 100%;
  min-height: 180px;
}
.model-speed-chart__grid line {
  stroke: var(--color-border-subtle);
  stroke-dasharray: var(--chart-grid-dash);
}
.model-speed-chart__grid text,
.model-speed-chart__axis,
.model-speed-chart__unit {
  fill: var(--color-text-faint);
  font-family: var(--font-mono);
  font-size: 11px;
}
.model-speed-chart__line {
  fill: none;
  stroke: currentColor;
  stroke-linecap: round;
  stroke-linejoin: round;
  stroke-width: var(--chart-line-width);
}
.model-speed-chart__dot,
.model-speed-chart__active-dot {
  fill: currentColor;
  stroke: var(--color-surface-raised);
  stroke-width: 2;
}
.model-speed-chart__guide {
  stroke: var(--color-border-strong);
  stroke-dasharray: 3 4;
}
.model-speed-chart__tooltip {
  position: absolute;
  z-index: 2;
  top: 16px;
  min-width: 230px;
  transform: translateX(-50%);
  border: 1px solid var(--color-border-strong);
  border-radius: 8px;
  background: var(--color-surface-raised);
  box-shadow: var(--shadow-chart-tooltip);
  padding: 9px 11px;
  pointer-events: none;
}
.model-speed-chart__tooltip--start {
  transform: none;
}
.model-speed-chart__tooltip--end {
  transform: translateX(-100%);
}
.model-speed-chart__tooltip time {
  display: block;
  margin-bottom: 6px;
  color: var(--color-text-faint);
  font-family: var(--font-mono);
  font-size: 11px;
}
.model-speed-chart__tooltip-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 1px 14px;
  align-items: center;
  font-size: 12px;
}
.model-speed-chart__tooltip-row + .model-speed-chart__tooltip-row {
  margin-top: 5px;
}
.model-speed-chart__tooltip-row > span {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 6px;
  overflow: hidden;
  color: var(--color-text-muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.model-speed-chart__tooltip-row strong {
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
}
.model-speed-chart__tooltip-row small {
  grid-column: 1 / -1;
  padding-left: 14px;
  color: var(--color-text-faint);
}
.model-speed-chart__empty {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  color: var(--color-text-faint);
  font-size: var(--text-sm);
}
</style>
