<script setup lang="ts">
import { computed, reactive, ref, shallowRef } from 'vue'
import { RouterLink } from 'vue-router'

import { ApiError, eventsApi, playersApi, trainingsApi, type IngestResult, type Training } from '@/api'
import { useAsync } from '@/composables/useAsync'
import { baseName, previewFile, suggestedStream, uploadBody, validStream, type FilePreview } from '@/lib/detectorFile'
import { formatDateTime, formatDuration, formatPercent } from '@/lib/format'

const message = (err: unknown) => {
  if (err instanceof ApiError && err.status === 413) return 'файл больше, чем принимает сервер'
  return err instanceof Error ? err.message : String(err)
}

// Последний стол, куда загружали: у тренера он обычно один.
const STREAM_KEY = 'cv.upload.stream'
function storedStream(): string {
  try {
    return localStorage.getItem(STREAM_KEY) ?? ''
  } catch {
    return ''
  }
}
function rememberStream(stream: string) {
  try {
    localStorage.setItem(STREAM_KEY, stream)
  } catch {
    // Хранилище недоступно (приватный режим) — просто не запомним.
  }
}

// Столы, которые сервис уже видел: подсказка к полю.
const recent = useAsync((signal) => trainingsApi.list({ limit: 200 }, signal))
const knownStreams = computed(() => {
  const names = new Set((recent.data.value?.items ?? []).map((t) => t.stream))
  const hint = preview.value && suggestedStream(preview.value)
  if (hint) names.add(hint)
  // Сортируется свежая копия; toSorted нет в lib проекта (ES2020).
  // oxlint-disable-next-line unicorn/no-array-sort
  return [...names].sort()
})

// --- файл и предпросмотр ---

const file = shallowRef<File>()
const preview = shallowRef<FilePreview>()
const previewError = ref('')
const reading = ref(false)
const dragging = ref(false)
const stream = ref(storedStream())

async function choose(f: File | undefined) {
  file.value = f
  preview.value = undefined
  previewError.value = ''
  result.value = undefined
  uploadError.value = ''
  trainings.value = []
  if (!f) return
  reading.value = true
  try {
    const p = await previewFile(f)
    if (file.value !== f) return
    preview.value = p
    if (!stream.value) stream.value = suggestedStream(p) ?? ''
  } catch (err) {
    if (file.value === f) previewError.value = `Файл не читается: ${message(err)}`
  } finally {
    if (file.value === f) reading.value = false
  }
}

function onInput(e: Event) {
  void choose((e.target as HTMLInputElement).files?.[0])
}

function onDrop(e: DragEvent) {
  dragging.value = false
  const f = e.dataTransfer?.files[0]
  if (f) void choose(f)
}

const streamValid = computed(() => validStream(stream.value))
const canUpload = computed(
  () => !!file.value && !!preview.value && preview.value.events > 0 && streamValid.value && !uploading.value,
)

// --- отправка ---

const uploading = ref(false)
const uploadError = ref('')
const result = shallowRef<IngestResult>()
const uploadedTo = ref('')

async function upload() {
  if (!file.value || !canUpload.value) return
  uploading.value = true
  uploadError.value = ''
  result.value = undefined
  trainings.value = []
  savedId.value = undefined
  rowError.value = undefined
  try {
    const { body, gzip } = await uploadBody(file.value)
    const res = await eventsApi.upload(body, stream.value, gzip)
    result.value = res
    uploadedTo.value = stream.value
    rememberStream(stream.value)
    void recent.reload()
    await loadTrainings(res.sessions)
  } catch (err) {
    uploadError.value = message(err)
  } finally {
    uploading.value = false
  }
}

const alreadyUploaded = computed(() => !!result.value && result.value.stored === 0 && result.value.duplicates > 0)

// --- тренировки из файла ---

const trainings = shallowRef<Training[]>([])
const trainingsError = ref('')
const players = useAsync((signal) => playersApi.list({ limit: 1000 }, signal))

interface Draft {
  player_id: number | ''
  title: string
}
const drafts = reactive<Record<number, Draft>>({})
const saving = ref<number>()
const savedId = ref<number>()
const rowError = ref<{ id: number; text: string }>()

async function loadTrainings(sessions: number[]) {
  trainingsError.value = ''
  try {
    const lists = await Promise.all(sessions.map((id) => trainingsApi.list({ session_id: id, limit: 200 })))
    const byId = new Map<number, Training>()
    for (const t of lists.flatMap((l) => l.items)) byId.set(t.id, t)
    // oxlint-disable-next-line unicorn/no-array-sort -- сортируется свежая копия
    trainings.value = [...byId.values()].sort((a, b) => a.started_at.localeCompare(b.started_at))
    for (const t of trainings.value) drafts[t.id] = { player_id: t.player_id ?? '', title: t.title ?? '' }
  } catch (err) {
    trainingsError.value = `Тренировки не загрузились: ${message(err)}`
  }
}

const changed = (t: Training) => {
  const d = drafts[t.id]
  return !!d && (d.player_id !== (t.player_id ?? '') || d.title.trim() !== (t.title ?? ''))
}

