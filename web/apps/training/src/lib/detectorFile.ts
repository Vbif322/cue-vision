// Файл детектора (`snooker_cv run --events`) до отправки: сжат ли он и что в нём.
// Разбор здесь — только для предпросмотра; принимает и считает удары сервер.

const GZIP_MAGIC = [0x1f, 0x8b]

export async function isGzip(file: Blob): Promise<boolean> {
  const head = new Uint8Array(await file.slice(0, 2).arrayBuffer())
  return head[0] === GZIP_MAGIC[0] && head[1] === GZIP_MAGIC[1]
}

/** Тело для отправки: сжатое gzip, если браузер умеет, — запись уходит в разы быстрее. */
export async function uploadBody(file: Blob): Promise<{ body: Blob; gzip: boolean }> {
  if (await isGzip(file)) return { body: file, gzip: true }
  if (typeof CompressionStream === 'undefined') return { body: file, gzip: false }
  const body = await new Response(file.stream().pipeThrough(new CompressionStream('gzip'))).blob()
  return { body, gzip: true }
}

export interface FilePreview {
  /** Строк с событиями (пустые не считаются). */
  events: number
  /** Строк, которые не разобрались как JSON-объект. */
  badLines: number
  /** Номера первых таких строк, с 1. */
  badLineNumbers: number[]
  shots: number
  pots: number
  /** Шапок session_start: обычно одна. */
  sessions: number
  /** Есть session_end — детектор дошёл до конца записи. */
  ended: boolean
  startedAt?: string
  source?: string
  sourceKind?: string
  hall?: string
  table?: string
  game?: string
  /** Секунды от начала сессии до последнего события. */
  duration?: number
}

type Raw = Record<string, unknown>

const str = (v: unknown) => (typeof v === 'string' && v !== '' ? v : undefined)

async function readText(file: Blob): Promise<string> {
  if (!(await isGzip(file))) return file.text()
  if (typeof DecompressionStream === 'undefined') throw new Error('браузер не умеет распаковывать gzip')
  return new Response(file.stream().pipeThrough(new DecompressionStream('gzip'))).text()
}

/** Разбирает файл так же, как сервер: JSON Lines либо один JSON-массив событий. */
export async function previewFile(file: Blob): Promise<FilePreview> {
  const text = (await readText(file)).replace(/^﻿/, '')
  const p: FilePreview = { events: 0, badLines: 0, badLineNumbers: [], shots: 0, pots: 0, sessions: 0, ended: false }

  const take = (ev: Raw) => {
    p.events++
    if (typeof ev.t === 'number' && (p.duration === undefined || ev.t > p.duration)) p.duration = ev.t
    switch (ev.type) {
      case 'session_start':
        p.sessions++
        if (p.sessions === 1) {
          p.startedAt = str(ev.started_at)
          p.source = str(ev.source)
          p.sourceKind = str(ev.source_kind)
          p.hall = str(ev.hall)
          p.table = str(ev.table)
          p.game = str(ev.game)
        }
        break
      case 'session_end':
        p.ended = true
        break
      case 'shot_end':
        p.shots++
        break
      case 'ball_pocketed':
        p.pots++
        break
    }
  }
  const bad = (line: number) => {
    p.badLines++
    if (p.badLineNumbers.length < 5) p.badLineNumbers.push(line)
  }

  if (text.trimStart().startsWith('[')) {
    const all: unknown = JSON.parse(text)
    if (!Array.isArray(all)) throw new Error('ожидался массив событий')
    all.forEach((ev, i) => (ev && typeof ev === 'object' && !Array.isArray(ev) ? take(ev as Raw) : bad(i + 1)))
    return p
  }

  let line = 0
  for (const raw of text.split('\n')) {
    line++
    if (raw.trim() === '') continue
    try {
      const ev: unknown = JSON.parse(raw)
      if (ev && typeof ev === 'object' && !Array.isArray(ev)) take(ev as Raw)
      else bad(line)
    } catch {
      bad(line)
    }
  }
  return p
}

/** Имя файла записи без каталога: в source детектор пишет путь, как его получил. */
export function baseName(path: string): string {
  return path.split(/[\\/]/).pop() ?? path
}

const STREAM_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/

export const validStream = (name: string) => STREAM_RE.test(name)

/** Поток, подсказанный шапкой: зал и стол детектора (hall-table или hall). */
export function suggestedStream(p: FilePreview): string | undefined {
  if (!p.hall) return undefined
  const name = p.table ? `${p.hall}-${p.table}` : p.hall
  return validStream(name) ? name : undefined
}
