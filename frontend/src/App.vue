<script setup>
import { ref, reactive, onMounted, onBeforeUnmount, computed } from 'vue'

// Same-origin, relative paths - nginx.conf proxies /api and /media to the
// backend container. This works unmodified whether the frontend is served
// from localhost:3000 (docker-compose) or a real HTTPS domain (EasyPanel/any
// reverse-proxy host): the old hardcoded `http://<hostname>:8081` broke the
// moment the frontend moved behind HTTPS/a domain with no port 8081 exposed
// (browsers block that as mixed content) or a different backend hostname.
const API_BASE = '/api'
const MEDIA_BASE = '/media'

// State variables
const currentTab = ref('channels') // 'channels', 'calendar', 'settings'
const channels = ref([])
const activeChannel = ref(null)
const activeVideo = ref(null)
const videos = ref([])

// Form states
const showCreateChannelModal = ref(false)
const newChannel = reactive({ name: '', niche: 'Curiosidades e Mistérios' })
const newVideoTopic = ref('')
const scheduleDate = ref('')

// Loading states
const isGeneratingChannel = ref(false)
const isGeneratingLogo = ref(false)
const isGeneratingBanner = ref(false)
const isGeneratingVideo = ref(false)
const isGeneratingIntro = ref(false)
const isGeneratingOutro = ref(false)
const isDeletingIntro = ref(false)
const isDeletingOutro = ref(false)
const showBookendsPage = ref(false)
const introScriptInput = ref('')
const outroScriptInput = ref('')
const renderPollingId = ref(null)

// --- Auth ---------------------------------------------------------------
const authToken = ref(localStorage.getItem('makinadark_token') || '')
const currentUserName = ref(localStorage.getItem('makinadark_user_name') || '')
const authMode = ref('login') // 'login' | 'register'
const authForm = reactive({ email: '', password: '', name: '' })
const authError = ref('')
const isAuthenticating = ref(false)
// Sign-up is off by default on the server (every account uses the
// operator's paid API keys) - only show "Criar Conta" when it's enabled.
const registrationEnabled = ref(false)

// Parses a JSON body without throwing on empty/non-JSON responses (e.g. an
// nginx 502 page), so callers can always show `data.error || HTTP <code>`.
const readJSON = async (res) => {
  try {
    return await res.json()
  } catch {
    return {}
  }
}
const errorText = (res, data) => data?.error || `HTTP ${res.status}`

// Wrapper around fetch that attaches the Bearer token and logs the user out
// on a 401 (expired/invalid token) instead of silently failing every call.
const apiFetch = async (path, options = {}) => {
  const headers = { ...(options.headers || {}) }
  if (authToken.value) headers['Authorization'] = `Bearer ${authToken.value}`
  const res = await fetch(`${API_BASE}${path}`, { ...options, headers })
  if (res.status === 401) {
    logout()
  }
  return res
}

const logout = () => {
  authToken.value = ''
  currentUserName.value = ''
  localStorage.removeItem('makinadark_token')
  localStorage.removeItem('makinadark_user_name')
  currentTab.value = 'channels'
  channels.value = []
  activeChannel.value = null
  activeVideo.value = null
  showBookendsPage.value = false
  channelCosts.value = null
  autoPilotStatus.value = null
  videos.value = []
  allScheduledVideos.value = []
  stopRenderPolling()
  stopVideoListPolling()
  stopChannelPolling()
}

const submitAuth = async () => {
  isAuthenticating.value = true
  authError.value = ''
  try {
    const path = authMode.value === 'login' ? '/auth/login' : '/auth/register'
    const res = await fetch(`${API_BASE}${path}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(authForm)
    })
    const data = await readJSON(res)
    if (!res.ok) {
      authError.value = data.error || 'Falha na autenticação.'
      return
    }
    authToken.value = data.token
    currentUserName.value = data.user?.name || data.user?.email || ''
    localStorage.setItem('makinadark_token', data.token)
    localStorage.setItem('makinadark_user_name', currentUserName.value)
    authForm.password = ''
    await fetchChannels()
  } catch (err) {
    authError.value = 'Erro de conexão com o backend.'
  } finally {
    isAuthenticating.value = false
  }
}

// The OAuth popup (backend /api/oauth/callback page) posts a message to its
// opener when it finishes, so the "Conectado" badge updates without a reload.
const onOAuthMessage = async (event) => {
  if (event.origin !== window.location.origin) return
  if (event.data?.type !== 'makinadark-oauth' || !activeChannel.value) return
  await refreshActiveChannel()
  await fetchAutoPilotStatus(activeChannel.value.id)
}

onMounted(async () => {
  window.addEventListener('message', onOAuthMessage)
  try {
    const res = await fetch(`${API_BASE}/auth/config`)
    if (res.ok) registrationEnabled.value = !!(await res.json()).registration_enabled
  } catch {
    // backend unreachable - the login form will report it on submit
  }
  if (authToken.value) fetchChannels()
})

onBeforeUnmount(() => {
  window.removeEventListener('message', onOAuthMessage)
  stopVideoListPolling()
  stopChannelPolling()
  stopRenderPolling()
})

const fetchChannels = async () => {
  try {
    const res = await apiFetch('/channels')
    if (res.ok) {
      channels.value = await res.json()
    }
  } catch (err) {
    console.error('Error fetching channels:', err)
  }
}

// While any video in the list is still generating, keep refetching so its
// progress % stays live instead of frozen at whatever it was when the list loaded.
const videoListPollingId = ref(null)
const channelPollingId = ref(null)

const syncChannelInList = (updated) => {
  if (!updated || !updated.id) return
  const idx = channels.value.findIndex(c => c.id === updated.id)
  if (idx !== -1) {
    channels.value[idx] = updated
  }
}

const startChannelPolling = (channelId) => {
  if (channelPollingId.value) return
  channelPollingId.value = setInterval(async () => {
    if (!activeChannel.value || activeChannel.value.id !== channelId) {
      stopChannelPolling()
      return
    }
    try {
      const res = await apiFetch(`/channels/${channelId}`)
      if (res.ok) {
        const updated = await res.json()
        activeChannel.value = updated
        syncChannelInList(updated)
        if (updated.intro_script && !introScriptInput.value) {
          introScriptInput.value = updated.intro_script
        }
        if (updated.outro_script && !outroScriptInput.value) {
          outroScriptInput.value = updated.outro_script
        }
        if (updated.intro_status !== 'generating' && updated.outro_status !== 'generating') {
          stopChannelPolling()
        }
      }
    } catch (err) {
      console.error('Error polling channel bookends status:', err)
    }
  }, 1500)
}

const stopChannelPolling = () => {
  if (channelPollingId.value) {
    clearInterval(channelPollingId.value)
    channelPollingId.value = null
  }
}

const fetchVideos = async (channelId) => {
  try {
    const res = await apiFetch(`/channels/${channelId}/videos`)
    if (res.ok) {
      videos.value = await res.json()
    }
  } catch (err) {
    console.error('Error fetching videos:', err)
  }

  if (videoListPollingId.value) {
    clearTimeout(videoListPollingId.value)
    videoListPollingId.value = null
  }
  if (activeChannel.value && activeChannel.value.id === channelId && !activeVideo.value && videos.value.some(v => isBusyStatus(v.status))) {
    videoListPollingId.value = setTimeout(() => fetchVideos(channelId), 4000)
  }
}

// "posting" is the transient state while the backend uploads to
// YouTube/TikTok - it needs live polling just like "generating".
const isBusyStatus = (status) => status === 'generating' || status === 'posting'

const refreshActiveChannel = async () => {
  if (!activeChannel.value) return null
  try {
    const res = await apiFetch(`/channels/${activeChannel.value.id}`)
    if (res.ok) {
      const fresh = await res.json()
      if (activeChannel.value && activeChannel.value.id === fresh.id) {
        activeChannel.value = fresh
      }
      syncChannelInList(fresh)
      return fresh
    }
  } catch (err) {
    console.error('Error refreshing channel:', err)
  }
  return null
}

const stopVideoListPolling = () => {
  if (videoListPollingId.value) {
    clearTimeout(videoListPollingId.value)
    videoListPollingId.value = null
  }
}

const selectChannel = async (channel) => {
  stopChannelPolling()
  activeChannel.value = channel
  activeVideo.value = null
  showBookendsPage.value = false
  narratorVoiceSelection.value = channel.narrator_voice_id || ''
  narratorVoiceSaved.value = false
  introScriptInput.value = channel.intro_script || ''
  outroScriptInput.value = channel.outro_script || ''
  try {
    const res = await apiFetch(`/channels/${channel.id}`)
    if (res.ok) {
      const fresh = await res.json()
      activeChannel.value = fresh
      syncChannelInList(fresh)
      narratorVoiceSelection.value = fresh.narrator_voice_id || ''
      introScriptInput.value = fresh.intro_script || ''
      outroScriptInput.value = fresh.outro_script || ''
    }
  } catch (err) {
    console.error('Error refreshing channel:', err)
  }
  await Promise.all([
    fetchVideos(channel.id),
    fetchChannelCosts(channel.id),
    fetchAutoPilotStatus(channel.id),
    fetchNarratorVoices(),
  ])
  if (activeChannel.value && (activeChannel.value.intro_status === 'generating' || activeChannel.value.outro_status === 'generating')) {
    startChannelPolling(channel.id)
  }
}

const openBookendsPage = async () => {
  if (!activeChannel.value) return
  showBookendsPage.value = true
  const fresh = await refreshActiveChannel()
  if (fresh) {
    if (fresh.intro_script) introScriptInput.value = fresh.intro_script
    if (fresh.outro_script) outroScriptInput.value = fresh.outro_script
    if (fresh.intro_status === 'generating' || fresh.outro_status === 'generating') {
      startChannelPolling(fresh.id)
    }
  }
}

// --- Contas de postagem (OAuth) -------------------------------------------
// The backend now hands out the consent URL from an authenticated endpoint
// (with a signed state) instead of a public link anyone could hit.
const connectingProvider = ref('')

const connectAccount = async (provider) => {
  if (!activeChannel.value) return
  // Open the popup synchronously (inside the click) so it isn't blocked,
  // then point it at the consent URL once the backend returns it.
  const popup = window.open('about:blank', '_blank', 'width=560,height=720')
  connectingProvider.value = provider
  try {
    const res = await apiFetch(`/channels/${activeChannel.value.id}/oauth/${provider}`)
    const data = await readJSON(res)
    if (!res.ok || !data.url) {
      if (popup) popup.close()
      alert('Não foi possível iniciar a conexão: ' + errorText(res, data))
      return
    }
    if (!popup) {
      window.location.href = data.url
      return
    }
    popup.location.href = data.url
    // Fallback for when the callback page runs on another origin (e.g.
    // REDIRECT_URI pointing straight at the backend port) and its
    // postMessage is ignored: refresh once the popup closes.
    const channelId = activeChannel.value.id
    const watcher = setInterval(async () => {
      if (!popup.closed) return
      clearInterval(watcher)
      if (activeChannel.value && activeChannel.value.id === channelId) {
        await refreshActiveChannel()
        await fetchAutoPilotStatus(channelId)
      }
    }, 1000)
  } catch (err) {
    if (popup) popup.close()
    alert('Erro de conexão ao iniciar a vinculação da conta.')
  } finally {
    connectingProvider.value = ''
  }
}

const disconnectAccount = async (provider) => {
  if (!activeChannel.value) return
  const label = provider === 'youtube' ? 'YouTube' : 'TikTok'
  if (!confirm(`Desconectar a conta do ${label} deste canal? Vídeos agendados não poderão ser publicados nele.`)) return
  const res = await apiFetch(`/channels/${activeChannel.value.id}/oauth/${provider}`, { method: 'DELETE' })
  const data = await readJSON(res)
  if (res.ok) {
    activeChannel.value = data
    syncChannelInList(data)
    await fetchAutoPilotStatus(data.id)
  } else {
    alert('Erro ao desconectar: ' + errorText(res, data))
  }
}

const deleteChannel = async () => {
  if (!activeChannel.value) return
  const ch = activeChannel.value
  if (!confirm(`Excluir o canal "${ch.name}" com TODOS os vídeos, imagens e arquivos? Essa ação não pode ser desfeita.`)) return
  const res = await apiFetch(`/channels/${ch.id}`, { method: 'DELETE' })
  const data = await readJSON(res)
  if (res.ok) {
    goBackToChannels()
    channels.value = channels.value.filter(c => c.id !== ch.id)
  } else {
    alert('Erro ao excluir o canal: ' + errorText(res, data))
  }
}

// --- Narrador do Canal -------------------------------------------------------
// Lets the operator pick which ElevenLabs voice narrates every scene going
// forward for this channel (see backend generateElevenLabsTTS).
const narratorVoices = ref([])
const narratorVoiceSelection = ref('')
const narratorVoiceSaved = ref(false)

const fetchNarratorVoices = async () => {
  if (narratorVoices.value.length > 0) return // fixed list, fetch once per session
  try {
    const res = await apiFetch('/narrator-voices')
    if (res.ok) {
      narratorVoices.value = await res.json()
    }
  } catch (err) {
    console.error('Error fetching narrator voices:', err)
  }
}

const updateNarratorVoice = async () => {
  if (!activeChannel.value) return
  narratorVoiceSaved.value = false
  try {
    const res = await apiFetch(`/channels/${activeChannel.value.id}/narrator-voice`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ voice_id: narratorVoiceSelection.value }),
    })
    const data = await res.json().catch(() => ({}))
    if (res.ok) {
      activeChannel.value = data
      syncChannelInList(data)
      narratorVoiceSaved.value = true
    } else {
      alert('Erro ao atualizar narrador: ' + (data.error || `HTTP ${res.status}`))
    }
  } catch (err) {
    console.error('Error updating narrator voice:', err)
    alert('Erro de conexão ao atualizar narrador.')
  }
}

// --- Auto-Pilot health ------------------------------------------------------
// Surfaces the backoff/failure state that used to live only in server logs -
// a channel stuck failing every cycle showed no sign of it anywhere before.
const autoPilotStatus = ref(null)

const fetchAutoPilotStatus = async (channelId) => {
  try {
    const res = await apiFetch(`/channels/${channelId}/autopilot-status`)
    if (res.ok) {
      autoPilotStatus.value = await res.json()
    }
  } catch (err) {
    console.error('Error fetching autopilot status:', err)
  }
}

// --- Cost dashboard --------------------------------------------------------
const channelCosts = ref(null)
const isLoadingCosts = ref(false)

const fetchChannelCosts = async (channelId) => {
  isLoadingCosts.value = true
  try {
    const res = await apiFetch(`/channels/${channelId}/costs`)
    if (res.ok) {
      channelCosts.value = await res.json()
    }
  } catch (err) {
    console.error('Error fetching channel costs:', err)
  } finally {
    isLoadingCosts.value = false
  }
}

const formatUSD = (value) => {
  return `$${(value || 0).toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 4 })}`
}

const providerLabel = (provider) => {
  const labels = {
    openai: 'OpenAI',
    gemini: 'Gemini',
    groq: 'Groq',
    anthropic: 'Claude (Anthropic)',
    elevenlabs: 'ElevenLabs',
    pexels: 'Pexels',
  }
  return labels[provider] || provider
}

const operationLabel = (operation) => {
  const labels = {
    llm_completion: 'Roteiro/Texto (LLM)',
    image_generation: 'Geração de Imagem',
    tts: 'Narração (TTS)',
    stock_media: 'Banco de Mídia',
  }
  return labels[operation] || operation
}

const goBackToChannels = () => {
  activeChannel.value = null
  activeVideo.value = null
  showBookendsPage.value = false
  stopVideoListPolling()
  stopChannelPolling()
}

const toLocalDateTimeInput = (isoString) => {
  if (!isoString) return ''
  const d = new Date(isoString)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

const selectVideo = async (video) => {
  stopVideoListPolling()
  try {
    const res = await apiFetch(`/videos/${video.id}`)
    if (res.ok) {
      activeVideo.value = await res.json()
      // Format in local timezone so <input type="datetime-local"> doesn't shift UTC
      scheduleDate.value = toLocalDateTimeInput(activeVideo.value.scheduled_for)

      // Start polling if video is being generated/rendered/published
      if (isBusyStatus(activeVideo.value.status)) {
        startRenderPolling(activeVideo.value.id)
      } else {
        stopRenderPolling()
      }
    }
  } catch (err) {
    console.error('Error fetching video:', err)
  }
}

const goBackToChannel = () => {
  activeVideo.value = null
  showBookendsPage.value = false
  stopRenderPolling()
  if (activeChannel.value) {
    fetchVideos(activeChannel.value.id)
    fetchChannelCosts(activeChannel.value.id)
    fetchAutoPilotStatus(activeChannel.value.id)
  }
}

// Actions
const submitNewChannel = async (payload) => {
  isGeneratingChannel.value = true
  try {
    const res = await apiFetch('/channels', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    })
    const data = await readJSON(res)
    if (!res.ok) {
      alert('Erro ao conceber canal: ' + errorText(res, data))
      return false
    }
    const created = data.channel || data
    await fetchChannels()
    await selectChannel(created)
    if (data.warning) {
      alert('Canal criado, mas parte da identidade visual falhou:\n' + data.warning + '\n\nVocê pode regerar o logo/capa na página do canal.')
    }
    return true
  } catch (err) {
    alert('Erro ao gerar canal por IA. Certifique-se de que o backend está ativo.')
    return false
  } finally {
    isGeneratingChannel.value = false
  }
}

const createAutoChannel = () => submitNewChannel({ name: '', niche: 'Curiosidades e Mistérios' })

const createChannel = async () => {
  const ok = await submitNewChannel({ name: newChannel.name.trim(), niche: newChannel.niche })
  if (ok) {
    showCreateChannelModal.value = false
    newChannel.name = ''
  }
}

const regenerateChannelImage = async (kind) => {
  if (!activeChannel.value) return
  const loading = kind === 'logo' ? isGeneratingLogo : isGeneratingBanner
  loading.value = true
  try {
    const res = await apiFetch(`/channels/${activeChannel.value.id}/generate-${kind}`, { method: 'POST' })
    const data = await readJSON(res)
    if (res.ok) {
      activeChannel.value = data
      syncChannelInList(data)
    } else {
      alert(`Erro ao gerar ${kind === 'logo' ? 'o logotipo' : 'a capa do canal'}: ` + errorText(res, data))
    }
  } catch (err) {
    alert('Erro de conexão ao gerar a imagem: ' + err.message)
  } finally {
    loading.value = false
  }
}

const generateLogo = () => regenerateChannelImage('logo')
const generateBanner = () => regenerateChannelImage('banner')

const bookendState = {
  intro: { loading: isGeneratingIntro, deleting: isDeletingIntro, input: introScriptInput, label: 'a abertura' },
  outro: { loading: isGeneratingOutro, deleting: isDeletingOutro, input: outroScriptInput, label: 'o encerramento' },
}

const generateBookend = async (kind) => {
  if (!activeChannel.value) return
  const st = bookendState[kind]
  st.loading.value = true
  try {
    const res = await apiFetch(`/channels/${activeChannel.value.id}/generate-${kind}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ script: st.input.value }),
    })
    const data = await readJSON(res)
    if (res.ok) {
      activeChannel.value = data
      syncChannelInList(data)
      startChannelPolling(data.id)
    } else {
      alert(`Erro ao gerar ${st.label}: ` + errorText(res, data))
    }
  } catch (err) {
    alert(`Erro de conexão ao gerar ${st.label}.`)
  } finally {
    st.loading.value = false
  }
}