async function save(t: Training) {
  const d = drafts[t.id]
  if (!d) return
  saving.value = t.id
  savedId.value = undefined
  rowError.value = undefined
  try {
    const title = d.title.trim()
    const updated = await trainingsApi.update(t.id, {
      player_id: d.player_id === '' ? null : d.player_id,
      title: title === '' ? null : title,
    })
    trainings.value = trainings.value.map((x) => (x.id === t.id ? updated : x))
    drafts[t.id] = { player_id: updated.player_id ?? '', title: updated.title ?? '' }
    savedId.value = t.id
  } catch (err) {
    rowError.value = { id: t.id, text: message(err) }
  } finally {
    saving.value = undefined
  }
}

// Новый игрок — сразу в список выбора; если тренировка одна, ей же и приписывается.
const newPlayer = ref('')
const creatingPlayer = ref(false)
const playerError = ref('')

async function createPlayer() {
  const name = newPlayer.value.trim()
  if (!name) return
  creatingPlayer.value = true
  playerError.value = ''
  try {
    const p = await playersApi.create(name)
    newPlayer.value = ''
    await players.reload()
    const only = trainings.value.length === 1 ? trainings.value[0] : undefined
    const draft = only && drafts[only.id]
    if (draft) draft.player_id = p.id
  } catch (err) {
    playerError.value = message(err)
  } finally {
    creatingPlayer.value = false
  }
}

const sizeLabel = (bytes: number) =>
  bytes < 1024 * 1024 ? `${Math.max(1, Math.round(bytes / 1024))} КБ` : `${(bytes / 1024 / 1024).toFixed(1)} МБ`
</script>

