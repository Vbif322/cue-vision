import { onScopeDispose, ref, shallowRef, watch, type WatchSource } from 'vue'

/**
 * Загрузка данных с состоянием: data, error, loading и reload.
 * При смене источников (например, id из маршрута) прежний запрос отменяется.
 */
export function useAsync<T>(load: (signal: AbortSignal) => Promise<T>, sources: WatchSource[] = []) {
  const data = shallowRef<T>()
  const error = shallowRef<Error>()
  const loading = ref(false)
  let controller: AbortController | undefined

  async function reload() {
    controller?.abort()
    const current = new AbortController()
    controller = current
    loading.value = true
    error.value = undefined
    try {
      data.value = await load(current.signal)
    } catch (err) {
      if (!current.signal.aborted) error.value = err instanceof Error ? err : new Error(String(err))
    } finally {
      if (controller === current) loading.value = false
    }
  }

  watch(sources, reload, { immediate: true })
  onScopeDispose(() => controller?.abort())

  return { data, error, loading, reload }
}