const deleteBookend = async (kind) => {
  const st = bookendState[kind]
  if (!activeChannel.value || !confirm(`Tem certeza que deseja excluir ${st.label} deste canal?`)) return
  st.deleting.value = true
  try {
    const res = await apiFetch(`/channels/${activeChannel.value.id}/${kind}`, { method: 'DELETE' })
    const data = await readJSON(res)
    if (res.ok) {
      activeChannel.value = data
      syncChannelInList(data)
      st.input.value = ''
    } else {
      alert(`Erro ao excluir ${st.label}: ` + errorText(res, data))
    }
  } catch (err) {
    alert(`Erro de conexão ao excluir ${st.label}.`)
  } finally {
    st.deleting.value = false
  }
}

const generateIntro = () => generateBookend('intro')
const generateOutro = () => generateBookend('outro')
const deleteIntro = () => deleteBookend('intro')
const deleteOutro = () => deleteBookend('outro')

// Real extension of a stored file (logos are PNG, banners JPEG) so the
// downloaded file isn't misnamed.
const fileExt = (path, fallback) => {
  const m = /\.([a-z0-9]{2,4})(?:\?|$)/i.exec(path || '')
  return m ? `.${m[1].toLowerCase()}` : fallback
}

const slugify = (text, fallback) => (text || fallback).toLowerCase().normalize('NFD').replace(/[̀-ͯ]/g, '').replace(/[^a-z0-9]+/g, '_').replace(/^_+|_+$/g, '') || fallback

const downloadMediaFile = async (path, defaultFilename) => {
  if (!path) return
  try {
    const fullUrl = formatRelativePath(path)
    const response = await fetch(fullUrl)
    // Without this check a 404/502 page was silently "downloaded" as the file.
    if (!response.ok) throw new Error(`HTTP ${response.status}`)
    const blob = await response.blob()
    const blobUrl = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = blobUrl
    a.download = defaultFilename || 'download'
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(blobUrl)
  } catch (err) {
    const a = document.createElement('a')
    a.href = formatRelativePath(path)
    a.target = '_blank'
    a.download = defaultFilename || 'download'
    a.click()
  }
}

const generateVideo = async () => {
  if (!activeChannel.value) return
  isGeneratingVideo.value = true
  try {
    const res = await apiFetch(`/channels/${activeChannel.value.id}/videos`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        topic: newVideoTopic.value
      })
    })
    const data = await res.json().catch(() => ({}))
    if (res.ok) {
      newVideoTopic.value = ''
      await fetchVideos(activeChannel.value.id)
      selectVideo(data)
    } else {
      alert('Erro ao gerar roteiro: ' + (data.error || `HTTP ${res.status}`))
    }
  } catch (err) {
    alert('Erro ao gerar o roteiro. Tente novamente.')
  } finally {
    isGeneratingVideo.value = false
  }
}

const toggleAutoPilot = async () => {
  if (!activeChannel.value) return
  try {
    const res = await apiFetch(`/channels/${activeChannel.value.id}/toggle-autopilot`, {
      method: 'POST'
    })
    const data = await readJSON(res)
    if (res.ok) {
      activeChannel.value.autopilot_enabled = data.autopilot_enabled
      syncChannelInList({ ...activeChannel.value })
      await fetchAutoPilotStatus(activeChannel.value.id)
    } else {
      alert('Erro ao alterar Piloto Automático: ' + (data.error || `HTTP ${res.status}`))
    }
  } catch (err) {
    console.error('Error toggling autopilot:', err)
    alert('Erro de conexão ao alterar Piloto Automático.')
  }
}

const isRendering = ref(false)

const triggerRender = async () => {
  if (!activeVideo.value || isRendering.value) return
  isRendering.value = true
  try {
    const res = await apiFetch(`/videos/${activeVideo.value.id}/render`, {
      method: 'POST'
    })
    const data = await readJSON(res)
    if (res.ok) {
      activeVideo.value.status = 'generating'
      activeVideo.value.render_progress = 0
      activeVideo.value.render_stage = 'Iniciando renderização...'
      activeVideo.value.error_message = ''
      startRenderPolling(activeVideo.value.id)
    } else {
      alert('Erro ao iniciar renderização: ' + errorText(res, data))
    }
  } catch (err) {
    console.error('Error triggering render:', err)
    alert('Erro de conexão ao iniciar renderização.')
  } finally {
    isRendering.value = false
  }
}

const stopRenderPolling = () => {
  if (renderPollingId.value) {
    clearInterval(renderPollingId.value)
    renderPollingId.value = null
  }
}

const startRenderPolling = (videoId) => {
  stopRenderPolling()
  renderPollingId.value = setInterval(async () => {
    if (!activeVideo.value || activeVideo.value.id !== videoId) {
      stopRenderPolling()
      return
    }
    try {
      const res = await apiFetch(`/videos/${videoId}`)
      if (res.ok) {
        const updated = await res.json()
        if (activeVideo.value && activeVideo.value.id === videoId) {
          activeVideo.value = updated
          if (!isBusyStatus(updated.status)) {
            stopRenderPolling()
            scheduleDate.value = toLocalDateTimeInput(updated.scheduled_for)
          }
        }
      } else if (res.status === 404) {
        stopRenderPolling()
      }
    } catch (err) {
      console.error('Error polling render status:', err)
    }
  }, 3000)
}

const scheduleVideo = async () => {
  if (!activeVideo.value) return
  if (!scheduleDate.value) {
    alert('Selecione uma data e hora para agendar a postagem.')
    return
  }
  const parsed = new Date(scheduleDate.value)
  if (Number.isNaN(parsed.getTime())) {
    alert('Data ou hora inválida.')
    return
  }
  try {
    const res = await apiFetch(`/videos/${activeVideo.value.id}/schedule`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        scheduled_for: parsed.toISOString()
      })
    })
    const data = await readJSON(res)
    if (res.ok) {
      activeVideo.value = data
      scheduleDate.value = toLocalDateTimeInput(data.scheduled_for)
      alert('Vídeo agendado com sucesso!')
    } else {
      alert('Erro ao agendar o vídeo: ' + errorText(res, data))
    }
  } catch (err) {
    alert('Erro ao agendar o vídeo.')
  }
}

const unscheduleVideo = async () => {
  if (!activeVideo.value || !confirm('Cancelar o agendamento desta publicação?')) return
  const res = await apiFetch(`/videos/${activeVideo.value.id}/unschedule`, { method: 'POST' })
  const data = await readJSON(res)
  if (res.ok) {
    activeVideo.value = data
    scheduleDate.value = ''
  } else {
    alert('Erro ao cancelar o agendamento: ' + errorText(res, data))
  }
}

const deleteVideo = async (video, event) => {
  if (event) event.stopPropagation()
  if (!confirm(`Excluir o vídeo "${video.title}" e todos os seus arquivos (imagens, áudio, vídeo renderizado)? Essa ação não pode ser desfeita.`)) return
  try {
    const res = await apiFetch(`/videos/${video.id}`, { method: 'DELETE' })
    const data = await readJSON(res)
    if (res.ok) {
      if (activeVideo.value && activeVideo.value.id === video.id) {
        goBackToChannel()
      } else if (activeChannel.value) {
        await fetchVideos(activeChannel.value.id)
      }
    } else {
      alert('Erro ao excluir o vídeo: ' + errorText(res, data))
    }
  } catch (err) {
    alert('Erro ao excluir o vídeo.')
  }
}

// --- Settings (operator API keys, stored in the backend) ----------------
const settingsStatus = reactive({
  openai_configured: false,
  gemini_configured: false,
  elevenlabs_configured: false,
  pexels_configured: false,
  youtube_configured: false,
  tiktok_configured: false,
  groq_configured: false,
  claude_configured: false,
  redirect_uri: '',
})

// --- Change password ----------------------------------------------------
const passwordForm = reactive({ current: '', next: '', confirm: '' })
const isChangingPassword = ref(false)

const changePassword = async () => {
  if (passwordForm.next.length < 8) {
    alert('A nova senha precisa ter pelo menos 8 caracteres.')
    return
  }
  if (passwordForm.next !== passwordForm.confirm) {
    alert('A confirmação não confere com a nova senha.')
    return
  }
  isChangingPassword.value = true
  try {
    const res = await apiFetch('/auth/password', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ current_password: passwordForm.current, new_password: passwordForm.next }),
    })
    const data = await readJSON(res)
    if (res.ok) {
      passwordForm.current = passwordForm.next = passwordForm.confirm = ''
      alert('Senha alterada com sucesso.')
    } else {
      alert('Erro ao alterar a senha: ' + errorText(res, data))
    }
  } finally {
    isChangingPassword.value = false
  }
}
const settingsForm = reactive({
  openai_api_key: '',
  gemini_api_key: '',
  elevenlabs_api_key: '',
  pexels_api_key: '',
  youtube_client_id: '',
  youtube_client_secret: '',
  tiktok_client_id: '',
  tiktok_client_secret: '',
})
const isSavingSettings = ref(false)

// --- API connection test buttons (Settings tab) --------------------------
const apiProviders = [
  { key: 'gemini', label: 'Google Gemini' },
  { key: 'openai', label: 'OpenAI' },
  { key: 'elevenlabs', label: 'ElevenLabs' },
  { key: 'youtube', label: 'YouTube' },
  { key: 'tiktok', label: 'TikTok' },
  { key: 'groq', label: 'Groq (LLM principal)' },
  { key: 'claude', label: 'Claude (Anthropic)' },
  { key: 'pexels', label: 'Pexels (mídia stock)' },
  { key: 'pollinations', label: 'Pollinations (imagens de cena)' },
  { key: 'shotstack', label: 'Shotstack (render em nuvem)' },
  { key: 'r2', label: 'Cloudflare R2 (armazenamento)' },
  { key: 'image_ranker', label: 'Image Ranker (seleção de mídia)' },
]
const apiTestResults = reactive({})
apiProviders.forEach(p => { apiTestResults[p.key] = { loading: false, ok: null, message: '' } })

const testProvider = async (key) => {
  apiTestResults[key] = { loading: true, ok: null, message: '' }
  try {
    const res = await apiFetch(`/settings/test/${key}`)
    const data = await res.json()
    apiTestResults[key] = { loading: false, ok: !!data.ok, message: data.message || (res.ok ? 'OK' : 'Erro desconhecido') }
  } catch (err) {
    apiTestResults[key] = { loading: false, ok: false, message: 'Falha ao contatar o backend: ' + err.message }
  }
}

const testAllProviders = async () => {
  for (const p of apiProviders) {
    await testProvider(p.key)
  }
}