<template>
  <section>
    <h1>Загрузка файла детектора</h1>
    <p class="muted">
      Файл событий, который детектор пишет после прогона видео (<code>events.jsonl</code>, можно сжатый
      <code>.gz</code>). Сервер соберёт из него удары и тренировку со статистикой. Повторная загрузка того же файла в тот
      же стол ничего не удваивает.
    </p>

    <form class="upload" @submit.prevent="upload">
      <label
        class="drop"
        :class="{ 'drop--over': dragging }"
        @dragover.prevent="dragging = true"
        @dragleave="dragging = false"
        @drop.prevent="onDrop"
      >
        <input type="file" accept=".jsonl,.json,.gz,.ndjson,application/json" @change="onInput" />
        <span v-if="file">
          <strong>{{ file.name }}</strong> · {{ sizeLabel(file.size) }}
        </span>
        <span v-else>Перетащите файл сюда или нажмите, чтобы выбрать</span>
      </label>

      <p v-if="reading" class="muted">Читаю файл…</p>
      <p v-if="previewError" class="error" role="alert">{{ previewError }}</p>

      <dl v-if="preview" class="preview">
        <div>
          <dt>Запись</dt>
          <dd>{{ preview.source ? baseName(preview.source) : '—' }}</dd>
        </div>
        <div>
          <dt>Снята</dt>
          <dd>
            {{ formatDateTime(preview.startedAt) }}
            <span v-if="preview.sourceKind === 'live'" class="muted"> · живой прогон</span>
          </dd>
        </div>
        <div>
          <dt>Длительность</dt>
          <dd>{{ formatDuration(preview.duration) }}</dd>
        </div>
        <div>
          <dt>Удары</dt>
          <dd>{{ preview.shots }} · упало шаров: {{ preview.pots }}</dd>
        </div>
        <div>
          <dt>Событий</dt>
          <dd>{{ preview.events }}</dd>
        </div>
      </dl>
      <template v-if="preview">
        <p v-if="preview.events === 0" class="error">В файле нет ни одного события.</p>
        <p v-else-if="preview.sessions === 0" class="muted">
          В файле нет шапки <code>session_start</code>: время ударов будет отсчитано от момента загрузки.
        </p>
        <p v-if="preview.events > 0 && !preview.ended" class="muted">
          Нет <code>session_end</code> — похоже, детектор не дошёл до конца записи. Загрузить можно, тренировка останется
          открытой.
        </p>
        <p v-if="preview.badLines" class="error">
          Не разобрано строк: {{ preview.badLines }} (строки {{ preview.badLineNumbers.join(', ')
          }}<template v-if="preview.badLines > preview.badLineNumbers.length">, …</template>). Остальное сервер примет.
        </p>
      </template>

      <label class="field stream">
        <span>Стол (поток)</span>
        <input
          v-model.trim="stream"
          list="known-streams"
          autocomplete="off"
          autocapitalize="none"
          placeholder="например, club1-t1"
          required
          :aria-invalid="stream !== '' && !streamValid"
        />
        <datalist id="known-streams">
          <option v-for="s in knownStreams" :key="s" :value="s" />
        </datalist>
      </label>
      <p v-if="stream !== '' && !streamValid" class="error">
        Латинские буквы, цифры, точка, дефис, подчёркивание; до 64 символов.
      </p>

      <div>
        <button type="submit" :disabled="!canUpload">{{ uploading ? 'Загружаю и разбираю…' : 'Загрузить' }}</button>
      </div>
      <p v-if="uploadError" class="error" role="alert">Не загрузилось: {{ uploadError }}</p>
    </form>

    <template v-if="result">
      <h2>Итог</h2>
      <p v-if="alreadyUploaded" class="muted">
        Этот файл уже был загружен в стол {{ uploadedTo }} — ничего не изменилось.
      </p>
      <p v-else class="success">Принято в стол {{ uploadedTo }}: новых событий {{ result.stored }}.</p>
      <p class="muted">
        Получено {{ result.received }} · уже были {{ result.duplicates }} · не хранятся {{ result.skipped }} · отвергнуто
        {{ result.invalid }}
      </p>
      <details v-if="result.errors?.length">
        <summary class="error">Отвергнутые строки: {{ result.invalid }}</summary>
        <ul>
          <li v-for="e in result.errors" :key="e.line">строка {{ e.line }}: {{ e.error }}</li>
        </ul>
      </details>

      <h2>Тренировки из файла</h2>
      <p v-if="trainingsError" class="error">{{ trainingsError }}</p>
      <p v-else-if="trainings.length === 0" class="muted">
        Тренировок не получилось: в записи нет ударов, либо автоматические тренировки на сервере выключены.
      </p>

      <template v-else>
        <p class="muted">Укажите игрока — тогда тренировка попадёт в его статистику.</p>
        <table>
          <thead>
            <tr>
              <th>Тренировка</th>
              <th>Начало</th>
              <th>Удары</th>
              <th>Игрок</th>
              <th>Название</th>
              <th />
            </tr>
          </thead>
          <tbody>
            <template v-for="t in trainings" :key="t.id">
              <tr>
                <td>
                  <RouterLink :to="{ name: 'training', params: { id: t.id } }">#{{ t.id }}</RouterLink>
                  <span v-if="t.status === 'active'" class="muted"> · идёт</span>
                </td>
                <td>{{ formatDateTime(t.started_at) }}</td>
                <td>{{ t.pots }} / {{ t.shots }} · {{ formatPercent(t.shots ? t.pots / t.shots : null) }}</td>
                <td>
                  <select
                    v-if="drafts[t.id]"
                    v-model="drafts[t.id]!.player_id"
                    :disabled="saving === t.id"
                    aria-label="Игрок"
                  >
                    <option value="">— не указан —</option>
                    <option v-for="p in players.data.value?.items ?? []" :key="p.id" :value="p.id">{{ p.name }}</option>
                  </select>
                </td>
                <td>
                  <input
                    v-if="drafts[t.id]"
                    v-model="drafts[t.id]!.title"
                    :disabled="saving === t.id"
                    placeholder="Свободная игра"
                    aria-label="Название"
                  />
                </td>
                <td>
                  <button type="button" :disabled="!changed(t) || saving === t.id" @click="save(t)">Сохранить</button>
                  <span v-if="savedId === t.id && !changed(t)" class="success"> ✓</span>
                </td>
              </tr>
              <tr v-if="rowError?.id === t.id">
                <td colspan="6" class="error" role="alert">{{ rowError.text }}</td>
              </tr>
            </template>
          </tbody>
        </table>

        <form class="new-player" @submit.prevent="createPlayer">
          <span class="muted">Игрока нет в списке?</span>
          <input v-model="newPlayer" placeholder="Имя и фамилия" aria-label="Новый игрок" maxlength="200" />
          <button type="submit" class="secondary" :disabled="!newPlayer.trim() || creatingPlayer">Добавить игрока</button>
        </form>
        <p v-if="playerError" class="error" role="alert">{{ playerError }}</p>
        <p v-if="players.error.value" class="error">Игроки не загрузились: {{ players.error.value.message }}</p>
      </template>
    </template>
  </section>
</template>

<style scoped>
.upload {
  display: grid;
  gap: 12px;
  max-width: 640px;
}
.drop {
  position: relative;
  display: grid;
  place-items: center;
  min-height: 110px;
  padding: 16px;
  border: 2px dashed var(--border);
  border-radius: 10px;
  background: var(--surface);
  text-align: center;
  color: var(--text-muted);
  cursor: pointer;
}
.drop:hover,
.drop--over {
  border-color: var(--accent);
  color: var(--text);
}
.drop input {
  position: absolute;
  inset: 0;
  opacity: 0;
  cursor: pointer;
}
.drop strong {
  color: var(--text-strong);
  word-break: break-all;
}
.preview {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 8px 16px;
  margin: 0;
}
.preview dt {
  font-size: 13px;
  color: var(--text-muted);
}
.preview dd {
  margin: 0;
  word-break: break-word;
}
.stream {
  max-width: 320px;
}
.upload p {
  margin: 0;
}
code {
  font-size: 13px;
}
td select,
td input {
  width: 100%;
  min-width: 120px;
}
.new-player {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-top: 16px;
}
@media (max-width: 640px) {
  th:nth-child(2),
  td:nth-child(2),
  th:nth-child(5),
  td:nth-child(5) {
    display: none;
  }
}
</style>
