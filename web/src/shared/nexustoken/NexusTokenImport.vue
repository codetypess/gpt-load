<script setup lang="ts">
import { computed, onScopeDispose, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useApiClient } from '@shared/http/client-context'

const props = defineProps<{
  modelValue: string
  baseUrl: string
  groupId?: number
  proxy?: unknown
  disabled?: boolean
  entryDisabled?: boolean
}>()
const emit = defineEmits<{
  'update:modelValue': [value: string]
  busy: [value: boolean]
}>()
const { locale } = useI18n()
const words = computed(() => {
  if (locale.value.startsWith('zh'))
    return {
      account: '账号密码登录',
      key: '仅导入 API Key',
      email: '账户邮箱',
      password: '密码',
      code: '两步验证码',
      login: '登录并读取拼车 Key',
      verify: '验证并读取',
      loading: '正在读取…',
      help: '登录后选择已绑定拼车的 Key，可同步个人和整车额度、重置卡及积分。',
      keyHelp: '每行一个 API Key。仅导入 Key 时，无法读取需要登录的拼车额度。',
      empty: '该账户没有已绑定有效拼车的 Key，请先在网站绑定后重新读取。',
      failed: '连接失败，请检查网站地址、账号密码或验证码。',
      selected: '选择要导入的拼车 Key',
      count: '已准备的 Key 数量',
    }
  if (locale.value.startsWith('ja'))
    return {
      account: 'アカウントでログイン',
      key: 'API Key のみ',
      email: 'メールアドレス',
      password: 'パスワード',
      code: '二段階認証コード',
      login: 'ログインして共有 Key を取得',
      verify: '認証して取得',
      loading: '取得中…',
      help: '共有枠に紐づく Key を選択して、個人・共有枠の使用量、リセットカード、ポイントを同期します。',
      keyHelp:
        'API Key を1行に1つ入力します。Key のみではログインが必要な共有枠情報を取得できません。',
      empty: '有効な共有枠に紐づく Key がありません。サイトで紐づけてから再試行してください。',
      failed: '接続に失敗しました。URL、ログイン情報、認証コードを確認してください。',
      selected: 'インポートする Key',
      count: '準備済み Key 数',
    }
  return {
    account: 'Account login',
    key: 'API Key only',
    email: 'Account email',
    password: 'Password',
    code: 'Two-factor code',
    login: 'Sign in and load carpool keys',
    verify: 'Verify and load',
    loading: 'Loading…',
    help: 'Select keys bound to a carpool to sync personal and vehicle quotas, reset cards, and points.',
    keyHelp:
      'One API key per line. Keys alone cannot access carpool information that requires account login.',
    empty: 'No keys are bound to an active carpool. Bind a key on the website and load again.',
    failed: 'Connection failed. Check the website URL, credentials, or verification code.',
    selected: 'Select carpool keys to import',
    count: 'Keys ready',
  }
})
interface Choice {
  id: number
  name: string
  plan: string
  credential: string
}
interface LoginResult {
  temp_token?: string
  choices: Choice[]
}
const client = useApiClient()
const id = useId()
const mode = ref('account')
const email = ref('')
const password = ref('')
const code = ref('')
const temporaryToken = ref('')
const choices = ref<Choice[]>([])
const selected = ref(new Set<number>())
const busy = ref(false)
const error = ref('')
const loaded = ref(false)
const count = computed(() => props.modelValue.split('\n').filter((line) => line.trim()).length)
let revision = 0
let controller: AbortController | undefined
function reset(): void {
  revision++
  controller?.abort()
  password.value = ''
  temporaryToken.value = ''
  code.value = ''
  choices.value = []
  selected.value = new Set()
  error.value = ''
  loaded.value = false
  busy.value = false
  emit('busy', false)
}
watch(
  () => [props.baseUrl, props.groupId, JSON.stringify(props.proxy), mode.value],
  () => {
    reset()
    emit('update:modelValue', '')
  },
)
onScopeDispose(reset)
function choose(choice: Choice, checked: boolean): void {
  const next = new Set(selected.value)
  if (checked) next.add(choice.id)
  else next.delete(choice.id)
  selected.value = next
  emit(
    'update:modelValue',
    choices.value
      .filter((item) => next.has(item.id))
      .map((item) => item.credential)
      .join('\n'),
  )
}
async function login(): Promise<void> {
  if (busy.value || props.disabled || props.entryDisabled || !props.baseUrl.trim()) return
  const current = ++revision
  controller = new AbortController()
  busy.value = true
  emit('busy', true)
  error.value = ''
  loaded.value = false
  choices.value = []
  selected.value = new Set()
  emit('update:modelValue', '')
  try {
    const result = await client.request<LoginResult>('/api/nexustoken/login', {
      method: 'POST',
      signal: controller.signal,
      json: {
        base_url: props.baseUrl,
        email: email.value.trim(),
        password: password.value,
        ...(temporaryToken.value
          ? { temp_token: temporaryToken.value, totp_code: code.value }
          : {}),
        ...(props.groupId
          ? { group_id: props.groupId }
          : props.proxy
            ? { proxy: props.proxy }
            : {}),
      },
    })
    if (current !== revision) return
    password.value = ''
    code.value = ''
    if (result.temp_token) {
      temporaryToken.value = result.temp_token
      return
    }
    temporaryToken.value = ''
    choices.value = result.choices
    selected.value = new Set(result.choices.map((item) => item.id))
    emit('update:modelValue', result.choices.map((item) => item.credential).join('\n'))
    loaded.value = true
  } catch {
    if (current === revision) error.value = words.value.failed
  } finally {
    if (current === revision) {
      busy.value = false
      emit('busy', false)
    }
  }
}
</script>