const fetchSettings = async () => {
  try {
    const res = await apiFetch('/settings')
    if (res.ok) {
      Object.assign(settingsStatus, await res.json())
    }
  } catch (err) {
    console.error('Error fetching settings:', err)
  }
}

const saveSettings = async () => {
  isSavingSettings.value = true
  try {
    const res = await apiFetch('/settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(settingsForm)
    })
    if (res.ok) {
      Object.keys(settingsForm).forEach(k => { settingsForm[k] = '' })
      await fetchSettings()
      alert('Chaves de API atualizadas no backend!')
    } else {
      alert('Erro ao salvar as chaves: ' + errorText(res, await readJSON(res)))
    }
  } catch (err) {
    alert('Erro ao salvar as chaves.')
  } finally {
    isSavingSettings.value = false
  }
}

// Calendar: one request for the visible month (the backend joins channel
// names), instead of downloading every channel's full video list - with
// every scene - just to find the scheduled ones.
const allScheduledVideos = ref([])
const calendarMonth = ref(new Date(new Date().getFullYear(), new Date().getMonth(), 1))
const isLoadingCalendar = ref(false)

const fetchAllVideosForCalendar = async () => {
  const from = calendarMonth.value
  const to = new Date(from.getFullYear(), from.getMonth() + 1, 1)
  isLoadingCalendar.value = true
  try {
    const res = await apiFetch(`/calendar?from=${encodeURIComponent(from.toISOString())}&to=${encodeURIComponent(to.toISOString())}`)
    if (res.ok) {
      allScheduledVideos.value = await res.json()
    }
  } catch (err) {
    console.error('Error loading calendar videos:', err)
  } finally {
    isLoadingCalendar.value = false
  }
}

const shiftCalendarMonth = async (delta) => {
  const m = calendarMonth.value
  calendarMonth.value = new Date(m.getFullYear(), m.getMonth() + delta, 1)
  await fetchAllVideosForCalendar()
}

const goToCalendarToday = async () => {
  const now = new Date()
  calendarMonth.value = new Date(now.getFullYear(), now.getMonth(), 1)
  await fetchAllVideosForCalendar()
}

// The day a post belongs to: when it went out if published, else its slot.
const calendarPostDate = (v) => new Date(v.posted_at || v.scheduled_for)

const calendarDays = computed(() => {
  const days = []
  const today = new Date()
  const year = calendarMonth.value.getFullYear()
  const month = calendarMonth.value.getMonth()

  const startDayOfWeek = new Date(year, month, 1).getDay() // 0 = Sunday
  const totalDays = new Date(year, month + 1, 0).getDate()

  for (let i = 0; i < startDayOfWeek; i++) {
    days.push({ dayNum: null, date: null, posts: [] })
  }

  for (let day = 1; day <= totalDays; day++) {
    const posts = allScheduledVideos.value.filter(v => {
      const d = calendarPostDate(v)
      return d.getDate() === day && d.getMonth() === month && d.getFullYear() === year
    })
    days.push({
      dayNum: day,
      date: new Date(year, month, day),
      // Must compare month/year too - with month navigation "the 13th" of
      // every month used to be highlighted as today.
      isToday: day === today.getDate() && month === today.getMonth() && year === today.getFullYear(),
      posts
    })
  }

  return days
})

const getMonthName = () => {
  const months = [
    'Janeiro', 'Fevereiro', 'Março', 'Abril', 'Maio', 'Junho',
    'Julho', 'Agosto', 'Setembro', 'Outubro', 'Novembro', 'Dezembro'
  ]
  return months[calendarMonth.value.getMonth()] + ' ' + calendarMonth.value.getFullYear()
}

const openCalendarPost = async (post) => {
  let ch = channels.value.find(c => c.id === post.channel_id)
  if (!ch) {
    await fetchChannels()
    ch = channels.value.find(c => c.id === post.channel_id)
  }
  if (!ch) return
  currentTab.value = 'channels'
  await selectChannel(ch)
  await selectVideo(post)
}

const changeTab = async (tab) => {
  currentTab.value = tab
  if (tab === 'calendar') {
    await fetchAllVideosForCalendar()
  } else if (tab === 'settings') {
    await fetchSettings()
  }
}

