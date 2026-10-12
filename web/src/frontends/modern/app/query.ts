import { QueryClient } from '@tanstack/vue-query'

export function createModernQueryClient() {
  return new QueryClient({
    defaultOptions: {
      // 使用查询库默认的 visibilitychange 监听与页面挂载刷新，不监听窗口 focus。
      // staleTime 只决定事件触发时是否更新缓存，不会产生定时请求。
      queries: {
        retry: false,
        staleTime: 0,
        // NexusToken reads saved observations; the backend polls upstream every 5 minutes.
        refetchInterval: (query) => (query.meta?.channelID === 'nexustoken' ? 30_000 : false),
        refetchIntervalInBackground: false,
        refetchOnWindowFocus: true,
        refetchOnReconnect: false,
        refetchOnMount: true,
      },
      mutations: { retry: false },
    },
  })
}