<template>
  <section class="nexus-import">
    <div class="nexus-import__modes">
      <label v-for="option in ['account', 'key']" :key="option">
        <input
          v-model="mode"
          type="radio"
          :name="id + '-mode'"
          :value="option"
          :disabled="disabled || busy"
        />
        {{ option === 'account' ? words.account : words.key }}
      </label>
    </div>
    <template v-if="mode === 'account'">
      <p>{{ words.help }}</p>
      <div class="nexus-import__fields">
        <label :for="id + '-email'"
          >{{ words.email }}
          <input
            :id="id + '-email'"
            v-model="email"
            type="email"
            autocomplete="username"
            :disabled="disabled || busy || Boolean(temporaryToken)"
          />
        </label>
        <label v-if="!temporaryToken" :for="id + '-password'"
          >{{ words.password }}
          <input
            :id="id + '-password'"
            v-model="password"
            type="password"
            autocomplete="current-password"
            :disabled="disabled || busy"
            @keydown.enter.prevent="login"
          />
        </label>
        <label v-else :for="id + '-code'"
          >{{ words.code }}
          <input
            :id="id + '-code'"
            v-model="code"
            inputmode="numeric"
            autocomplete="one-time-code"
            :disabled="disabled || busy"
            @keydown.enter.prevent="login"
          />
        </label>
      </div>
      <button
        type="button"
        :disabled="
          disabled ||
          busy ||
          entryDisabled ||
          !baseUrl ||
          (temporaryToken ? !code : !email || !password)
        "
        @click="login"
      >
        {{ busy ? words.loading : temporaryToken ? words.verify : words.login }}
      </button>
      <p v-if="error" role="alert">{{ error }}</p>
      <p v-else-if="loaded && !choices.length" role="status">{{ words.empty }}</p>
      <fieldset v-if="choices.length" :disabled="disabled || busy">
        <legend>{{ words.selected }}</legend>
        <label v-for="choice in choices" :key="choice.id" class="nexus-import__choice">
          <input
            type="checkbox"
            :checked="selected.has(choice.id)"
            @change="choose(choice, ($event.target as HTMLInputElement).checked)"
          />
          <strong>{{ choice.name || '#' + choice.id }}</strong
          ><span>{{ choice.plan }}</span>
        </label>
      </fieldset>
    </template>
    <label v-else :for="id + '-keys'"
      >{{ words.keyHelp }}
      <textarea
        :id="id + '-keys'"
        :value="modelValue"
        rows="6"
        autocomplete="off"
        spellcheck="false"
        :disabled="disabled"
        @input="emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)"
      />
    </label>
    <p role="status">{{ words.count }}: {{ count }}</p>
  </section>
</template>

<style scoped>
.nexus-import {
  display: grid;
  gap: 12px;
  color: inherit;
  font-size: 14px;
}
.nexus-import p {
  margin: 0;
  opacity: 0.8;
}
.nexus-import__modes,
.nexus-import__choice {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.nexus-import__modes label {
  display: flex;
  align-items: center;
  gap: 6px;
}
.nexus-import__fields {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 220px), 1fr));
  gap: 12px;
}
.nexus-import__fields label,
.nexus-import > label {
  display: grid;
  gap: 6px;
}
.nexus-import input:not([type='radio']):not([type='checkbox']),
.nexus-import textarea {
  box-sizing: border-box;
  width: 100%;
  min-width: 0;
  padding: 10px 12px;
  color: inherit;
  background: transparent;
  border: 1px solid var(--modern-border, var(--color-border-subtle, currentColor));
  border-radius: 8px;
}
.nexus-import button {
  justify-self: start;
  padding: 8px 14px;
  border: 1px solid var(--modern-border, var(--color-border-subtle, currentColor));
  border-radius: 8px;
  color: inherit;
  background: transparent;
  cursor: pointer;
}
.nexus-import button:disabled {
  opacity: 0.5;
  cursor: default;
}
.nexus-import fieldset {
  min-width: 0;
  margin: 0;
  padding: 12px;
  border: 1px solid var(--modern-border, var(--color-border-subtle, currentColor));
  border-radius: 8px;
}
.nexus-import__choice {
  padding-block: 6px;
  overflow-wrap: anywhere;
}
.nexus-import__choice span {
  opacity: 0.8;
}
</style>
