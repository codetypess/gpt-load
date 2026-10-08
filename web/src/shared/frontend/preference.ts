export type FrontendID = 'classic' | 'modern'

// 界面偏好仅影响当前浏览器，与登录状态和接口权限无关。
const frontendStorageKey = 'gpt-load.frontend.v2'
const legacyFrontendStorageKey = 'gpt-load.frontend'

function getStorage(): Storage | undefined {
  try {
    return window.localStorage
  } catch {
    return undefined
  }
}

function readStorageKey(key: string): string {
  try {
    return getStorage()?.getItem(key) ?? ''
  } catch {
    return ''
  }
}

function removePreference(key: string): void {
  try {
    getStorage()?.removeItem(key)
  } catch {
    // 存储不可用时，默认经典版入口仍然生效。
  }
}

export function getPreferredFrontend(): FrontendID {
  removePreference(legacyFrontendStorageKey)
  return readStorageKey(frontendStorageKey) === 'modern' ? 'modern' : 'classic'
}

export function switchFrontend(frontend: FrontendID, path = '/settings'): void {
  const storage = getStorage()
  if (!storage) {
    throw new Error('FRONTEND_PREFERENCE_NOT_SAVED')
  }
  storage.removeItem(legacyFrontendStorageKey)
  if (frontend === 'modern') storage.setItem(frontendStorageKey, frontend)
  else storage.removeItem(frontendStorageKey)
  const saved = storage.getItem(frontendStorageKey)
  if ((frontend === 'modern' && saved !== frontend) || (frontend === 'classic' && saved !== null)) {
    throw new Error('FRONTEND_PREFERENCE_NOT_SAVED')
  }
  window.location.assign(path)
}
