import { request } from './http'
import type { IngestResult } from './types'

export const eventsApi = {
  /**
   * Файл детектора (`snooker_cv run --events`) в поток stream. gzip — тело сжато:
   * сервер распакует сам. Повторная загрузка того же файла в тот же поток ничего не удваивает.
   */
  upload: (body: Blob, stream: string, gzip: boolean) =>
    request<IngestResult>('POST', '/events', {
      query: { stream },
      raw: body,
      headers: { 'Content-Type': 'application/x-ndjson', ...(gzip ? { 'Content-Encoding': 'gzip' } : {}) },
    }),
}
