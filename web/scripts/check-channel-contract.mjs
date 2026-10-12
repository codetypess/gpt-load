import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { createServer } from 'vite'

const server = await createServer({
  server: { middlewareMode: true, watch: null, hmr: false, ws: false },
})

try {
  const { projectChannelList } = await server.ssrLoadModule(
    '/src/frontends/classic/app/resources/channels.ts',
  )
  const { InvalidResponseError } = await server.ssrLoadModule('/src/shared/http/errors.ts')
  const { getGroupChannels } = await server.ssrLoadModule(
    '/src/frontends/modern/api/group-create.ts',
  )
  const readModern = (data) =>
    getGroupChannels({ request: async () => data }, new AbortController().signal)
  const field = (key, inputKind, required = false) => ({
    key,
    label: key,
    input_kind: inputKind,
    required,
    sensitive: inputKind === 'secret',
    default_value: null,
  })
  const channel = (id, credentialFields, quotaObservation = false) => ({
    channel_id: id,
    name: id,
    mark: id,
    icon: id,
    search_terms: [],
    description: '',
    default_base_url: 'https://example.com',
    default_base_urls: ['https://example.com'],
    notices: [],
    param_fields: [field('base_url', 'url', true)],
    credential_fields: credentialFields,
    connection: {
      type: 'api_key',
      credential_input: 'batch_text',
      authorization_methods: [],
    },
    capabilities: {
      model_discovery: true,
      quota_observation: quotaObservation,
      credential_actions: [],
      outbound_proxy: true,
    },
    routes: [],
    client_protocols: [],
  })
  const catalog = {
    items: [
      channel('newapi', [field('api_key', 'secret', true)]),
      channel(
        'nexustoken',
        [
          field('api_key', 'secret', true),
          ...['session_id', 'seat_id', 'key_id', 'email', 'plan_name'].map((key) =>
            field(key, 'text'),
          ),
        ],
        true,
      ),
    ],
    total: 2,
  }
  const result = projectChannelList(catalog)
  assert.deepEqual(
    result.items.map((item) => item.channel_id),
    ['newapi', 'nexustoken'],
  )
  assert.equal(result.items[1].capabilities.quota_observation, true)
  assert.equal(
    result.items[1].credential_fields.find((item) => item.key === 'email').sensitive,
    false,
  )
  assert.deepEqual(
    (await readModern(catalog)).map((item) => item.id),
    ['newapi', 'nexustoken'],
  )

  for (const mutate of [
    (item) => {
      item.credential_fields[0].sensitive = false
    },
    (item) => {
      item.credential_fields[1].sensitive = true
    },
    (item) => {
      item.credential_fields[0].default_value = 'secret-value'
    },
    (item) => {
      item.api_key = 'secret-value'
    },
  ]) {
    const invalid = structuredClone(catalog)
    mutate(invalid.items[1])
    assert.throws(() => projectChannelList(invalid), InvalidResponseError)
  }

  const { refreshCredentialObservation, projectCredentialItem, projectCredentialDetail } =
    await server.ssrLoadModule('/src/frontends/classic/app/resources/credentials.ts')
  const { readObservation } = await server.ssrLoadModule(
    '/src/frontends/modern/api/credential-observation.ts',
  )
  const { presentCarpoolQuota } = await server.ssrLoadModule(
    '/src/shared/nexustoken/quota-presentation.ts',
  )
  const observedAt = 1791816000000
  const observation = {
    state: 'fresh',
    observation_version: 4,
    observed_at_ms: observedAt,
    last_attempt_at_ms: observedAt,
    snapshot: {
      plan_summary: { name: 'ChatGPT Pro 200' },
      account_summary: { display_name: 'Carpool seat', email: 'driver@example.com' },
      quota_windows: [
        ['personal', 18000],
        ['vehicle', 18000],
        ['personal', 604800],
        ['vehicle', 604800],
      ].map(([scope, seconds]) => ({
        id: `${scope}-${seconds === 18000 ? '5h' : '7d'}`,
        label: `${scope} quota`,
        scope,
        unit: 'percent',
        used: 7,
        limit: 100,
        remaining: 93,
        utilization: 0.07,
        ...(seconds === 604800
          ? { used_cost_usd: scope === 'personal' ? 68.8 : 256.44 }
          : scope === 'personal'
            ? { used_cost_usd: 0 }
            : {}),
        window_seconds: seconds,
        ...(seconds === 604800 ? { reset_at_ms: observedAt + 604800000 } : {}),
        state: 'available',
        ...(scope === 'personal' && seconds === 604800 ? { is_primary: true } : {}),
      })),
      credits: { balance: '0', has_credits: false, unlimited: false },
      reset_credits_available: 0,
    },
  }
  const refreshed = await refreshCredentialObservation({ request: async () => observation }, 4, 4)
  assert.equal(refreshed.snapshot.quota_windows.length, 4)
  assert.equal(refreshed.snapshot.credits.balance, '0')
  assert.equal(refreshed.snapshot.reset_credits_available, 0)
  const modernObservation = readObservation(observation)
  assert.equal(modernObservation.windows.length, 4)
  assert.equal(modernObservation.credits.balance, '0')
  assert.equal(modernObservation.resetCredits, 0)
  assert.deepEqual(
    refreshed.snapshot.quota_windows.map((window) => window.used_cost_usd),
    [0, undefined, 68.8, 256.44],
  )
  assert.deepEqual(
    modernObservation.windows.map((window) => window.usedCostUSD),
    [0, undefined, 68.8, 256.44],
  )
  for (const locale of ['zh-CN', 'en-US', 'ja-JP']) {
    const classicRows = refreshed.snapshot.quota_windows.map((window) =>
      presentCarpoolQuota({ ...window, usedCostUSD: window.used_cost_usd }, locale),
    )
    const modernRows = modernObservation.windows.map((window) =>
      presentCarpoolQuota(window, locale),
    )
    assert.deepEqual(classicRows, modernRows)
    assert.deepEqual(
      classicRows.map((row) => row.label),
      ['$0.00 · 7%', '7%', '$68.80 · 7%', '$256.44 · 7%'],
    )
    assert(classicRows.every((row) => row.progress === 7))
  }
  const overLimit = presentCarpoolQuota(
    { scope: 'personal', unit: 'percent', used: 137.5, utilization: 1, usedCostUSD: 1001 },
    'zh-CN',
  )
  assert.equal(overLimit.label, '$1,001.00 · 137.5%')
  assert.equal(overLimit.progress, 100)
  assert.equal(
    presentCarpoolQuota({ scope: 'account', unit: 'percent', used: 7 }, 'zh-CN'),
    undefined,
  )
  for (const cost of [-1, '68.80', null]) {
    const invalid = structuredClone(observation)
    invalid.snapshot.quota_windows[2].used_cost_usd = cost
    await assert.rejects(
      refreshCredentialObservation({ request: async () => invalid }, 4, 4),
      InvalidResponseError,
    )
    assert.throws(() => readObservation(invalid), InvalidResponseError)
  }

  const item = {
    credential_id: 4,
    connection_type: 'api_key',
    name: '',
    mask: 'd***r@example.com',
    account: { email: 'driver@example.com', email_mask: 'd***r@example.com' },
    secret_version: 1,
    auth_state: 'ready',
    configured_status: 'active',
    effective_status: 'available',
    weight: 50,
    model_cooldowns: [],
    recent_success_count: 0,
    recent_failure_count: 0,
    consecutive_failure_count: 0,
    last_failure_category: 'ok',
    last_status_code: null,
    cooldown_until_ms: null,
    recovery: { mode: 'none', automatic: false, at_ms: null },
    proxy: {
      configured_mode: 'inherit',
      effective_mode: 'direct',
      effective_source: 'default',
      has_auth: false,
    },
    observation,
  }
  assert.equal(
    projectCredentialItem(item).observation.snapshot.quota_windows[2].used_cost_usd,
    68.8,
  )
  assert.equal(
    projectCredentialDetail({ credential: item, observation }).observation.snapshot.quota_windows[3]
      .used_cost_usd,
    256.44,
  )
  const duplicatePrimary = structuredClone(observation)
  duplicatePrimary.snapshot.quota_windows[3].is_primary = true
  await assert.rejects(
    refreshCredentialObservation({ request: async () => duplicatePrimary }, 4, 4),
    InvalidResponseError,
  )
  console.log('NexusToken 同步、列表、详情及用量展示通过（含个人/整车金额、零值、缺失金额和超限）')

  if (process.argv[2]) {
    const actual = JSON.parse(await readFile(process.argv[2], 'utf8'))
    const parsed = projectChannelList(actual)
    assert(parsed.items.some((item) => item.channel_id === 'nexustoken'))
    assert(parsed.items.some((item) => item.channel_id === 'newapi'))
    assert.equal((await readModern(actual)).length, parsed.total)
    console.log(`完整后端渠道目录解析通过：${parsed.total} 个渠道`)
  }
  console.log('渠道目录普通凭据字段及敏感字段校验通过')
} finally {
  await server.close()
}
