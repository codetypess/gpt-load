interface CarpoolQuota {
  scope: string
  unit: string
  used?: number
  utilization?: number
  usedCostUSD?: number
}

export function presentCarpoolQuota(window: CarpoolQuota, locale: string) {
  if (window.unit !== 'percent' || !['personal', 'vehicle'].includes(window.scope)) {
    return undefined
  }
  const used =
    window.used ?? (window.utilization === undefined ? undefined : window.utilization * 100)
  if (used === undefined) return undefined
  const percentLabel = `${new Intl.NumberFormat(locale, { maximumFractionDigits: 2 }).format(used)}%`
  const cost =
    window.usedCostUSD === undefined
      ? undefined
      : `$${new Intl.NumberFormat(locale, {
          minimumFractionDigits: 2,
          maximumFractionDigits: 2,
        }).format(window.usedCostUSD)}`
  return {
    label: cost === undefined ? percentLabel : `${cost} · ${percentLabel}`,
    percentLabel,
    progress: Math.max(0, Math.min(100, used)),
  }
}
