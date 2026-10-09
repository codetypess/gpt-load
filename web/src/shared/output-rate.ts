type OutputTiming = {
  protocol: string
  status_code: number
  duration_ms: number
  first_response_ms?: number | null
  output_tokens: string
  usage_state: string
}

// 两套前端共用请求输出速度。首响可用时只计算首响后的生成窗口；
// 非流式请求没有首响采样，回退到总耗时。
export function outputTokensPerSecond(row: OutputTiming): number | null {
  const tokens = Number(row.output_tokens)
  if (
    row.status_code < 200 ||
    row.status_code >= 300 ||
    !['openai-completions', 'openai-responses', 'anthropic', 'gemini'].includes(row.protocol) ||
    (row.usage_state !== 'complete' && row.usage_state !== 'partial') ||
    !Number.isSafeInteger(tokens) ||
    tokens <= 0
  ) {
    return null
  }
  if (!Number.isSafeInteger(row.duration_ms) || row.duration_ms <= 0) return null

  const firstResponseMs = row.first_response_ms
  if (firstResponseMs !== null && firstResponseMs !== undefined) {
    if (
      !Number.isSafeInteger(firstResponseMs) ||
      firstResponseMs < 0 ||
      firstResponseMs >= row.duration_ms
    ) {
      return null
    }
    const generationMs = row.duration_ms - firstResponseMs
    return tokens / (generationMs / 1000)
  }

  return tokens / (row.duration_ms / 1000)
}
