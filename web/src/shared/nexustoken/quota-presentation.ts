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
  const money = new Intl.NumberFormat(locale, { maximumFractionDigits: 2 })
  let cost: string | undefined
  if (window.usedCostUSD !== undefined) {
    cost = `$${money.format(window.usedCostUSD)}`
    if (window.usedCostUSD > 0 && used > 0) {
      const estimatedLimit = window.usedCostUSD / (used / 100)
      if (Number.isFinite(estimatedLimit)) cost += `/$${money.format(estimatedLimit)}`
    }
  }
  return {
    label: cost === undefined ? percentLabel : `${cost} · ${percentLabel}`,
    percentLabel,
    progress: Math.max(0, Math.min(100, used)),
  }
}