const formatRelativePath = (path) => {
  if (!path) return ''
  // Already an absolute URL (e.g. served from the R2 bucket) - use as-is
  if (/^https?:\/\//i.test(path)) return path
  // Normalize Windows backslashes and make url complete for the local-disk fallback
  const normalized = path.replace(/\\/g, '/')
  return `${MEDIA_BASE}/${normalized.replace(/^(\.?\/)?media\//, '')}`
}

const formatNicheLabel = (niche) => {
  if (!niche) return ''
  return niche.substring(0, 1).toUpperCase() + niche.substring(1)
}

const getStatusBadgeClass = (status) => {
  return `badge badge-${status}`
}

// The UI is Portuguese; raw status codes ("draft", "ready"...) leaked into it.
const STATUS_LABELS = {
  generating: 'Gerando',
  draft: 'Rascunho',
  ready: 'Pronto',
  scheduled: 'Agendado',
  posting: 'Publicando',
  posted: 'Publicado',
  failed: 'Falhou',
}
const statusLabel = (status) => STATUS_LABELS[status] || status

// error_message on a posted video holds a non-fatal note (e.g. TikTok failed
// but YouTube worked) - it shouldn't look like a fatal error.
const isWarningOnly = (video) => video && (video.status === 'posted' || video.status === 'ready' || video.status === 'scheduled')

// Re-render is only possible while the scene source files still exist (they
// are purged after a successful render to save storage).
const canRender = (video) => video && (video.status === 'draft' || video.status === 'failed') && (video.scenes || []).some(s => s.image_path || s.video_clip_url || s.audio_path)
</script>

<template>
  <!-- Auth Gate -->
  <div v-if="!authToken" class="auth-screen">
    <div class="card auth-card">
      <div class="brand" style="justify-content: center; margin-bottom: 1.5rem;">
        <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3">
          <path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
        Mákina <span>Dark</span>
      </div>

      <div v-if="registrationEnabled" style="display: flex; gap: 0.5rem; margin-bottom: 1.5rem;">
        <button type="button" class="btn" :class="authMode === 'login' ? 'btn-primary' : 'btn-secondary'" style="flex: 1;" @click="authMode = 'login'; authError = ''">Entrar</button>
        <button type="button" class="btn" :class="authMode === 'register' ? 'btn-primary' : 'btn-secondary'" style="flex: 1;" @click="authMode = 'register'; authError = ''">Criar Conta</button>
      </div>

      <form @submit.prevent="submitAuth">
        <div class="form-group" v-if="authMode === 'register'">
          <label class="form-label">Nome</label>
          <input v-model="authForm.name" type="text" class="form-input" placeholder="Seu nome" />
        </div>
        <div class="form-group">
          <label class="form-label">Email</label>
          <input v-model="authForm.email" type="email" required class="form-input" placeholder="voce@exemplo.com" />
        </div>
        <div class="form-group">
          <label class="form-label">Senha</label>
          <input v-model="authForm.password" type="password" required minlength="8" class="form-input" placeholder="Mínimo 8 caracteres" />
        </div>
        <p v-if="authError" style="color: #f87171; font-size: 0.85rem; margin-bottom: 1rem;">{{ authError }}</p>
        <button type="submit" :disabled="isAuthenticating" class="btn btn-primary" style="width: 100%;">
          <span v-if="isAuthenticating" class="spinner"></span>
          <span v-else>{{ authMode === 'login' ? 'Entrar' : 'Criar Conta' }}</span>
        </button>
      </form>
    </div>
  </div>

  <!-- Main App -->
  <div v-else class="app-container">
    <!-- Sidebar Navigation -->
    <aside class="sidebar">
      <div class="brand">
        <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3">
          <path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
        Mákina <span>Dark</span>
      </div>

      <nav style="flex-grow: 1;">
        <ul class="nav-menu">
          <li>
            <a @click="changeTab('channels')" class="nav-item" :class="{ active: currentTab === 'channels' }">
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <rect x="3" y="3" width="7" height="9" rx="1"/>
                <rect x="14" y="3" width="7" height="5" rx="1"/>
                <rect x="14" y="12" width="7" height="9" rx="1"/>
                <rect x="3" y="16" width="7" height="5" rx="1"/>
              </svg>
              Canais Dark
            </a>
          </li>
          <li>
            <a @click="changeTab('calendar')" class="nav-item" :class="{ active: currentTab === 'calendar' }">
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <rect x="3" y="4" width="18" height="18" rx="2" ry="2"/>
                <line x1="16" y1="2" x2="16" y2="6"/>
                <line x1="8" y1="2" x2="8" y2="6"/>
                <line x1="3" y1="10" x2="21" y2="10"/>
              </svg>
              Cronograma
            </a>
          </li>
          <li>
            <a @click="changeTab('settings')" class="nav-item" :class="{ active: currentTab === 'settings' }">
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <circle cx="12" cy="12" r="3"/>
                <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/>
              </svg>
              Configurações
            </a>
          </li>
        </ul>
      </nav>

      <div style="border-top: 1px solid var(--border-color); padding-top: 1rem; text-align: center;">
        <p v-if="currentUserName" style="font-size: 0.8rem; color: var(--text-secondary); margin-bottom: 0.5rem;">{{ currentUserName }}</p>
        <button type="button" class="btn btn-secondary" style="width: 100%; font-size: 0.8rem; margin-bottom: 0.75rem;" @click="logout">Sair</button>
        <p style="font-size: 0.75rem; color: var(--text-muted);">Mákina Dark Engine v1.0</p>
      </div>
    </aside>

    <!-- Main Content Area -->
    <main class="main-content">
      
      <!-- Tab 1: Channels Management -->
      <section v-if="currentTab === 'channels'">
        <!-- Channels Main View -->
        <div v-if="!activeChannel">
          <div class="flex-wrap-header">
            <div>
              <h1>Canais Gerados</h1>
              <p style="color: var(--text-secondary);">Cadastre e gerencie a identidade visual e o conteúdo de cada canal.</p>
            </div>
            <div style="display: flex; flex-wrap: wrap; gap: 0.75rem;">
              <button @click="createAutoChannel" :disabled="isGeneratingChannel" class="btn btn-primary" style="background: linear-gradient(135deg, #06b6d4 0%, #3b82f6 100%); border: none; box-shadow: 0 4px 20px rgba(6, 182, 212, 0.4);">
                <span v-if="isGeneratingChannel" class="spinner"></span>
                <span v-else>🚀 Gerar Canal 100% Automático (Sem Pensar)</span>
              </button>
              <button @click="showCreateChannelModal = true" class="btn btn-secondary">
                + Personalizar Nicho
              </button>
            </div>
          </div>

          <div class="grid-cols-3" v-if="channels.length > 0">
            <div v-for="ch in channels" :key="ch.id" @click="selectChannel(ch)" class="card card-clickable" style="display: flex; flex-direction: column; gap: 1rem;">
              <div style="display: flex; align-items: center; gap: 1rem;">
                <div v-if="ch.logo_path" class="channel-logo-avatar" :style="{ backgroundImage: `url(${formatRelativePath(ch.logo_path)})` }"></div>
                <div v-else class="channel-logo-avatar-placeholder">{{ (ch.name || 'CH').substring(0, 2).toUpperCase() }}</div>
                <div style="min-width: 0;">
                  <h3 style="font-weight: 700; margin-bottom: 0.25rem; word-break: break-word;">{{ ch.name }}</h3>
                  <span class="badge" style="background: rgba(139, 92, 246, 0.15); color: var(--accent-purple);">{{ ch.niche }}</span>
                </div>
              </div>
              <p style="font-size: 0.85rem; color: var(--text-secondary); line-height: 1.4; display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; flex-grow: 1;">
                {{ ch.concept }}
              </p>
              <div style="border-top: 1px solid var(--border-color); padding-top: 0.75rem; display: flex; justify-content: space-between; font-size: 0.75rem; color: var(--text-muted);">
                <span>Criado em {{ new Date(ch.created_at).toLocaleDateString() }}</span>
                <span>Configurar →</span>
              </div>
            </div>
          </div>

          <div v-else class="card" style="text-align: center; padding: 4rem 2rem;">
            <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="var(--text-muted)" stroke-width="1.5" style="margin-bottom: 1rem;">
              <circle cx="12" cy="12" r="10"/><line x1="8" y1="12" x2="16" y2="12"/><line x1="12" y1="8" x2="12" y2="16"/>
            </svg>
            <h3 style="margin-bottom: 0.5rem;">Nenhum canal cadastrado</h3>
            <p style="color: var(--text-muted); max-width: 400px; margin: 0 auto 1.5rem;">Crie seu primeiro canal dark agora. A inteligência artificial irá criar a identidade e conceito do zero.</p>
            <button @click="showCreateChannelModal = true" class="btn btn-primary">Criar Canal por IA</button>
          </div>
        </div>

        <!-- Channel Detail View (Main Overview) -->
        <div v-else-if="activeChannel && !showBookendsPage && !activeVideo">
          <div style="display: flex; justify-content: space-between; align-items: center; gap: 1rem; margin-bottom: 1.5rem;">
            <div style="display: flex; align-items: center; gap: 0.5rem; color: var(--text-secondary); cursor: pointer;" @click="goBackToChannels">
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <line x1="19" y1="12" x2="5" y2="12"/><polyline points="12 19 5 12 12 5"/>
              </svg>
              Voltar para canais
            </div>
            <button type="button" @click="deleteChannel" class="btn btn-secondary" style="font-size: 0.8rem; color: #f87171; border-color: rgba(248, 113, 113, 0.3);">
              Excluir Canal
            </button>
          </div>

          <!-- Channel Branding Banner Card -->
          <div class="channel-banner-preview" :style="{ backgroundImage: activeChannel.banner_path ? `url(${formatRelativePath(activeChannel.banner_path)})` : 'linear-gradient(135deg, #1e1b4b 0%, #311042 100%)' }">
            <div class="channel-logo-overlay" :style="{ backgroundImage: activeChannel.logo_path ? `url(${formatRelativePath(activeChannel.logo_path)})` : 'none' }">
              <span v-if="!activeChannel.logo_path" style="display:flex; height:100%; align-items:center; justify-content:center; font-weight:800; font-size:1.5rem; color:var(--text-muted);">
                {{ (activeChannel.name || 'CH').substring(0, 2).toUpperCase() }}
              </span>
            </div>
          </div>

          <!-- Auto-Pilot health warning: previously a channel stuck failing every 10-minute
               cycle gave no sign of it anywhere in the app, only in server logs. -->
          <div v-if="autoPilotStatus && autoPilotStatus.consecutive_failures > 0"
               class="card" style="margin-top: 1rem; padding: 1rem 1.25rem; border: 1px solid rgba(239, 68, 68, 0.35); background: rgba(239, 68, 68, 0.08);">
            <div style="display: flex; gap: 0.6rem; align-items: flex-start;">
              <span style="font-size: 1.1rem; line-height: 1;">🚫</span>
              <div>
                <strong style="color: var(--danger); font-size: 0.85rem;">
                  Auto-Pilot falhou {{ autoPilotStatus.consecutive_failures }} {{ autoPilotStatus.consecutive_failures === 1 ? 'vez seguida' : 'vezes seguidas' }} neste canal
                </strong>
                <p style="margin: 0.2rem 0 0; font-size: 0.85rem; color: var(--text-secondary);">
                  {{ autoPilotStatus.last_failure_reason || 'Motivo não registrado.' }}
                  <span v-if="autoPilotStatus.last_failure_at"> (última tentativa: {{ new Date(autoPilotStatus.last_failure_at).toLocaleString() }})</span>
                </p>
              </div>
            </div>
          </div>

          <div class="channel-overview-layout">
            <div style="min-width: 0;">
              <h1 style="font-size: 2rem;">{{ activeChannel.name }}</h1>
              <p style="color: var(--accent-purple); font-weight: 600; margin-bottom: 0.5rem;">Nicho: {{ formatNicheLabel(activeChannel.niche) }}</p>
              <p style="color: var(--text-secondary); font-size: 0.95rem; line-height: 1.5;">{{ activeChannel.concept }}</p>

              <!-- AI Branding & Config Badges Box -->
              <div style="margin-top: 1.25rem; background: rgba(15, 23, 42, 0.6); border: 1px solid var(--border-color); padding: 1.25rem; border-radius: 14px;">
                <h4 style="font-weight: 700; font-size: 0.95rem; margin-bottom: 1rem; color: var(--accent-cyan); display: flex; align-items: center; gap: 0.5rem;">
                  ⚙️ Configurações & Persona Escolhidas pela IA
                </h4>
                <div class="channel-persona-grid">
                  <div style="background: rgba(255,255,255,0.03); padding: 0.75rem 1rem; border-radius: 10px; border: 1px solid rgba(255,255,255,0.05);">
                    <span style="font-size: 0.75rem; color: var(--text-muted); display: block; margin-bottom: 0.25rem;">Persona do Canal</span>
                    <strong style="font-size: 0.85rem; color: #f4f4f5;">{{ activeChannel.persona || 'Investigador Enigmático' }}</strong>
                  </div>
                  <div style="background: rgba(255,255,255,0.03); padding: 0.75rem 1rem; border-radius: 10px; border: 1px solid rgba(255,255,255,0.05);">
                    <span style="font-size: 0.75rem; color: var(--text-muted); display: block; margin-bottom: 0.25rem;">Tipo de Voz (Vocal)</span>
                    <strong style="font-size: 0.85rem; color: #f4f4f5;">{{ activeChannel.voice_type || 'Onyx (Masculina Grave)' }}</strong>
                  </div>
                  <div style="background: rgba(255,255,255,0.03); padding: 0.75rem 1rem; border-radius: 10px; border: 1px solid rgba(255,255,255,0.05);">
                    <span style="font-size: 0.75rem; color: var(--text-muted); display: block; margin-bottom: 0.25rem;">Tom de Narração</span>
                    <strong style="font-size: 0.85rem; color: #f4f4f5;">{{ activeChannel.narration_tone || 'Dramático e Pausado' }}</strong>
                  </div>
                </div>
              </div>

              <!-- Narrador do Canal: picks the ElevenLabs voice used for every
                   scene's narration audio going forward (see generateElevenLabsTTS). -->
              <div style="margin-top: 1rem; background: rgba(15, 23, 42, 0.6); border: 1px solid var(--border-color); padding: 1.25rem; border-radius: 14px;">
                <label for="narrator-voice-select" style="font-weight: 700; font-size: 0.95rem; margin-bottom: 0.6rem; color: var(--accent-cyan); display: flex; align-items: center; gap: 0.5rem;">
                  🎙️ Narrador do Canal
                </label>
                <select id="narrator-voice-select" v-model="narratorVoiceSelection" @change="updateNarratorVoice" class="form-input" style="width: 100%;">
                  <option value="">Padrão do sistema (Adam)</option>
                  <option v-for="voice in narratorVoices" :key="voice.voice_id" :value="voice.voice_id">
                    {{ voice.name }}
                  </option>
                </select>
                <p v-if="narratorVoiceSaved" style="font-size: 0.78rem; color: var(--success); margin-top: 0.4rem;">Narrador salvo — os próximos vídeos usarão essa voz.</p>
              </div>

              <!-- Auto-Pilot Banner -->
              <div class="channel-bar-row" style="margin-top: 1.5rem; background: rgba(139, 92, 246, 0.08); border: 1px solid rgba(139, 92, 246, 0.2); padding: 1rem 1.25rem; border-radius: 14px;">
                <div style="flex: 1; min-width: 220px;">
                  <h4 style="font-weight: 700; font-size: 0.95rem; color: var(--text-primary); display: flex; align-items: center; gap: 0.5rem;">
                    <span style="width: 8px; height: 8px; border-radius: 50%;" :style="{ background: activeChannel.autopilot_enabled ? 'var(--success)' : 'var(--text-muted)' }"></span>
                    Piloto Automático por IA (Auto-Pilot)
                  </h4>
                  <p style="font-size: 0.8rem; color: var(--text-secondary); margin-top: 0.25rem;">
                    {{ activeChannel.autopilot_enabled ? 'Ativo: a IA escolhe temas inéditos em alta, gera roteiro, narração e cenas, renderiza e agenda as postagens distribuídas ao longo da semana (máx. 2 vídeos na fila).' : 'Inativo: ative para a IA gerenciar a linha editorial e publicar vídeos no automático.' }}
                  </p>
                  <p v-if="activeChannel.autopilot_enabled && autoPilotStatus && autoPilotStatus.can_publish === false" style="font-size: 0.8rem; color: var(--warning); margin-top: 0.4rem; font-weight: 600;">
                    ⚠️ Pausado até você conectar o YouTube ou o TikTok abaixo - sem conta vinculada nenhum vídeo é produzido (para não gastar créditos à toa).
                  </p>
                  <p v-else-if="activeChannel.autopilot_enabled && autoPilotStatus && autoPilotStatus.last_skip_reason" style="font-size: 0.78rem; color: var(--text-muted); margin-top: 0.4rem;">
                    Última verificação: {{ autoPilotStatus.last_skip_reason }}
                  </p>
                </div>
                <button @click="toggleAutoPilot" class="btn" :class="activeChannel.autopilot_enabled ? 'btn-primary' : 'btn-secondary'" style="flex-shrink: 0;">
                  {{ activeChannel.autopilot_enabled ? 'Piloto Ativo' : 'Ativar Piloto Automático' }}
                </button>
              </div>

              <!-- Channel OAuth Integrations Bar -->
              <div class="channel-bar-row" style="margin-top: 1rem; background: var(--bg-secondary); border: 1px solid var(--border-color); padding: 1rem 1.25rem; border-radius: 14px;">
                <div style="flex: 1; min-width: 220px;">
                  <h4 style="font-weight: 700; font-size: 0.95rem; color: var(--text-primary);">Contas de Postagem Vinculadas</h4>
                  <p style="font-size: 0.8rem; color: var(--text-secondary); margin-top: 0.25rem;">
                    Conecte a conta do YouTube e do TikTok deste canal para habilitar postagens automáticas.
                  </p>
                </div>
                <div style="display: flex; flex-wrap: wrap; gap: 0.75rem; flex-shrink: 0;">
                  <button v-for="acc in [{ key: 'youtube', label: 'YouTube', connected: activeChannel.yt_connected }, { key: 'tiktok', label: 'TikTok', connected: activeChannel.tiktok_connected }]"
                          :key="acc.key" type="button" class="btn btn-secondary" style="font-size: 0.85rem;"
                          :disabled="connectingProvider === acc.key"
                          :title="acc.connected ? 'Clique para desconectar' : ''"
                          @click="acc.connected ? disconnectAccount(acc.key) : connectAccount(acc.key)">
                    <span v-if="connectingProvider === acc.key" class="spinner" style="width: 14px; height: 14px;"></span>
                    <span v-else-if="acc.connected" style="color: var(--success);">✓ {{ acc.label }} Conectado</span>
                    <span v-else>+ Conectar {{ acc.label }}</span>
                  </button>
                </div>
              </div>

              <!-- Visual Identity Section (Logo & Banner / Capa with Download and Separate Generation Buttons) -->
              <div style="margin-top: 1.25rem; background: var(--bg-secondary); border: 1px solid var(--border-color); padding: 1.25rem; border-radius: 14px;">
                <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 1rem;">
                  <div>
                    <h4 style="font-weight: 700; font-size: 0.95rem; color: var(--accent-cyan); display: flex; align-items: center; gap: 0.5rem;">
                      🎨 Identidade Visual (Logo & Capa do Canal)
                    </h4>
                    <p style="font-size: 0.8rem; color: var(--text-secondary); margin-top: 0.25rem;">
                      Gerencie, gere individualmente e baixe as imagens do canal para o seu computador.
                    </p>
                  </div>
                </div>

                <div class="channel-visual-grid">
                  <!-- Logo Card -->
                  <div style="background: rgba(15, 23, 42, 0.8); border: 1px solid var(--border-color); padding: 1rem; border-radius: 12px; display: flex; flex-direction: column; align-items: center; justify-content: space-between; text-align: center;">
                    <div style="width: 100%;">
                      <span style="font-size: 0.75rem; color: var(--text-muted); display: block; margin-bottom: 0.75rem; font-weight: 600;">Logotipo do Canal</span>
                      <div style="width: 110px; height: 110px; margin: 0 auto 0.75rem auto; border-radius: 50%; overflow: hidden; border: 2px solid var(--border-color); background-size: cover; background-position: center; background-color: #1e1b4b;"
                           :style="{ backgroundImage: activeChannel.logo_path ? `url(${formatRelativePath(activeChannel.logo_path)})` : 'none' }">
                        <span v-if="!activeChannel.logo_path" style="display:flex; height:100%; align-items:center; justify-content:center; font-weight:800; font-size:1.5rem; color:var(--text-muted);">
                          {{ (activeChannel.name || 'CH').substring(0, 2).toUpperCase() }}
                        </span>
                      </div>
                    </div>

                    <div style="width: 100%; display: flex; flex-direction: column; gap: 0.5rem; margin-top: 0.5rem;">
                      <button @click="generateLogo" :disabled="isGeneratingLogo" class="btn btn-secondary" style="width: 100%; font-size: 0.8rem; display: flex; align-items: center; justify-content: center; gap: 0.4rem; border-color: var(--accent-cyan); color: var(--accent-cyan);">
                        <span v-if="isGeneratingLogo" class="spinner"></span>
                        <span v-else>🎨 {{ activeChannel.logo_path ? 'Regerar Logo' : 'Gerar Logo' }}</span>
                      </button>

                      <button v-if="activeChannel.logo_path" @click="downloadMediaFile(activeChannel.logo_path, `logo-${slugify(activeChannel.name, 'canal')}${fileExt(activeChannel.logo_path, '.png')}`)"
                              class="btn btn-primary" style="width: 100%; font-size: 0.8rem; display: flex; align-items: center; justify-content: center; gap: 0.4rem; background: linear-gradient(135deg, #06b6d4 0%, #3b82f6 100%); border: none;">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                          <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path>
                          <polyline points="7 10 12 15 17 10"></polyline>
                          <line x1="12" y1="15" x2="12" y2="3"></line>
                        </svg>
                        Baixar Logo
                      </button>
                    </div>
                  </div>

                  <!-- Banner / Capa Card -->
                  <div style="background: rgba(15, 23, 42, 0.8); border: 1px solid var(--border-color); padding: 1rem; border-radius: 12px; display: flex; flex-direction: column; justify-content: space-between; min-width: 0;">
                    <div>
                      <span style="font-size: 0.75rem; color: var(--text-muted); display: block; margin-bottom: 0.75rem; font-weight: 600;">Capa do Canal (Banner 16:9)</span>
                      <div style="width: 100%; height: 130px; margin-bottom: 0.75rem; border-radius: 10px; overflow: hidden; border: 1px solid var(--border-color); background-size: cover; background-position: center; background-color: #1e1b4b;"
                           :style="{ backgroundImage: activeChannel.banner_path ? `url(${formatRelativePath(activeChannel.banner_path)})` : 'linear-gradient(135deg, #1e1b4b 0%, #311042 100%)' }">
                      </div>
                    </div>

                    <div style="display: flex; flex-wrap: wrap; justify-content: space-between; align-items: center; gap: 0.75rem; margin-top: 0.5rem;">
                      <button @click="generateBanner" :disabled="isGeneratingBanner" class="btn btn-secondary" style="font-size: 0.8rem; display: flex; align-items: center; gap: 0.4rem; border-color: var(--accent-cyan); color: var(--accent-cyan);">
                        <span v-if="isGeneratingBanner" class="spinner"></span>
                        <span v-else>🎨 {{ activeChannel.banner_path ? 'Regerar Capa' : 'Gerar Capa' }}</span>
                      </button>

                      <button v-if="activeChannel.banner_path" @click="downloadMediaFile(activeChannel.banner_path, `capa-${slugify(activeChannel.name, 'canal')}${fileExt(activeChannel.banner_path, '.jpg')}`)"
                              class="btn btn-primary" style="font-size: 0.8rem; display: flex; align-items: center; gap: 0.4rem; background: linear-gradient(135deg, #06b6d4 0%, #3b82f6 100%); border: none;">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                          <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path>
                          <polyline points="7 10 12 15 17 10"></polyline>
                          <line x1="12" y1="15" x2="12" y2="3"></line>
                        </svg>
                        Baixar Capa (Banner)
                      </button>
                    </div>
                  </div>
                </div>
              </div>

              <!-- Dedicated Link Card to open Bookends Page -->
              <div @click="openBookendsPage" class="card card-clickable channel-bar-row" style="margin-top: 1.25rem; background: linear-gradient(135deg, rgba(6, 182, 212, 0.08) 0%, rgba(139, 92, 246, 0.08) 100%); border: 1px solid rgba(6, 182, 212, 0.3); padding: 1.25rem; border-radius: 14px;">
                <div style="display: flex; align-items: center; gap: 1rem; flex: 1; min-width: 220px;">
                  <div style="width: 44px; height: 44px; flex-shrink: 0; border-radius: 12px; background: rgba(6, 182, 212, 0.15); display: flex; align-items: center; justify-content: center; font-size: 1.4rem;">
                    🎬
                  </div>
                  <div>
                    <h3 style="font-size: 1.05rem; font-weight: 700; color: #f4f4f5; display: flex; flex-wrap: wrap; align-items: center; gap: 0.6rem;">
                      Abertura & Encerramento do Canal
                      <span v-if="activeChannel.intro_video_path && activeChannel.outro_video_path" class="badge" style="background: rgba(16, 185, 129, 0.15); color: var(--success); font-size: 0.75rem;">✓ Prontos</span>
                      <span v-else-if="activeChannel.intro_video_path || activeChannel.outro_video_path" class="badge" style="background: rgba(245, 158, 11, 0.15); color: #f59e0b; font-size: 0.75rem;">Parcial</span>
                      <span v-else class="badge" style="background: rgba(239, 68, 68, 0.15); color: var(--danger); font-size: 0.75rem;">Não gerados</span>
                    </h3>
                    <p style="font-size: 0.82rem; color: var(--text-secondary); margin-top: 0.25rem;">
                      Abra a página dedicada para gerar, editar os roteiros por IA, assistir à prévia em vídeo e gerenciar a abertura e encerramento.
                    </p>
                  </div>
                </div>
                <button type="button" class="btn btn-primary" style="flex-shrink: 0; background: linear-gradient(135deg, #06b6d4 0%, #3b82f6 100%); border: none;">
                  Gerenciar Abertura e Encerramento →
                </button>
              </div>
            </div>
            
            <div class="card" style="width: 100%; padding: 1.25rem;">
              <h3 style="margin-bottom: 0.5rem; font-size: 1.1rem; font-weight: 700;">Criar Novo Vídeo</h3>
              <p style="font-size: 0.8rem; color: var(--text-secondary); margin-bottom: 1rem;">
                Deixe o tema em branco para que a IA busque no <strong>Google Trends</strong> um assunto inédito de alta retenção que <strong>NUNCA</strong> foi feito neste canal antes.
              </p>
              <div class="form-group">
                <label class="form-label">Tema do Vídeo (Opcional)</label>
                <input v-model="newVideoTopic" type="text" placeholder="Deixe em branco para usar Google Trends (Sem Repetir)" class="form-input" />
              </div>
              <button @click="generateVideo" :disabled="isGeneratingVideo" class="btn btn-primary" style="width: 100%; background: linear-gradient(135deg, #8b5cf6 0%, #06b6d4 100%); border: none;">
                <span v-if="isGeneratingVideo" class="spinner"></span>
                <span v-else>⚡ Gerar Roteiro por Tendência (Sem Repetir)</span>
              </button>
            </div>
          </div>

          <!-- Cost Dashboard -->
          <div v-if="channelCosts" class="card" style="margin-bottom: 2.5rem;">
            <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 1.25rem;">
              <h2 style="font-size: 1.3rem; font-weight: 700;">💰 Gastos com APIs</h2>
              <div style="text-align: right;">
                <span style="font-size: 1.6rem; font-weight: 800; color: var(--accent-cyan);">{{ formatUSD(channelCosts.total_cost_usd) }}</span>
                <p style="font-size: 0.7rem; color: var(--text-muted);">total estimado neste canal</p>
              </div>
            </div>

            <div v-if="channelCosts.gemini_free_tier && channelCosts.gemini_free_tier.configured" style="margin-bottom: 1.25rem; padding: 0.75rem 1rem; border-radius: 10px; display: flex; justify-content: space-between; align-items: center;"
                 :style="{ background: channelCosts.gemini_free_tier.limit_reached ? 'rgba(248, 113, 113, 0.1)' : 'rgba(139, 92, 246, 0.08)', border: channelCosts.gemini_free_tier.limit_reached ? '1px solid rgba(248, 113, 113, 0.3)' : '1px solid rgba(139, 92, 246, 0.2)' }">
              <span style="font-size: 0.85rem;">
                Cota gratuita do Gemini hoje (todos os canais): <strong>{{ channelCosts.gemini_free_tier.requests_today }}/{{ channelCosts.gemini_free_tier.daily_limit }}</strong> requisições
              </span>
              <span v-if="channelCosts.gemini_free_tier.limit_reached" style="font-size: 0.75rem; color: #f87171; font-weight: 700;">
                LIMITE ATINGIDO - as chamadas seguem para o próximo provedor configurado (Claude/OpenAI são pagos)
              </span>
            </div>

            <div class="grid-cols-2" style="gap: 1.5rem;">
              <div>
                <h4 style="font-size: 0.85rem; font-weight: 700; margin-bottom: 0.75rem; color: var(--text-secondary);">Por API / Operação</h4>
                <div v-if="channelCosts.by_provider_operation && channelCosts.by_provider_operation.length" style="display: flex; flex-direction: column; gap: 0.5rem;">
                  <div v-for="row in channelCosts.by_provider_operation" :key="row.provider + row.operation" style="display: flex; justify-content: space-between; font-size: 0.8rem; padding: 0.5rem 0.75rem; background: var(--bg-tertiary); border-radius: 8px;">
                    <span>{{ providerLabel(row.provider) }} · {{ operationLabel(row.operation) }} <span style="color: var(--text-muted);">({{ row.call_count }}x)</span></span>
                    <strong>{{ formatUSD(row.total_cost_usd) }}</strong>
                  </div>
                </div>
                <p v-else style="font-size: 0.8rem; color: var(--text-muted);">Nenhum gasto registrado ainda.</p>
              </div>

              <div>
                <h4 style="font-size: 0.85rem; font-weight: 700; margin-bottom: 0.75rem; color: var(--text-secondary);">Por Vídeo</h4>
                <div v-if="channelCosts.by_video && channelCosts.by_video.length" style="display: flex; flex-direction: column; gap: 0.5rem; max-height: 220px; overflow-y: auto;">
                  <div v-for="row in channelCosts.by_video" :key="row.video_id" style="display: flex; justify-content: space-between; font-size: 0.8rem; padding: 0.5rem 0.75rem; background: var(--bg-tertiary); border-radius: 8px;">
                    <span style="overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 75%;">{{ row.title || row.video_id }}</span>
                    <strong>{{ formatUSD(row.total_cost_usd) }}</strong>
                  </div>
                </div>
                <p v-else style="font-size: 0.8rem; color: var(--text-muted);">Nenhum vídeo com gasto registrado ainda.</p>
              </div>
            </div>

            <div v-if="channelCosts.daily_last_30_days && channelCosts.daily_last_30_days.length" style="margin-top: 1.5rem;">
              <h4 style="font-size: 0.85rem; font-weight: 700; margin-bottom: 0.75rem; color: var(--text-secondary);">Por Dia (últimos 30 dias)</h4>
              <div style="display: flex; flex-direction: column; gap: 0.35rem; max-height: 180px; overflow-y: auto;">
                <div v-for="row in channelCosts.daily_last_30_days" :key="row.day + row.provider" style="display: flex; justify-content: space-between; font-size: 0.78rem; padding: 0.4rem 0.75rem; border-bottom: 1px solid var(--border-color);">
                  <span>{{ row.day }} · {{ providerLabel(row.provider) }} <span style="color: var(--text-muted);">({{ row.call_count }}x)</span></span>
                  <strong>{{ formatUSD(row.total_cost_usd) }}</strong>
                </div>
              </div>
            </div>

            <p style="font-size: 0.7rem; color: var(--text-muted); margin-top: 1rem;">
              Unidades (tokens/caracteres/imagens) são exatas, vindas da resposta de cada API. O valor em $ é uma estimativa baseada em tabelas de preço públicas e pode variar.
            </p>
          </div>

          <!-- Video Lists -->
          <h2 style="font-size: 1.5rem; margin-bottom: 1.5rem; font-weight: 700; border-bottom: 1px solid var(--border-color); padding-bottom: 0.5rem;">Vídeos do Canal</h2>
          
          <div class="grid-cols-2" v-if="videos.length > 0">
            <div v-for="v in videos" :key="v.id" @click="selectVideo(v)" class="card card-clickable" style="display: flex; align-items: center; gap: 1.25rem; padding: 1.25rem;">
              <div class="video-card-thumbnail" :style="{ backgroundImage: v.thumbnail_path ? `url(${formatRelativePath(v.thumbnail_path)})` : 'linear-gradient(135deg, #18181b 0%, #27272a 100%)' }">
                <div class="play-overlay">
                  <svg width="24" height="24" viewBox="0 0 24 24" fill="currentColor">
                    <polygon points="5 3 19 12 5 21 5 3"/>
                  </svg>
                </div>
              </div>
              
              <div style="flex-grow: 1; min-width: 0; display: flex; flex-direction: column; gap: 0.5rem;">
                <div style="display: flex; justify-content: space-between; align-items: flex-start; gap: 0.5rem;">
                  <h4 style="font-weight: 700; font-size: 1.05rem; display: -webkit-box; -webkit-line-clamp: 1; -webkit-box-orient: vertical; overflow: hidden;">{{ v.title }}</h4>
                  <span style="display: flex; align-items: center; gap: 0.4rem; flex-shrink: 0;">
                    <span v-if="v.total_scene_count > 0 && v.degraded_scene_count > 0"
                          :title="v.degraded_scene_count + ' de ' + v.total_scene_count + ' cenas usaram conteúdo de reserva'"
                          :style="{ fontSize: '0.7rem', color: (v.degraded_scene_count / v.total_scene_count > 0.3) ? 'var(--danger)' : 'var(--warning)' }">⚠️</span>
                    <span :class="getStatusBadgeClass(v.status)">{{ v.status === 'generating' ? `${v.render_progress || 0}%` : statusLabel(v.status) }}</span>
                  </span>
                </div>
                <p style="font-size: 0.8rem; color: var(--text-muted); display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;">
                  {{ v.description }}
                </p>
                <div style="display: flex; justify-content: space-between; gap: 0.5rem; flex-wrap: wrap; font-size: 0.75rem; color: var(--text-muted); margin-top: 0.5rem;">
                  <span>Cenas: {{ v.scene_count || v.total_scene_count || 0 }}</span>
                  <span v-if="v.posted_at">Publicado: {{ new Date(v.posted_at).toLocaleString() }}</span>
                  <span v-else-if="v.scheduled_for">Agendado: {{ new Date(v.scheduled_for).toLocaleString() }}</span>
                  <span v-else>{{ statusLabel(v.status) }}</span>
                </div>
              </div>
              <button @click="deleteVideo(v, $event)" class="btn btn-secondary" title="Excluir vídeo" style="flex-shrink: 0; padding: 0.5rem; color: #f87171; border-color: rgba(248, 113, 113, 0.3);">
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <polyline points="3 6 5 6 21 6"/>
                  <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
                </svg>
              </button>
            </div>
          </div>
          
          <div v-else class="card" style="text-align: center; padding: 3rem;">
            <p style="color: var(--text-muted);">Nenhum vídeo gerado ainda. Use o painel "Criar Novo Vídeo" acima para gerar o primeiro roteiro.</p>
          </div>
        </div>

        <!-- Dedicated Bookends View (Página de Abertura & Encerramento) -->
        <div v-else-if="activeChannel && showBookendsPage && !activeVideo">
          <div style="display: flex; align-items: center; gap: 0.5rem; margin-bottom: 1.5rem; color: var(--text-secondary); cursor: pointer;" @click="showBookendsPage = false">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <line x1="19" y1="12" x2="5" y2="12"/><polyline points="12 19 5 12 12 5"/>
            </svg>
            Voltar para o canal {{ activeChannel.name }}
          </div>

          <div style="margin-bottom: 2rem; background: var(--bg-secondary); border: 1px solid var(--border-color); padding: 1.5rem; border-radius: 16px;">
            <span class="badge" style="background: rgba(6, 182, 212, 0.15); color: var(--accent-cyan); font-size: 0.8rem; margin-bottom: 0.5rem;">Gerenciador do Canal</span>
            <h1 style="font-size: 2rem; font-weight: 800; margin-bottom: 0.5rem;">🎬 Abertura & Encerramento do Canal</h1>
            <p style="color: var(--text-secondary); font-size: 0.95rem; max-width: 850px; line-height: 1.5;">
              Nesta página você pode gerar, personalizar o texto, assistir às prévias em vídeo e gerenciar a <strong>Abertura (Intro)</strong> e o <strong>Encerramento (Outro)</strong> do canal <strong>{{ activeChannel.name }}</strong>. 
              Os clipes criados serão automaticamente anexados no início e no final de todos os vídeos deste canal. Se o campo de texto estiver em branco, o roteiro será gerado via IA.
            </p>
          </div>

          <div class="grid-cols-2" style="gap: 1.5rem; align-items: start;">
            <!-- Abertura (Intro) Card -->
            <div class="card" style="display: flex; flex-direction: column; justify-content: space-between; gap: 1.25rem;">
              <div>
                <div style="display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid var(--border-color); padding-bottom: 0.75rem; margin-bottom: 1rem;">
                  <h2 style="font-size: 1.25rem; font-weight: 700; color: #f4f4f5; display: flex; align-items: center; gap: 0.5rem;">
                    🚀 Abertura do Canal (Intro)
                  </h2>
                  <span v-if="activeChannel.intro_status === 'generating'" class="badge" style="background: rgba(6, 182, 212, 0.15); color: var(--accent-cyan); font-size: 0.75rem;">⚡ Renderizando {{ activeChannel.intro_progress || 0 }}%</span>
                  <span v-else-if="activeChannel.intro_video_path" class="badge" style="background: rgba(16, 185, 129, 0.15); color: var(--success); font-size: 0.75rem;">✓ Pronta</span>
                  <span v-else class="badge" style="background: rgba(245, 158, 11, 0.15); color: #f59e0b; font-size: 0.75rem;">Pendente</span>
                </div>

                <!-- Intro Progress Bar -->
                <div v-if="activeChannel.intro_status === 'generating'" style="margin-bottom: 1.25rem; background: rgba(6, 182, 212, 0.08); border: 1px solid rgba(6, 182, 212, 0.25); padding: 1rem; border-radius: 12px;">
                  <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem; font-size: 0.85rem;">
                    <span style="color: var(--accent-cyan); font-weight: 600; display: flex; align-items: center; gap: 0.4rem;">
                      <span class="spinner" style="width: 14px; height: 14px;"></span>
                      {{ activeChannel.intro_stage || 'Renderizando vídeo da abertura...' }}
                    </span>
                    <strong style="color: #f4f4f5;">{{ activeChannel.intro_progress || 0 }}%</strong>
                  </div>
                  <div style="width: 100%; height: 8px; background: rgba(255, 255, 255, 0.1); border-radius: 999px; overflow: hidden;">
                    <div style="height: 100%; background: linear-gradient(90deg, #06b6d4 0%, #3b82f6 100%); transition: width 0.4s ease; border-radius: 999px;"
                         :style="{ width: (activeChannel.intro_progress || 0) + '%' }"></div>
                  </div>
                </div>

                <!-- Intro Error Notice -->
                <div v-if="activeChannel.intro_status === 'failed' && activeChannel.intro_error" style="margin-bottom: 1rem; padding: 0.75rem 1rem; border-radius: 10px; background: rgba(239, 68, 68, 0.1); border: 1px solid rgba(239, 68, 68, 0.3); color: var(--danger); font-size: 0.8rem;">
                  <strong>Falha na abertura:</strong> {{ activeChannel.intro_error }}
                </div>

                <!-- Video Player Preview -->
                <div v-if="activeChannel.intro_video_path && activeChannel.intro_status !== 'generating'" style="margin-bottom: 1.25rem;">
                  <label class="form-label" style="font-size: 0.8rem; color: var(--accent-cyan); font-weight: 600; margin-bottom: 0.4rem; display: block;">
                    🎥 Prévia do Vídeo da Abertura
                  </label>
                  <video :src="formatRelativePath(activeChannel.intro_video_path)" controls style="width: 100%; max-height: 240px; border-radius: 12px; background: #000; border: 1px solid var(--border-color);"></video>
                  <div style="margin-top: 0.5rem; text-align: right;">
                    <button @click="downloadMediaFile(activeChannel.intro_video_path, `abertura-${slugify(activeChannel.name, 'canal')}.mp4`)"
                            class="btn btn-secondary" style="font-size: 0.78rem;">
                      ⬇️ Baixar Vídeo da Abertura
                    </button>
                  </div>
                </div>

                <!-- Text Script Input -->
                <div class="form-group">
                  <label class="form-label" style="font-weight: 600;">Texto da Narração da Abertura</label>
                  <p style="font-size: 0.78rem; color: var(--text-muted); margin-bottom: 0.5rem;">
                    Digite o texto personalizado para a abertura. Deixe em branco para que a IA escreva um gancho cativante.
                  </p>
                  <textarea v-model="introScriptInput" rows="5" class="form-input" style="width: 100%; font-size: 0.9rem; resize: vertical;" placeholder="Digite o texto da abertura... (Deixe em branco para gerar via IA)"></textarea>
                </div>
              </div>

              <!-- Buttons -->
              <div style="display: flex; gap: 0.75rem; border-top: 1px solid var(--border-color); padding-top: 1rem;">
                <button @click="generateIntro" :disabled="isGeneratingIntro || activeChannel.intro_status === 'generating'" class="btn btn-primary" style="flex: 1; background: linear-gradient(135deg, #06b6d4 0%, #3b82f6 100%); border: none;">
                  <span v-if="isGeneratingIntro" class="spinner"></span>
                  <span v-else>⚡ {{ activeChannel.intro_video_path ? 'Regerar Abertura' : 'Gerar Abertura' }}</span>
                </button>
                <button v-if="activeChannel.intro_video_path || activeChannel.intro_script" @click="deleteIntro" :disabled="isDeletingIntro || activeChannel.intro_status === 'generating'" class="btn btn-secondary" style="border-color: var(--danger); color: var(--danger);">
                  <span v-if="isDeletingIntro" class="spinner"></span>
                  <span v-else>🗑️ Excluir</span>
                </button>
              </div>
            </div>

            <!-- Encerramento (Outro) Card -->
            <div class="card" style="display: flex; flex-direction: column; justify-content: space-between; gap: 1.25rem;">
              <div>
                <div style="display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid var(--border-color); padding-bottom: 0.75rem; margin-bottom: 1rem;">
                  <h2 style="font-size: 1.25rem; font-weight: 700; color: #f4f4f5; display: flex; align-items: center; gap: 0.5rem;">
                    📢 Encerramento do Canal (Outro)
                  </h2>
                  <span v-if="activeChannel.outro_status === 'generating'" class="badge" style="background: rgba(139, 92, 246, 0.15); color: var(--accent-purple); font-size: 0.75rem;">⚡ Renderizando {{ activeChannel.outro_progress || 0 }}%</span>
                  <span v-else-if="activeChannel.outro_video_path" class="badge" style="background: rgba(16, 185, 129, 0.15); color: var(--success); font-size: 0.75rem;">✓ Pronto</span>
                  <span v-else class="badge" style="background: rgba(245, 158, 11, 0.15); color: #f59e0b; font-size: 0.75rem;">Pendente</span>
                </div>

                <!-- Outro Progress Bar -->
                <div v-if="activeChannel.outro_status === 'generating'" style="margin-bottom: 1.25rem; background: rgba(139, 92, 246, 0.08); border: 1px solid rgba(139, 92, 246, 0.25); padding: 1rem; border-radius: 12px;">
                  <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem; font-size: 0.85rem;">
                    <span style="color: var(--accent-purple); font-weight: 600; display: flex; align-items: center; gap: 0.4rem;">
                      <span class="spinner" style="width: 14px; height: 14px;"></span>
                      {{ activeChannel.outro_stage || 'Renderizando vídeo do encerramento...' }}
                    </span>
                    <strong style="color: #f4f4f5;">{{ activeChannel.outro_progress || 0 }}%</strong>
                  </div>
                  <div style="width: 100%; height: 8px; background: rgba(255, 255, 255, 0.1); border-radius: 999px; overflow: hidden;">
                    <div style="height: 100%; background: linear-gradient(90deg, #8b5cf6 0%, #06b6d4 100%); transition: width 0.4s ease; border-radius: 999px;"
                         :style="{ width: (activeChannel.outro_progress || 0) + '%' }"></div>
                  </div>
                </div>

                <!-- Outro Error Notice -->
                <div v-if="activeChannel.outro_status === 'failed' && activeChannel.outro_error" style="margin-bottom: 1rem; padding: 0.75rem 1rem; border-radius: 10px; background: rgba(239, 68, 68, 0.1); border: 1px solid rgba(239, 68, 68, 0.3); color: var(--danger); font-size: 0.8rem;">
                  <strong>Falha no encerramento:</strong> {{ activeChannel.outro_error }}
                </div>

                <!-- Video Player Preview -->
                <div v-if="activeChannel.outro_video_path && activeChannel.outro_status !== 'generating'" style="margin-bottom: 1.25rem;">
                  <label class="form-label" style="font-size: 0.8rem; color: var(--accent-cyan); font-weight: 600; margin-bottom: 0.4rem; display: block;">
                    🎥 Prévia do Vídeo do Encerramento
                  </label>
                  <video :src="formatRelativePath(activeChannel.outro_video_path)" controls style="width: 100%; max-height: 240px; border-radius: 12px; background: #000; border: 1px solid var(--border-color);"></video>
                  <div style="margin-top: 0.5rem; text-align: right;">
                    <button @click="downloadMediaFile(activeChannel.outro_video_path, `encerramento-${slugify(activeChannel.name, 'canal')}.mp4`)"
                            class="btn btn-secondary" style="font-size: 0.78rem;">
                      ⬇️ Baixar Vídeo do Encerramento
                    </button>
                  </div>
                </div>

                <!-- Text Script Input -->
                <div class="form-group">
                  <label class="form-label" style="font-weight: 600;">Texto da Narração do Encerramento</label>
                  <p style="font-size: 0.78rem; color: var(--text-muted); margin-bottom: 0.5rem;">
                    Digite a chamada para ação (CTA). Deixe em branco para que a IA escreva o texto pedindo inscrição e curtidas.
                  </p>
                  <textarea v-model="outroScriptInput" rows="5" class="form-input" style="width: 100%; font-size: 0.9rem; resize: vertical;" placeholder="Digite o texto do encerramento... (Deixe em branco para gerar via IA)"></textarea>
                </div>
              </div>

              <!-- Buttons -->
              <div style="display: flex; gap: 0.75rem; border-top: 1px solid var(--border-color); padding-top: 1rem;">
                <button @click="generateOutro" :disabled="isGeneratingOutro || activeChannel.outro_status === 'generating'" class="btn btn-primary" style="flex: 1; background: linear-gradient(135deg, #8b5cf6 0%, #06b6d4 100%); border: none;">
                  <span v-if="isGeneratingOutro" class="spinner"></span>
                  <span v-else>⚡ {{ activeChannel.outro_video_path ? 'Regerar Encerramento' : 'Gerar Encerramento' }}</span>
                </button>
                <button v-if="activeChannel.outro_video_path || activeChannel.outro_script" @click="deleteOutro" :disabled="isDeletingOutro || activeChannel.outro_status === 'generating'" class="btn btn-secondary" style="border-color: var(--danger); color: var(--danger);">
                  <span v-if="isDeletingOutro" class="spinner"></span>
                  <span v-else>🗑️ Excluir</span>
                </button>
              </div>
            </div>

          </div>
        </div>

        <!-- Video Detail View -->
        <div v-else-if="activeVideo">
          <div style="display: flex; align-items: center; gap: 0.5rem; margin-bottom: 1.5rem; color: var(--text-secondary); cursor: pointer;" @click="goBackToChannel">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <line x1="19" y1="12" x2="5" y2="12"/><polyline points="12 19 5 12 12 5"/>
            </svg>
            Voltar para o Canal
          </div>

          <div class="flex-wrap-header">
            <div style="min-width: 0;">
              <span :class="getStatusBadgeClass(activeVideo.status)" style="margin-bottom: 0.5rem;">{{ statusLabel(activeVideo.status) }}</span>
              <h1 style="font-size: 1.85rem; margin-top: 0.25rem; word-break: break-word;">{{ activeVideo.title }}</h1>
            </div>
            
            <div style="display: flex; flex-wrap: wrap; gap: 0.75rem;">
              <button v-if="canRender(activeVideo)" @click="triggerRender" :disabled="isRendering" class="btn btn-cyan">
                <span v-if="isRendering" class="spinner"></span>
                <span v-else>{{ activeVideo.status === 'failed' ? 'Tentar Renderizar Novamente' : 'Gerar Vídeo Completo (FFmpeg)' }}</span>
              </button>
              <a v-if="activeVideo.youtube_video_id" :href="`https://www.youtube.com/watch?v=${activeVideo.youtube_video_id}`" target="_blank" rel="noopener" class="btn btn-secondary">
                ▶ Ver no YouTube
              </a>
              
              <div v-if="activeVideo.status === 'ready' || activeVideo.status === 'scheduled' || activeVideo.status === 'posted'" style="display: flex; flex-wrap: wrap; gap: 0.75rem;">
                <button v-if="activeVideo.video_path && !activeVideo.media_purged" @click="downloadMediaFile(activeVideo.video_path, `${slugify(activeVideo.title, 'video')}-16x9.mp4`)" class="btn btn-secondary">
                  Download Vídeo (16:9)
                </button>
                <button v-if="activeVideo.shorts_path && !activeVideo.media_purged" @click="downloadMediaFile(activeVideo.shorts_path, `${slugify(activeVideo.title, 'shorts')}-9x16.mp4`)" class="btn btn-secondary">
                  Download Shorts (9:16)
                </button>
                <button v-if="activeVideo.subtitles_path && !activeVideo.media_purged" @click="downloadMediaFile(activeVideo.subtitles_path, `${slugify(activeVideo.title, 'legendas')}.srt`)" class="btn btn-secondary">
                  Download Legendas (.srt)
                </button>
                <span v-if="activeVideo.media_purged" style="font-size: 0.8rem; color: var(--text-muted); align-self: center;">
                  Arquivo removido do armazenamento após publicação
                </span>
              </div>

              <button @click="deleteVideo(activeVideo)" :disabled="isBusyStatus(activeVideo.status)" class="btn btn-secondary" style="color: #f87171; border-color: rgba(248, 113, 113, 0.3);">
                Excluir Vídeo
              </button>
            </div>
          </div>

          <!-- Error/warning message: a failed/blocked video gave no clue why before -->
          <div v-if="activeVideo.error_message" class="card" style="margin-bottom: 1.25rem; padding: 1rem 1.25rem;"
               :style="isWarningOnly(activeVideo) ? 'border: 1px solid rgba(245, 158, 11, 0.35); background: rgba(245, 158, 11, 0.08);' : 'border: 1px solid rgba(239, 68, 68, 0.35); background: rgba(239, 68, 68, 0.08);'">
            <div style="display: flex; gap: 0.6rem; align-items: flex-start;">
              <span style="font-size: 1.1rem; line-height: 1;">⚠️</span>
              <div style="min-width: 0; word-break: break-word;">
                <strong :style="{ color: isWarningOnly(activeVideo) ? 'var(--warning)' : 'var(--danger)', fontSize: '0.85rem' }">{{ isWarningOnly(activeVideo) ? 'Atenção' : 'Erro' }}</strong>
                <p style="margin: 0.2rem 0 0; font-size: 0.85rem; color: var(--text-secondary);">{{ activeVideo.error_message }}</p>
              </div>
            </div>
          </div>

          <!-- Degradation warning: a "ready"/"scheduled" video could silently contain silent or
               generic-stock-footage scenes with no visible sign of it - this makes that visible. -->
          <div v-if="activeVideo.total_scene_count > 0 && activeVideo.degraded_scene_count > 0"
               class="card" style="margin-bottom: 1.25rem; padding: 1rem 1.25rem;"
               :style="{ border: '1px solid ' + (activeVideo.degraded_scene_count / activeVideo.total_scene_count > 0.3 ? 'rgba(239, 68, 68, 0.35)' : 'rgba(245, 158, 11, 0.35)'), background: (activeVideo.degraded_scene_count / activeVideo.total_scene_count > 0.3 ? 'rgba(239, 68, 68, 0.08)' : 'rgba(245, 158, 11, 0.08)') }">
            <div style="display: flex; gap: 0.6rem; align-items: flex-start;">
              <span style="font-size: 1.1rem; line-height: 1;">{{ activeVideo.degraded_scene_count / activeVideo.total_scene_count > 0.3 ? '🚫' : '⚠️' }}</span>
              <div>
                <strong :style="{ color: activeVideo.degraded_scene_count / activeVideo.total_scene_count > 0.3 ? 'var(--danger)' : 'var(--warning)', fontSize: '0.85rem' }">
                  {{ activeVideo.degraded_scene_count }} de {{ activeVideo.total_scene_count }} cenas usaram conteúdo de reserva
                </strong>
                <p style="margin: 0.2rem 0 0; font-size: 0.85rem; color: var(--text-secondary);">
                  Áudio mudo ou clipe genérico foi usado porque a narração ou o vídeo/imagem original da cena não ficou pronto.
                  {{ activeVideo.degraded_scene_count / activeVideo.total_scene_count > 0.3 ? 'Acima de 30% degradado — a publicação automática bloqueia este vídeo.' : '' }}
                </p>
              </div>
            </div>
          </div>

          <!-- PlaySquad Viral Scorecard Dashboard -->
          <div class="card" style="margin-bottom: 2rem; background: linear-gradient(135deg, rgba(15, 23, 42, 0.9) 0%, rgba(30, 27, 75, 0.6) 100%); border: 1px solid rgba(139, 92, 246, 0.3); padding: 1.5rem; border-radius: 16px;">
            <div class="channel-bar-row" style="margin-bottom: 1.25rem;">
              <div>
                <h3 style="font-size: 1.15rem; font-weight: 800; color: #f4f4f5; display: flex; align-items: center; gap: 0.6rem;">
                  <span style="font-size: 1.3rem;">📊</span> Scorecard de Viralidade & Análise PlaySquad
                </h3>
                <p style="font-size: 0.8rem; color: var(--text-secondary); margin-top: 0.2rem;">
                  Pontuação técnica baseada em Força de Gancho, Retenção dos 2 Minutos, CTR e Originalidade.
                </p>
              </div>
              <div style="text-align: right; background: rgba(139, 92, 246, 0.15); border: 1px solid rgba(139, 92, 246, 0.4); padding: 0.5rem 1rem; border-radius: 12px;">
                <span style="font-size: 1.8rem; font-weight: 900; color: var(--accent-cyan);">{{ activeVideo.viral_score ?? 88 }}/100</span>
                <span style="font-size: 0.75rem; display: block; color: var(--success); font-weight: 700;">POTENCIAL VIRAL EXCELENTE</span>
              </div>
            </div>

            <!-- Scorecard Progress Bars -->
            <div class="scorecard-grid">
              <div style="background: rgba(255,255,255,0.03); padding: 0.85rem 1rem; border-radius: 10px; border: 1px solid rgba(255,255,255,0.05);">
                <div style="display: flex; justify-content: space-between; font-size: 0.8rem; margin-bottom: 0.4rem;">
                  <span style="color: var(--text-muted);">⚡ Força do Hook (0-15s)</span>
                  <strong style="color: var(--accent-cyan);">{{ activeVideo.hook_power_score ?? 90 }}/100</strong>
                </div>
                <div style="width: 100%; height: 6px; background: rgba(255,255,255,0.1); border-radius: 3px; overflow: hidden;">
                  <div :style="{ width: (activeVideo.hook_power_score ?? 90) + '%' }" style="height: 100%; background: var(--accent-cyan);"></div>
                </div>
              </div>

              <div style="background: rgba(255,255,255,0.03); padding: 0.85rem 1rem; border-radius: 10px; border: 1px solid rgba(255,255,255,0.05);">
                <div style="display: flex; justify-content: space-between; font-size: 0.8rem; margin-bottom: 0.4rem;">
                  <span style="color: var(--text-muted);">⏱️ Retenção Est. (AVD)</span>
                  <strong style="color: var(--accent-purple);">{{ activeVideo.retention_score ?? 85 }}/100</strong>
                </div>
                <div style="width: 100%; height: 6px; background: rgba(255,255,255,0.1); border-radius: 3px; overflow: hidden;">
                  <div :style="{ width: (activeVideo.retention_score ?? 85) + '%' }" style="height: 100%; background: var(--accent-purple);"></div>
                </div>
              </div>

              <div style="background: rgba(255,255,255,0.03); padding: 0.85rem 1rem; border-radius: 10px; border: 1px solid rgba(255,255,255,0.05);">
                <div style="display: flex; justify-content: space-between; font-size: 0.8rem; margin-bottom: 0.4rem;">
                  <span style="color: var(--text-muted);">🧲 Potencial CTR</span>
                  <strong style="color: #f59e0b;">{{ activeVideo.ctr_score ?? 92 }}/100</strong>
                </div>
                <div style="width: 100%; height: 6px; background: rgba(255,255,255,0.1); border-radius: 3px; overflow: hidden;">
                  <div :style="{ width: (activeVideo.ctr_score ?? 92) + '%' }" style="height: 100%; background: #f59e0b;"></div>
                </div>
              </div>

              <div style="background: rgba(255,255,255,0.03); padding: 0.85rem 1rem; border-radius: 10px; border: 1px solid rgba(255,255,255,0.05);">
                <div style="display: flex; justify-content: space-between; font-size: 0.8rem; margin-bottom: 0.4rem;">
                  <span style="color: var(--text-muted);">🛡️ Monetização / Anti-Slop</span>
                  <strong style="color: var(--success);">{{ activeVideo.originality_score ?? 95 }}/100</strong>
                </div>
                <div style="width: 100%; height: 6px; background: rgba(255,255,255,0.1); border-radius: 3px; overflow: hidden;">
                  <div :style="{ width: (activeVideo.originality_score ?? 95) + '%' }" style="height: 100%; background: var(--success);"></div>
                </div>
              </div>
            </div>

            <div v-if="activeVideo.score_notes" style="background: rgba(0,0,0,0.3); border: 1px dashed rgba(255,255,255,0.1); padding: 0.75rem 1rem; border-radius: 8px; font-size: 0.82rem; color: var(--text-secondary);">
              💡 <strong>Análise de Inteligência:</strong> {{ activeVideo.score_notes }}
            </div>
          </div>

          <!-- Render Progress Bar: real, step-based progress reported by the server (renders run entirely server-side) -->
          <div v-if="activeVideo.status === 'posting'" class="card" style="margin-bottom: 2rem; padding: 1.25rem 1.5rem;">
            <span style="font-weight: 700; font-size: 0.9rem; display: flex; align-items: center; gap: 0.5rem;">
              <span class="spinner"></span> Publicando no YouTube/TikTok...
            </span>
            <p style="font-size: 0.75rem; color: var(--text-muted); margin-top: 0.6rem;">O envio de vídeos longos pode levar vários minutos.</p>
          </div>

          <div v-if="activeVideo.status === 'generating'" class="card" style="margin-bottom: 2rem; padding: 1.25rem 1.5rem;">
            <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.6rem;">
              <span style="font-weight: 700; font-size: 0.9rem; display: flex; align-items: center; gap: 0.5rem;">
                <span class="spinner"></span> {{ activeVideo.render_stage || 'Renderizando...' }}
              </span>
              <span style="font-size: 0.85rem; color: var(--text-muted);">{{ activeVideo.render_progress || 0 }}%</span>
            </div>
            <div style="width: 100%; height: 8px; border-radius: 4px; background: var(--bg-tertiary); overflow: hidden;">
              <div :style="{ width: (activeVideo.render_progress || 0) + '%', height: '100%', background: 'linear-gradient(90deg, #8b5cf6 0%, #06b6d4 100%)', transition: 'width 0.4s ease' }"></div>
            </div>
            <p style="font-size: 0.75rem; color: var(--text-muted); margin-top: 0.6rem;">
              Renderizado no servidor (FFmpeg) - não usa o processador do seu computador.
            </p>
          </div>

          <div class="video-detail-grid">
            <!-- Left Side: Script and Metadata details -->
            <div class="card" style="display: flex; flex-direction: column; gap: 1.5rem;">
              <div>
                <h3 style="font-size: 1.1rem; font-weight: 700; margin-bottom: 0.75rem; border-bottom: 1px solid var(--border-color); padding-bottom: 0.5rem;">Metadados Gerados por IA</h3>
                <div class="form-group">
                  <label class="form-label">Título do Vídeo</label>
                  <input :value="activeVideo.title" readonly type="text" class="form-input" />
                </div>
                <div class="form-group">
                  <label class="form-label">Descrição SEO</label>
                  <textarea :value="activeVideo.description" readonly rows="4" class="form-textarea"></textarea>
                </div>
                <div class="form-group">
                  <label class="form-label">Tags (separadas por vírgula)</label>
                  <input :value="activeVideo.tags" readonly type="text" class="form-input" />
                </div>
              </div>

              <div>
                <h3 style="font-size: 1.1rem; font-weight: 700; margin-bottom: 0.75rem; border-bottom: 1px solid var(--border-color); padding-bottom: 0.5rem;">Roteiro Integro</h3>
                <p style="font-size: 0.95rem; color: var(--text-secondary); line-height: 1.6; white-space: pre-line; background: var(--bg-tertiary); padding: 1.25rem; border-radius: 12px; border: 1px solid var(--border-color);">
                  {{ activeVideo.script || 'Nenhum roteiro gerado ainda.' }}
                </p>
              </div>
            </div>

            <!-- Right Side: Scheduling and Video player preview -->
            <div style="display: flex; flex-direction: column; gap: 1.5rem;">
              <!-- Video Player Preview Card -->
              <div v-if="(activeVideo.video_path || activeVideo.shorts_path) && !activeVideo.media_purged" class="card">
                <h3 style="font-size: 1.1rem; font-weight: 700; margin-bottom: 1rem; color: var(--accent-cyan);">Prévia do Vídeo</h3>
                <div v-if="activeVideo.video_path" style="margin-bottom: 1rem;">
                  <span style="font-size: 0.75rem; color: var(--text-muted); display: block; margin-bottom: 0.4rem;">Formato 16:9</span>
                  <video :src="formatRelativePath(activeVideo.video_path)" controls style="width: 100%; border-radius: 12px; background: #000; border: 1px solid var(--border-color);"></video>
                </div>
                <div v-if="activeVideo.shorts_path">
                  <span style="font-size: 0.75rem; color: var(--text-muted); display: block; margin-bottom: 0.4rem;">Shorts 9:16</span>
                  <video :src="formatRelativePath(activeVideo.shorts_path)" controls style="width: 100%; max-height: 360px; border-radius: 12px; background: #000; border: 1px solid var(--border-color);"></video>
                </div>
              </div>

              <!-- Scheduler Card -->
              <div v-if="activeVideo.status === 'ready' || activeVideo.status === 'scheduled'" class="card" style="border-color: rgba(6, 182, 212, 0.2);">
                <h3 style="font-size: 1.1rem; font-weight: 700; margin-bottom: 1rem; color: var(--accent-cyan);">Agendar Publicação</h3>
                <p style="font-size: 0.8rem; color: var(--text-secondary); margin-bottom: 1rem;">Selecione o horário para postagem automatizada no TikTok e YouTube.</p>
                <div class="form-group">
                  <label class="form-label">Data e Hora de Postagem</label>
                  <input v-model="scheduleDate" type="datetime-local" class="form-input" />
                </div>
                <p v-if="activeChannel && !activeChannel.yt_connected && !activeChannel.tiktok_connected" style="font-size: 0.78rem; color: var(--warning); margin-bottom: 0.75rem;">
                  Conecte o YouTube ou o TikTok na página do canal para poder agendar.
                </p>
                <button @click="scheduleVideo" :disabled="!scheduleDate" class="btn btn-cyan" style="width: 100%;">
                  {{ activeVideo.status === 'scheduled' ? 'Atualizar Agendamento' : 'Agendar Postagem' }}
                </button>
                <button v-if="activeVideo.status === 'scheduled'" @click="unscheduleVideo" class="btn btn-secondary" style="width: 100%; margin-top: 0.5rem;">
                  Cancelar Agendamento
                </button>
              </div>

              <div v-if="activeVideo.scenes && activeVideo.scenes.length" class="card">
                <h3 style="font-size: 1.1rem; font-weight: 700; margin-bottom: 1rem;">Estrutura de Cenas ({{ activeVideo.scenes.length }})</h3>
                
                <div style="display: flex; flex-direction: column; gap: 1rem;">
                  <div v-for="scene in activeVideo.scenes" :key="scene.id" style="border: 1px solid var(--border-color); border-radius: 12px; padding: 1rem; background: var(--bg-tertiary);">
                    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem;">
                      <span style="font-weight: 700; font-size: 0.8rem; text-transform: uppercase; color: var(--accent-purple);">Cena {{ scene.index }}</span>
                      <span v-if="scene.duration" style="font-size: 0.75rem; color: var(--text-muted);">Duração: {{ scene.duration.toFixed(1) }}s</span>
                    </div>

                    <!-- Scene Image preview -->
                    <div v-if="scene.image_path" class="scene-image-preview" :style="{ backgroundImage: `url(${formatRelativePath(scene.image_path)})` }"></div>

                    <p style="font-size: 0.85rem; color: var(--text-secondary); margin-top: 0.5rem; line-height: 1.4;">
                      {{ scene.narration_text }}
                    </p>

                    <!-- Scene Audio narration preview -->
                    <div v-if="scene.audio_path" style="margin-top: 0.75rem;">
                      <audio controls :src="formatRelativePath(scene.audio_path)" style="width: 100%; height: 35px;"></audio>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </section>

      <!-- Tab 2: Calendar Visualization -->
      <section v-if="currentTab === 'calendar'">
        <div style="margin-bottom: 2rem;">
          <h1>Cronograma de Conteúdo</h1>
          <p style="color: var(--text-secondary);">Visualize toda a programação e postagens planejadas ao longo do mês.</p>
        </div>

        <div class="card" style="padding: 2rem;">
          <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 1.5rem;">
            <div style="display: flex; align-items: center; gap: 0.5rem;">
              <button type="button" class="btn btn-secondary" style="padding: 0.4rem 0.75rem;" @click="shiftCalendarMonth(-1)" title="Mês anterior">‹</button>
              <h3 style="font-size: 1.3rem; font-weight: 800; color: var(--accent-purple); min-width: 190px; text-align: center;">{{ getMonthName() }}</h3>
              <button type="button" class="btn btn-secondary" style="padding: 0.4rem 0.75rem;" @click="shiftCalendarMonth(1)" title="Próximo mês">›</button>
              <button type="button" class="btn btn-secondary" style="padding: 0.4rem 0.75rem; font-size: 0.8rem;" @click="goToCalendarToday">Hoje</button>
              <span v-if="isLoadingCalendar" class="spinner" style="width: 14px; height: 14px;"></span>
            </div>
            <div style="display: flex; gap: 1rem; font-size: 0.8rem;">
              <span style="display: flex; align-items: center; gap: 0.25rem;"><span class="badge badge-scheduled" style="width: 10px; height: 10px; padding:0; border-radius:50%;"></span> Agendados</span>
              <span style="display: flex; align-items: center; gap: 0.25rem;"><span class="badge badge-posted" style="width: 10px; height: 10px; padding:0; border-radius:50%;"></span> Publicados</span>
            </div>
          </div>

          <div class="calendar-grid">
            <!-- Day of Week Headers -->
            <div class="calendar-day-header">DOM</div>
            <div class="calendar-day-header">SEG</div>
            <div class="calendar-day-header">TER</div>
            <div class="calendar-day-header">QUA</div>
            <div class="calendar-day-header">QUI</div>
            <div class="calendar-day-header">SEX</div>
            <div class="calendar-day-header">SÁB</div>

            <!-- Calendar Days -->
            <div v-for="(day, index) in calendarDays" :key="index" class="calendar-day" :class="{ today: day.isToday }">
              <div v-if="day.dayNum !== null" style="display: flex; flex-direction: column; height: 100%;">
                <span class="calendar-day-num">{{ day.dayNum }}</span>
                <div style="flex-grow: 1; overflow-y: auto;">
                  <div v-for="post in day.posts" :key="post.id" class="calendar-post-badge" :class="post.status === 'posted' ? 'badge-posted' : 'badge-scheduled'"
                       :title="`${post.title} - ${statusLabel(post.status)} ${calendarPostDate(post).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`"
                       style="cursor: pointer;" @click="openCalendarPost(post)">
                    {{ calendarPostDate(post).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) }} [{{ post.channel_name }}] {{ post.title }}
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </section>

      <!-- Tab 3: Settings/APIs Configuration -->
      <section v-if="currentTab === 'settings'">
        <div style="margin-bottom: 2rem;">
          <h1>Configurações das APIs</h1>
          <p style="color: var(--text-secondary);">Cadastre suas chaves de API externas e credenciais de postagem do YouTube e TikTok.</p>
        </div>

        <div class="card" style="max-width: 700px; display: flex; flex-direction: column; gap: 2rem;">
          <div>
            <h3 style="font-weight: 700; margin-bottom: 1rem; border-bottom: 1px solid var(--border-color); padding-bottom: 0.5rem;">Inteligência Artificial (Criação)</h3>
            <p style="font-size: 0.85rem; color: var(--text-muted); margin-bottom: 0.75rem;">Pelo menos uma chave de LLM (Groq, Google Gemini, Claude ou OpenAI) deve estar configurada para gerar roteiros e vídeos. Groq e Claude são configurados via <code>.env</code> no servidor e testáveis abaixo em Diagnóstico de Conexões.</p>
            <p style="font-size: 0.8rem; margin-bottom: 1.25rem; display: flex; flex-wrap: wrap; gap: 0.75rem;">
              <span :style="{ color: settingsStatus.groq_configured ? 'var(--success)' : 'var(--text-muted)' }">{{ settingsStatus.groq_configured ? '✓' : '✗' }} Groq</span>
              <span :style="{ color: settingsStatus.gemini_configured ? 'var(--success)' : 'var(--text-muted)' }">{{ settingsStatus.gemini_configured ? '✓' : '✗' }} Gemini</span>
              <span :style="{ color: settingsStatus.claude_configured ? 'var(--success)' : 'var(--text-muted)' }">{{ settingsStatus.claude_configured ? '✓' : '✗' }} Claude</span>
              <span :style="{ color: settingsStatus.openai_configured ? 'var(--success)' : 'var(--text-muted)' }">{{ settingsStatus.openai_configured ? '✓' : '✗' }} OpenAI</span>
            </p>

            <div class="form-group">
              <label class="form-label">Google Gemini API Key <span v-if="settingsStatus.gemini_configured" style="color: var(--success);">(configurada)</span></label>
              <div style="display: flex; gap: 0.5rem; align-items: center;">
                <input v-model="settingsForm.gemini_api_key" type="password" placeholder="AIzaSy..." class="form-input" style="flex: 1;" />
                <button type="button" class="btn btn-secondary" :disabled="apiTestResults.gemini.loading" @click="testProvider('gemini')" style="white-space: nowrap;">
                  <span v-if="apiTestResults.gemini.loading" class="spinner"></span>
                  <span v-else>Testar</span>
                </button>
              </div>
              <p v-if="apiTestResults.gemini.message" style="font-size: 0.8rem; margin-top: 0.35rem;" :style="{ color: apiTestResults.gemini.ok ? 'var(--success)' : 'var(--danger)' }">{{ apiTestResults.gemini.message }}</p>
            </div>

            <div class="form-group">
              <label class="form-label">OpenAI API Key (logos, capas e thumbnails via gpt-image; GPT como último recurso de texto) <span v-if="settingsStatus.openai_configured" style="color: var(--success);">(configurada)</span></label>
              <div style="display: flex; gap: 0.5rem; align-items: center;">
                <input v-model="settingsForm.openai_api_key" type="password" placeholder="sk-..." class="form-input" style="flex: 1;" />
                <button type="button" class="btn btn-secondary" :disabled="apiTestResults.openai.loading" @click="testProvider('openai')" style="white-space: nowrap;">
                  <span v-if="apiTestResults.openai.loading" class="spinner"></span>
                  <span v-else>Testar</span>
                </button>
              </div>
              <p v-if="apiTestResults.openai.message" style="font-size: 0.8rem; margin-top: 0.35rem;" :style="{ color: apiTestResults.openai.ok ? 'var(--success)' : 'var(--danger)' }">{{ apiTestResults.openai.message }}</p>
            </div>
          </div>

          <div>
            <h3 style="font-weight: 700; margin-bottom: 1rem; border-bottom: 1px solid var(--border-color); padding-bottom: 0.5rem;">Narração (Vozes)</h3>
            <div class="form-group">
              <label class="form-label">ElevenLabs API Key (Vozes de Alta Fidelidade) <span v-if="settingsStatus.elevenlabs_configured" style="color: var(--success);">(configurada)</span></label>
              <div style="display: flex; gap: 0.5rem; align-items: center;">
                <input v-model="settingsForm.elevenlabs_api_key" type="password" placeholder="Entrar ElevenLabs Key..." class="form-input" style="flex: 1;" />
                <button type="button" class="btn btn-secondary" :disabled="apiTestResults.elevenlabs.loading" @click="testProvider('elevenlabs')" style="white-space: nowrap;">
                  <span v-if="apiTestResults.elevenlabs.loading" class="spinner"></span>
                  <span v-else>Testar</span>
                </button>
              </div>
              <p v-if="apiTestResults.elevenlabs.message" style="font-size: 0.8rem; margin-top: 0.35rem;" :style="{ color: apiTestResults.elevenlabs.ok ? 'var(--success)' : 'var(--danger)' }">{{ apiTestResults.elevenlabs.message }}</p>
            </div>
          </div>

          <div>
            <h3 style="font-weight: 700; margin-bottom: 1rem; border-bottom: 1px solid var(--border-color); padding-bottom: 0.5rem;">Mídia Stock (Cenas)</h3>
            <div class="form-group">
              <label class="form-label">Pexels API Key (Fotos e Clipes HD) <span v-if="settingsStatus.pexels_configured" style="color: var(--success);">(configurada)</span></label>
              <div style="display: flex; gap: 0.5rem; align-items: center;">
                <input v-model="settingsForm.pexels_api_key" type="password" placeholder="Entrar Pexels Key..." class="form-input" style="flex: 1;" />
                <button type="button" class="btn btn-secondary" :disabled="apiTestResults.pexels.loading" @click="testProvider('pexels')" style="white-space: nowrap;">
                  <span v-if="apiTestResults.pexels.loading" class="spinner"></span>
                  <span v-else>Testar</span>
                </button>
              </div>
              <p v-if="apiTestResults.pexels.message" style="font-size: 0.8rem; margin-top: 0.35rem;" :style="{ color: apiTestResults.pexels.ok ? 'var(--success)' : 'var(--danger)' }">{{ apiTestResults.pexels.message }}</p>
            </div>
          </div>

          <div>
            <h3 style="font-weight: 700; margin-bottom: 1rem; border-bottom: 1px solid var(--border-color); padding-bottom: 0.5rem;">Integrações de Postagem</h3>
            <p style="font-size: 0.85rem; color: var(--text-muted); margin-bottom: 0.5rem;">Defina os IDs de aplicativos de desenvolvedor para permitir o login OAuth2 no YouTube e TikTok.</p>
            <p v-if="settingsStatus.redirect_uri" style="font-size: 0.8rem; color: var(--text-muted); margin-bottom: 1.25rem; word-break: break-all;">
              URL de retorno (cadastre-a no Google Cloud / TikTok for Developers): <code>{{ settingsStatus.redirect_uri }}</code>
            </p>

            <div class="grid-cols-2">
              <div class="form-group">
                <label class="form-label">YouTube Client ID <span v-if="settingsStatus.youtube_configured" style="color: var(--success);">✓</span></label>
                <input v-model="settingsForm.youtube_client_id" type="text" placeholder="google-oauth-client-id" class="form-input" />
              </div>
              <div class="form-group">
                <label class="form-label">YouTube Client Secret</label>
                <input v-model="settingsForm.youtube_client_secret" type="password" placeholder="secret" class="form-input" />
              </div>
            </div>
            <div style="display: flex; align-items: center; gap: 0.5rem; margin-bottom: 1rem;">
              <button type="button" class="btn btn-secondary" :disabled="apiTestResults.youtube.loading" @click="testProvider('youtube')">
                <span v-if="apiTestResults.youtube.loading" class="spinner"></span>
                <span v-else>Testar YouTube</span>
              </button>
              <p v-if="apiTestResults.youtube.message" style="font-size: 0.8rem; margin: 0;" :style="{ color: apiTestResults.youtube.ok ? 'var(--success)' : 'var(--danger)' }">{{ apiTestResults.youtube.message }}</p>
            </div>

            <div class="grid-cols-2">
              <div class="form-group">
                <label class="form-label">TikTok Client Key <span v-if="settingsStatus.tiktok_configured" style="color: var(--success);">✓</span></label>
                <input v-model="settingsForm.tiktok_client_id" type="text" placeholder="tiktok-client-key" class="form-input" />
              </div>
              <div class="form-group">
                <label class="form-label">TikTok Client Secret</label>
                <input v-model="settingsForm.tiktok_client_secret" type="password" placeholder="secret" class="form-input" />
              </div>
            </div>
            <div style="display: flex; align-items: center; gap: 0.5rem;">
              <button type="button" class="btn btn-secondary" :disabled="apiTestResults.tiktok.loading" @click="testProvider('tiktok')">
                <span v-if="apiTestResults.tiktok.loading" class="spinner"></span>
                <span v-else>Testar TikTok</span>
              </button>
              <p v-if="apiTestResults.tiktok.message" style="font-size: 0.8rem; margin: 0;" :style="{ color: apiTestResults.tiktok.ok ? 'var(--success)' : 'var(--danger)' }">{{ apiTestResults.tiktok.message }}</p>
            </div>
          </div>

          <div style="display: flex; justify-content: flex-end; gap: 1rem;">
            <button :disabled="isSavingSettings" @click="saveSettings" class="btn btn-primary">
              <span v-if="isSavingSettings" class="spinner"></span>
              <span v-else>Salvar Chaves</span>
            </button>
          </div>
        </div>

        <div class="card" style="max-width: 700px; margin-top: 1.5rem;">
          <h3 style="font-weight: 700; margin-bottom: 1rem; border-bottom: 1px solid var(--border-color); padding-bottom: 0.5rem;">Alterar Senha</h3>
          <form @submit.prevent="changePassword" style="display: flex; flex-direction: column; gap: 0.25rem;">
            <div class="form-group">
              <label class="form-label">Senha atual</label>
              <input v-model="passwordForm.current" type="password" autocomplete="current-password" required class="form-input" />
            </div>
            <div class="grid-cols-2">
              <div class="form-group">
                <label class="form-label">Nova senha (mín. 8 caracteres)</label>
                <input v-model="passwordForm.next" type="password" autocomplete="new-password" required minlength="8" class="form-input" />
              </div>
              <div class="form-group">
                <label class="form-label">Confirmar nova senha</label>
                <input v-model="passwordForm.confirm" type="password" autocomplete="new-password" required minlength="8" class="form-input" />
              </div>
            </div>
            <div style="display: flex; justify-content: flex-end;">
              <button type="submit" :disabled="isChangingPassword" class="btn btn-primary">
                <span v-if="isChangingPassword" class="spinner"></span>
                <span v-else>Alterar Senha</span>
              </button>
            </div>
          </form>
        </div>

        <div class="card" style="max-width: 700px; margin-top: 1.5rem;">
          <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem;">
            <h3 style="font-weight: 700;">Diagnóstico de Conexões</h3>
            <button type="button" class="btn btn-secondary" @click="testAllProviders">Testar Todas</button>
          </div>
          <p style="font-size: 0.85rem; color: var(--text-muted); margin-bottom: 1.25rem;">
            Demais integrações (configuradas via variável de ambiente no servidor, não editáveis aqui). Cada botão faz uma chamada real e mínima à API e mostra o motivo exato em caso de erro.
          </p>
          <div style="display: flex; flex-direction: column; gap: 0.75rem;">
            <div v-for="p in apiProviders.filter(p => ['groq','claude','pollinations','shotstack','r2','image_ranker'].includes(p.key))" :key="p.key"
                 style="display: flex; flex-wrap: wrap; align-items: center; gap: 0.75rem; padding: 0.6rem 0; border-bottom: 1px solid var(--border-color);">
              <span style="flex: 1; min-width: 180px; font-weight: 600;">{{ p.label }}</span>
              <span v-if="apiTestResults[p.key].message" style="font-size: 0.8rem; max-width: 380px; text-align: right; word-break: break-word;"
                    :style="{ color: apiTestResults[p.key].ok ? 'var(--success)' : 'var(--danger)' }">{{ apiTestResults[p.key].message }}</span>
              <button type="button" class="btn btn-secondary" :disabled="apiTestResults[p.key].loading" @click="testProvider(p.key)" style="white-space: nowrap;">
                <span v-if="apiTestResults[p.key].loading" class="spinner"></span>
                <span v-else>Testar</span>
              </button>
            </div>
          </div>
        </div>
      </section>
    </main>

    <!-- Modal: Conception of Channel by AI -->
    <div v-if="showCreateChannelModal" class="modal-backdrop" @click.self="!isGeneratingChannel && (showCreateChannelModal = false)">
      <div class="modal-content">
        <h2 style="font-weight: 800; font-size: 1.5rem; margin-bottom: 0.5rem;">Conceber Canal com IA</h2>
        <p style="color: var(--text-secondary); font-size: 0.9rem; margin-bottom: 1.5rem;">Insira o nicho que você deseja focar e a Inteligência Artificial gerará o nome, o conceito conceitual, o logotipo e o banner do zero.</p>
        
        <div class="form-group">
          <label class="form-label">Nicho do Canal</label>
          <select v-model="newChannel.niche" class="form-select">
            <option value="Curiosidades e Mistérios">Curiosidades & Mistérios (Foco em Cliques)</option>
            <option value="Finanças e Mentalidade">Finanças, Riqueza & Mindset (Alto RPM)</option>
            <option value="Histórias e Mitologia">Histórias Antigas & Mitologias</option>
            <option value="Quiz e Desafios">Quiz, Charadas & Desafios Rápidos (Altamente Viral)</option>
            <option value="Fatos de Terror">Histórias de Terror & Reddit</option>
          </select>
        </div>

        <div class="form-group">
          <label class="form-label">Nome do Canal (Opcional - Deixe vazio para IA gerar)</label>
          <input v-model="newChannel.name" type="text" placeholder="Ex: Segredos do Cosmo" class="form-input" />
        </div>

        <div style="display: flex; justify-content: flex-end; gap: 1rem; margin-top: 2rem;">
          <button @click="showCreateChannelModal = false" class="btn btn-secondary" :disabled="isGeneratingChannel">Cancelar</button>
          <button @click="createChannel" class="btn btn-primary" :disabled="isGeneratingChannel" style="min-width: 140px;">
            <span v-if="isGeneratingChannel" class="spinner"></span>
            <span v-else>Conceber Canal</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style>
/* Local Component UI Elements styling overrides */
.auth-screen {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 1.5rem;
}

.auth-card {
  width: 100%;
  max-width: 380px;
}

.channel-logo-avatar {
  width: 55px;
  height: 55px;
  border-radius: 50%;
  background-size: cover;
  background-position: center;
  border: 2px solid var(--border-color);
  background-color: var(--bg-tertiary);
}

.channel-logo-avatar-placeholder {
  width: 55px;
  height: 55px;
  border-radius: 50%;
  background: var(--bg-tertiary);
  border: 2px solid var(--border-color);
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 700;
  color: var(--text-muted);
}

.video-card-thumbnail {
  width: 100px;
  height: 60px;
  border-radius: 10px;
  background-size: cover;
  background-position: center;
  flex-shrink: 0;
  position: relative;
  border: 1px solid var(--border-color);
}

.play-overlay {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.4);
  display: flex;
  align-items: center;
  justify-content: center;
  opacity: 0;
  transition: opacity 0.3s;
  border-radius: 10px;
}

.card:hover .play-overlay {
  opacity: 1;
}

.scene-image-preview {
  width: 100%;
  height: 160px;
  border-radius: 10px;
  background-size: cover;
  background-position: center;
  border: 1px solid var(--border-color);
  margin-top: 0.5rem;
}

/* Spinner Animation */
.spinner {
  display: inline-block;
  width: 18px;
  height: 18px;
  border: 2px solid rgba(255, 255, 255, 0.3);
  border-radius: 50%;
  border-top-color: white;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}
</style>
