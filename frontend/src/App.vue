<script setup>
import { ref, reactive, onMounted, computed } from 'vue'

const API_BASE = window.location.hostname ? `http://${window.location.hostname}:8081/api` : 'http://localhost:8081/api'
const MEDIA_BASE = window.location.hostname ? `http://${window.location.hostname}:8081/media` : 'http://localhost:8081/media'

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
  channels.value = []
  activeChannel.value = null
  activeVideo.value = null
  if (renderPollingId.value) {
    clearInterval(renderPollingId.value)
    renderPollingId.value = null
  }
  if (videoListPollingId.value) {
    clearTimeout(videoListPollingId.value)
    videoListPollingId.value = null
  }
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
    const data = await res.json()
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

// Load channels on mount if already logged in
onMounted(() => {
  if (authToken.value) fetchChannels()
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
  if (activeChannel.value && !activeVideo.value && videos.value.some(v => v.status === 'generating')) {
    videoListPollingId.value = setTimeout(() => fetchVideos(channelId), 4000)
  }
}

const stopVideoListPolling = () => {
  if (videoListPollingId.value) {
    clearTimeout(videoListPollingId.value)
    videoListPollingId.value = null
  }
}

const selectChannel = async (channel) => {
  activeChannel.value = channel
  activeVideo.value = null
  showBookendsPage.value = false
  narratorVoiceSelection.value = channel.narrator_voice_id || ''
  narratorVoiceSaved.value = false
  introScriptInput.value = channel.intro_script || ''
  outroScriptInput.value = channel.outro_script || ''
  await fetchVideos(channel.id)
  await fetchChannelCosts(channel.id)
  await fetchAutoPilotStatus(channel.id)
  await fetchNarratorVoices()
  if (channel.intro_status === 'generating' || channel.outro_status === 'generating') {
    startChannelPolling(channel.id)
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
    if (res.ok) {
      const updated = await res.json()
      activeChannel.value = updated
      narratorVoiceSaved.value = true
      await fetchChannels()
    }
  } catch (err) {
    console.error('Error updating narrator voice:', err)
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
  const labels = { openai: 'OpenAI', gemini: 'Gemini', elevenlabs: 'ElevenLabs', pexels: 'Pexels' }
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

const selectVideo = async (video) => {
  stopVideoListPolling()
  try {
    const res = await apiFetch(`/videos/${video.id}`)
    if (res.ok) {
      activeVideo.value = await res.json()
      // Setup scheduler date if already set
      if (activeVideo.value.scheduled_for) {
        scheduleDate.value = new Date(activeVideo.value.scheduled_for).toISOString().slice(0, 16)
      } else {
        scheduleDate.value = ''
      }
      
      // Start polling if video is in rendering/generating state
      if (activeVideo.value.status === 'generating') {
        startRenderPolling(activeVideo.value.id)
      }
    }
  } catch (err) {
    console.error('Error fetching video:', err)
  }
}

const goBackToChannel = () => {
  activeVideo.value = null
  showBookendsPage.value = false
  if (renderPollingId.value) {
    clearInterval(renderPollingId.value)
    renderPollingId.value = null
  }
  fetchVideos(activeChannel.value.id)
}

// Actions
const createAutoChannel = async () => {
  isGeneratingChannel.value = true
  try {
    const res = await apiFetch('/channels', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: '',
        niche: 'Curiosidades e Mistérios'
      })
    })
    if (res.ok) {
      const created = await res.json()
      await fetchChannels()
      selectChannel(created)
    }
  } catch (err) {
    alert('Erro ao gerar o canal autônomo por IA.')
  } finally {
    isGeneratingChannel.value = false
  }
}

const createChannel = async () => {
  isGeneratingChannel.value = true
  try {
    const res = await apiFetch('/channels', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: newChannel.name,
        niche: newChannel.niche
      })
    })
    if (res.ok) {
      const created = await res.json()
      await fetchChannels()
      showCreateChannelModal.value = false
      newChannel.name = ''
      // select the newly created channel
      selectChannel(created)
    }
  } catch (err) {
    alert('Erro ao gerar canal por IA. Certifique-se de que o backend está ativo.')
  } finally {
    isGeneratingChannel.value = false
  }
}

const generateBranding = async () => {
  if (!activeChannel.value) return
  isGeneratingChannel.value = true
  try {
    const res = await apiFetch(`/channels/${activeChannel.value.id}/generate-branding`, {
      method: 'POST'
    })
    if (res.ok) {
      const updated = await res.json()
      activeChannel.value = updated
      await fetchChannels()
    }
  } catch (err) {
    alert('Erro ao gerar a identidade visual.')
  } finally {
    isGeneratingChannel.value = false
  }
}

const generateLogo = async () => {
  if (!activeChannel.value) return
  isGeneratingLogo.value = true
  try {
    const res = await apiFetch(`/channels/${activeChannel.value.id}/generate-logo`, {
      method: 'POST'
    })
    if (res.ok) {
      const updated = await res.json()
      activeChannel.value = updated
      await fetchChannels()
    }
  } catch (err) {
    alert('Erro ao gerar logotipo.')
  } finally {
    isGeneratingLogo.value = false
  }
}

const generateBanner = async () => {
  if (!activeChannel.value) return
  isGeneratingBanner.value = true
  try {
    const res = await apiFetch(`/channels/${activeChannel.value.id}/generate-banner`, {
      method: 'POST'
    })
    if (res.ok) {
      const updated = await res.json()
      activeChannel.value = updated
      await fetchChannels()
    }
  } catch (err) {
    alert('Erro ao gerar a capa do canal.')
  } finally {
    isGeneratingBanner.value = false
  }
}

const generateIntro = async () => {
  if (!activeChannel.value) return
  isGeneratingIntro.value = true
  try {
    const res = await apiFetch(`/channels/${activeChannel.value.id}/generate-intro`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ script: introScriptInput.value }),
    })
    if (res.ok) {
      const updated = await res.json()
      activeChannel.value = updated
      introScriptInput.value = updated.intro_script || ''
      startChannelPolling(activeChannel.value.id)
      await fetchChannels()
    } else {
      const data = await res.json()
      alert(data.error || 'Erro ao gerar abertura.')
    }
  } catch (err) {
    alert('Erro de conexão ao gerar abertura.')
  } finally {
    isGeneratingIntro.value = false
  }
}

const generateOutro = async () => {
  if (!activeChannel.value) return
  isGeneratingOutro.value = true
  try {
    const res = await apiFetch(`/channels/${activeChannel.value.id}/generate-outro`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ script: outroScriptInput.value }),
    })
    if (res.ok) {
      const updated = await res.json()
      activeChannel.value = updated
      outroScriptInput.value = updated.outro_script || ''
      startChannelPolling(activeChannel.value.id)
      await fetchChannels()
    } else {
      const data = await res.json()
      alert(data.error || 'Erro ao gerar encerramento.')
    }
  } catch (err) {
    alert('Erro de conexão ao gerar encerramento.')
  } finally {
    isGeneratingOutro.value = false
  }
}

const deleteIntro = async () => {
  if (!activeChannel.value || !confirm('Tem certeza que deseja excluir a abertura deste canal?')) return
  isDeletingIntro.value = true
  try {
    const res = await apiFetch(`/channels/${activeChannel.value.id}/intro`, {
      method: 'DELETE',
    })
    if (res.ok) {
      const updated = await res.json()
      activeChannel.value = updated
      introScriptInput.value = ''
      await fetchChannels()
    } else {
      const data = await res.json()
      alert(data.error || 'Erro ao excluir abertura.')
    }
  } catch (err) {
    alert('Erro de conexão ao excluir abertura.')
  } finally {
    isDeletingIntro.value = false
  }
}

const deleteOutro = async () => {
  if (!activeChannel.value || !confirm('Tem certeza que deseja excluir o encerramento deste canal?')) return
  isDeletingOutro.value = true
  try {
    const res = await apiFetch(`/channels/${activeChannel.value.id}/outro`, {
      method: 'DELETE',
    })
    if (res.ok) {
      const updated = await res.json()
      activeChannel.value = updated
      outroScriptInput.value = ''
      await fetchChannels()
    } else {
      const data = await res.json()
      alert(data.error || 'Erro ao excluir encerramento.')
    }
  } catch (err) {
    alert('Erro de conexão ao excluir encerramento.')
  } finally {
    isDeletingOutro.value = false
  }
}

const downloadMediaFile = async (path, defaultFilename) => {
  if (!path) return
  try {
    const fullUrl = formatRelativePath(path)
    const response = await fetch(fullUrl)
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
  isGeneratingVideo.value = true
  try {
    const res = await apiFetch(`/channels/${activeChannel.value.id}/videos`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        topic: newVideoTopic.value
      })
    })
    if (res.ok) {
      const createdVideo = await res.json()
      newVideoTopic.value = ''
      await fetchVideos(activeChannel.value.id)
      selectVideo(createdVideo)
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
    if (res.ok) {
      const data = await res.json()
      activeChannel.value.autopilot_enabled = data.autopilot_enabled
    }
  } catch (err) {
    console.error('Error toggling autopilot:', err)
  }
}

const triggerRender = async () => {
  if (!activeVideo.value) return
  try {
    const res = await apiFetch(`/videos/${activeVideo.value.id}/render`, {
      method: 'POST'
    })
    if (res.ok) {
      activeVideo.value.status = 'generating'
      startRenderPolling(activeVideo.value.id)
    }
  } catch (err) {
    console.error('Error triggering render:', err)
  }
}

const startRenderPolling = (videoId) => {
  if (renderPollingId.value) clearInterval(renderPollingId.value)
  renderPollingId.value = setInterval(async () => {
    try {
      const res = await apiFetch(`/videos/${videoId}`)
      if (res.ok) {
        const updated = await res.json()
        if (activeVideo.value && activeVideo.value.id === videoId) {
          activeVideo.value = updated
          if (updated.status !== 'generating') {
            clearInterval(renderPollingId.value)
            renderPollingId.value = null
          }
        }
      }
    } catch (err) {
      console.error('Error polling render status:', err)
    }
  }, 3000)
}

const scheduleVideo = async () => {
  if (!scheduleDate.value) return
  try {
    const res = await apiFetch(`/videos/${activeVideo.value.id}/schedule`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        scheduled_for: new Date(scheduleDate.value).toISOString()
      })
    })
    if (res.ok) {
      activeVideo.value = await res.json()
      alert('Vídeo agendado com sucesso!')
    }
  } catch (err) {
    alert('Erro ao agendar o vídeo.')
  }
}

const deleteVideo = async (video, event) => {
  if (event) event.stopPropagation()
  if (!confirm(`Excluir o vídeo "${video.title}" e todos os seus arquivos (imagens, áudio, vídeo renderizado)? Essa ação não pode ser desfeita.`)) return
  try {
    const res = await apiFetch(`/videos/${video.id}`, { method: 'DELETE' })
    if (res.ok) {
      if (activeVideo.value && activeVideo.value.id === video.id) {
        goBackToChannel()
      } else if (activeChannel.value) {
        await fetchVideos(activeChannel.value.id)
      }
    } else {
      alert('Erro ao excluir o vídeo.')
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
  youtube_configured: false,
  tiktok_configured: false,
})
const settingsForm = reactive({
  openai_api_key: '',
  gemini_api_key: '',
  elevenlabs_api_key: '',
  youtube_client_id: '',
  youtube_client_secret: '',
  tiktok_client_id: '',
  tiktok_client_secret: '',
})
const isSavingSettings = ref(false)

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
      alert('Erro ao salvar as chaves.')
    }
  } catch (err) {
    alert('Erro ao salvar as chaves.')
  } finally {
    isSavingSettings.value = false
  }
}

// Calendar Calculation
const allScheduledVideos = ref([])

const fetchAllVideosForCalendar = async () => {
  try {
    const allVideos = []
    for (const ch of channels.value) {
      const res = await apiFetch(`/channels/${ch.id}/videos`)
      if (res.ok) {
        const vList = await res.json()
        vList.forEach(v => {
          if (v.scheduled_for) {
            allVideos.push({
              ...v,
              channelName: ch.name
            })
          }
        })
      }
    }
    allScheduledVideos.value = allVideos
  } catch (err) {
    console.error('Error loading calendar videos:', err)
  }
}

const calendarDays = computed(() => {
  const days = []
  const today = new Date()
  const year = today.getFullYear()
  const month = today.getMonth()

  // First day of current month
  const firstDay = new Date(year, month, 1)
  const startDayOfWeek = firstDay.getDay() // 0 = Sunday, 1 = Monday etc.

  // Total days in current month
  const totalDays = new Date(year, month + 1, 0).getDate()

  // Prepend empty slots for padding
  for (let i = 0; i < startDayOfWeek; i++) {
    days.push({ dayNum: null, date: null, posts: [] })
  }

  // Populate days
  for (let day = 1; day <= totalDays; day++) {
    const dateObj = new Date(year, month, day)
    
    // Filter posts for this specific day
    const posts = allScheduledVideos.value.filter(v => {
      const schedDate = new Date(v.scheduled_for)
      return schedDate.getDate() === day &&
             schedDate.getMonth() === month &&
             schedDate.getFullYear() === year
    })

    days.push({
      dayNum: day,
      date: dateObj,
      isToday: day === today.getDate(),
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
  return months[new Date().getMonth()] + ' ' + new Date().getFullYear()
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
  return `${MEDIA_BASE}/${normalized.replace('media/', '')}`
}

const formatNicheLabel = (niche) => {
  return niche.substring(0, 1).toUpperCase() + niche.substring(1)
}

const getStatusBadgeClass = (status) => {
  return `badge badge-${status}`
}
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

      <div style="display: flex; gap: 0.5rem; margin-bottom: 1.5rem;">
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
          <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 2rem;">
            <div>
              <h1>Canais Gerados</h1>
              <p style="color: var(--text-secondary);">Cadastre e gerencie a identidade visual e o conteúdo de cada canal.</p>
            </div>
            <div style="display: flex; gap: 0.75rem;">
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
            <div v-for="ch in channels" :key="ch.id" @click="selectChannel(ch)" class="card" style="cursor: pointer; display: flex; flex-direction: column; gap: 1rem;">
              <div style="display: flex; align-items: center; gap: 1rem;">
                <div v-if="ch.logo_path" class="channel-logo-avatar" :style="{ backgroundImage: `url(${formatRelativePath(ch.logo_path)})` }"></div>
                <div v-else class="channel-logo-avatar-placeholder">{{ ch.name.substring(0, 2).toUpperCase() }}</div>
                <div>
                  <h3 style="font-weight: 700; margin-bottom: 0.25rem;">{{ ch.name }}</h3>
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
          <div style="display: flex; align-items: center; gap: 0.5rem; margin-bottom: 1.5rem; color: var(--text-secondary); cursor: pointer;" @click="goBackToChannels">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <line x1="19" y1="12" x2="5" y2="12"/><polyline points="12 19 5 12 12 5"/>
            </svg>
            Voltar para canais
          </div>

          <!-- Channel Branding Banner Card -->
          <div class="channel-banner-preview" :style="{ backgroundImage: activeChannel.banner_path ? `url(${formatRelativePath(activeChannel.banner_path)})` : 'linear-gradient(135deg, #1e1b4b 0%, #311042 100%)' }">
            <div class="channel-logo-overlay" :style="{ backgroundImage: activeChannel.logo_path ? `url(${formatRelativePath(activeChannel.logo_path)})` : 'none' }">
              <span v-if="!activeChannel.logo_path" style="display:flex; height:100%; align-items:center; justify-content:center; font-weight:800; font-size:1.5rem; color:var(--text-muted);">
                {{ activeChannel.name.substring(0, 2).toUpperCase() }}
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

          <div style="margin-top: 1rem; display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 3rem;">
            <div style="max-width: 60%;">
              <h1 style="font-size: 2rem;">{{ activeChannel.name }}</h1>
              <p style="color: var(--accent-purple); font-weight: 600; margin-bottom: 0.5rem;">Nicho: {{ formatNicheLabel(activeChannel.niche) }}</p>
              <p style="color: var(--text-secondary); font-size: 0.95rem; line-height: 1.5;">{{ activeChannel.concept }}</p>

              <!-- AI Branding & Config Badges Box -->
              <div style="margin-top: 1.25rem; background: rgba(15, 23, 42, 0.6); border: 1px solid var(--border-color); padding: 1.25rem; border-radius: 14px;">
                <h4 style="font-weight: 700; font-size: 0.95rem; margin-bottom: 1rem; color: var(--accent-cyan); display: flex; align-items: center; gap: 0.5rem;">
                  ⚙️ Configurações & Persona Escolhidas pela IA
                </h4>
                <div style="display: grid; grid-template-columns: repeat(3, 1fr); gap: 1rem;">
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
              <div style="margin-top: 1.5rem; background: rgba(139, 92, 246, 0.08); border: 1px solid rgba(139, 92, 246, 0.2); padding: 1rem 1.25rem; border-radius: 14px; display: flex; align-items: center; justify-content: space-between; gap: 1rem;">
                <div>
                  <h4 style="font-weight: 700; font-size: 0.95rem; color: var(--text-primary); display: flex; align-items: center; gap: 0.5rem;">
                    <span style="width: 8px; height: 8px; border-radius: 50%;" :style="{ background: activeChannel.autopilot_enabled ? 'var(--success)' : 'var(--text-muted)' }"></span>
                    Piloto Automático por IA (Auto-Pilot)
                  </h4>
                  <p style="font-size: 0.8rem; color: var(--text-secondary); margin-top: 0.25rem;">
                    {{ activeChannel.autopilot_enabled ? 'Ativo: A IA analiza métricas do YouTube/TikTok, escolhe o tema otimizado, busca vídeos HD sem marca d\'água, renderiza na nuvem e agenda postagens sem intervenção humana.' : 'Inativo: Ative para permitir que a IA gerencie a linha editorial e publique vídeos 100% no automático.' }}
                  </p>
                </div>
                <button @click="toggleAutoPilot" class="btn" :class="activeChannel.autopilot_enabled ? 'btn-primary' : 'btn-secondary'" style="flex-shrink: 0;">
                  {{ activeChannel.autopilot_enabled ? 'Piloto Ativo' : 'Ativar Piloto Automático' }}
                </button>
              </div>

              <!-- Channel OAuth Integrations Bar -->
              <div style="margin-top: 1rem; background: var(--bg-secondary); border: 1px solid var(--border-color); padding: 1rem 1.25rem; border-radius: 14px; display: flex; align-items: center; justify-content: space-between; gap: 1rem;">
                <div>
                  <h4 style="font-weight: 700; font-size: 0.95rem; color: var(--text-primary);">Contas de Postagem Vinculadas</h4>
                  <p style="font-size: 0.8rem; color: var(--text-secondary); margin-top: 0.25rem;">
                    Conecte a conta do YouTube e do TikTok deste canal para habilitar postagens automáticas.
                  </p>
                </div>
                <div style="display: flex; gap: 0.75rem; flex-shrink: 0;">
                  <a :href="`${API_BASE}/channels/${activeChannel.id}/auth/youtube`" target="_blank" class="btn btn-secondary" style="font-size: 0.85rem;">
                    <span v-if="activeChannel.yt_connected" style="color: var(--success);">✓ YouTube Conectado</span>
                    <span v-else>+ Conectar YouTube</span>
                  </a>
                  <a :href="`${API_BASE}/channels/${activeChannel.id}/auth/tiktok`" target="_blank" class="btn btn-secondary" style="font-size: 0.85rem;">
                    <span v-if="activeChannel.tiktok_connected" style="color: var(--success);">✓ TikTok Conectado</span>
                    <span v-else>+ Conectar TikTok</span>
                  </a>
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

                <div style="display: grid; grid-template-columns: 200px 1fr; gap: 1.25rem; align-items: stretch;">
                  <!-- Logo Card -->
                  <div style="background: rgba(15, 23, 42, 0.8); border: 1px solid var(--border-color); padding: 1rem; border-radius: 12px; display: flex; flex-direction: column; align-items: center; justify-content: space-between; text-align: center;">
                    <div style="width: 100%;">
                      <span style="font-size: 0.75rem; color: var(--text-muted); display: block; margin-bottom: 0.75rem; font-weight: 600;">Logotipo do Canal</span>
                      <div style="width: 110px; height: 110px; margin: 0 auto 0.75rem auto; border-radius: 50%; overflow: hidden; border: 2px solid var(--border-color); background-size: cover; background-position: center; background-color: #1e1b4b;"
                           :style="{ backgroundImage: activeChannel.logo_path ? `url(${formatRelativePath(activeChannel.logo_path)})` : 'none' }">
                        <span v-if="!activeChannel.logo_path" style="display:flex; height:100%; align-items:center; justify-content:center; font-weight:800; font-size:1.5rem; color:var(--text-muted);">
                          {{ activeChannel.name.substring(0, 2).toUpperCase() }}
                        </span>
                      </div>
                    </div>

                    <div style="width: 100%; display: flex; flex-direction: column; gap: 0.5rem; margin-top: 0.5rem;">
                      <button @click="generateLogo" :disabled="isGeneratingLogo" class="btn btn-secondary" style="width: 100%; font-size: 0.8rem; display: flex; align-items: center; justify-content: center; gap: 0.4rem; border-color: var(--accent-cyan); color: var(--accent-cyan);">
                        <span v-if="isGeneratingLogo" class="spinner"></span>
                        <span v-else>🎨 {{ activeChannel.logo_path ? 'Regerar Logo' : 'Gerar Logo' }}</span>
                      </button>

                      <button v-if="activeChannel.logo_path" @click="downloadMediaFile(activeChannel.logo_path, `logo-${activeChannel.name.toLowerCase().replace(/\s+/g, '_')}.png`)"
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
                  <div style="background: rgba(15, 23, 42, 0.8); border: 1px solid var(--border-color); padding: 1rem; border-radius: 12px; display: flex; flex-direction: column; justify-content: space-between;">
                    <div>
                      <span style="font-size: 0.75rem; color: var(--text-muted); display: block; margin-bottom: 0.75rem; font-weight: 600;">Capa do Canal (Banner 16:9)</span>
                      <div style="width: 100%; height: 130px; margin-bottom: 0.75rem; border-radius: 10px; overflow: hidden; border: 1px solid var(--border-color); background-size: cover; background-position: center; background-color: #1e1b4b;"
                           :style="{ backgroundImage: activeChannel.banner_path ? `url(${formatRelativePath(activeChannel.banner_path)})` : 'linear-gradient(135deg, #1e1b4b 0%, #311042 100%)' }">
                      </div>
                    </div>

                    <div style="display: flex; justify-content: space-between; align-items: center; gap: 1rem; margin-top: 0.5rem;">
                      <button @click="generateBanner" :disabled="isGeneratingBanner" class="btn btn-secondary" style="font-size: 0.8rem; display: flex; align-items: center; gap: 0.4rem; border-color: var(--accent-cyan); color: var(--accent-cyan);">
                        <span v-if="isGeneratingBanner" class="spinner"></span>
                        <span v-else>🎨 {{ activeChannel.banner_path ? 'Regerar Capa' : 'Gerar Capa' }}</span>
                      </button>

                      <button v-if="activeChannel.banner_path" @click="downloadMediaFile(activeChannel.banner_path, `capa-${activeChannel.name.toLowerCase().replace(/\s+/g, '_')}.png`)"
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
              <div @click="showBookendsPage = true" class="card" style="cursor: pointer; margin-top: 1.25rem; background: linear-gradient(135deg, rgba(6, 182, 212, 0.08) 0%, rgba(139, 92, 246, 0.08) 100%); border: 1px solid rgba(6, 182, 212, 0.3); padding: 1.25rem; border-radius: 14px; display: flex; align-items: center; justify-content: space-between; gap: 1rem;">
                <div style="display: flex; align-items: center; gap: 1rem;">
                  <div style="width: 44px; height: 44px; border-radius: 12px; background: rgba(6, 182, 212, 0.15); display: flex; align-items: center; justify-content: center; font-size: 1.4rem;">
                    🎬
                  </div>
                  <div>
                    <h3 style="font-size: 1.05rem; font-weight: 700; color: #f4f4f5; display: flex; align-items: center; gap: 0.6rem;">
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
                <button class="btn btn-primary" style="flex-shrink: 0; background: linear-gradient(135deg, #06b6d4 0%, #3b82f6 100%); border: none;">
                  Gerenciar Abertura e Encerramento →
                </button>
              </div>
            </div>
            
            <div class="card" style="width: 380px; padding: 1.25rem;">
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

            <div v-if="channelCosts.gemini_free_tier" style="margin-bottom: 1.25rem; padding: 0.75rem 1rem; border-radius: 10px; display: flex; justify-content: space-between; align-items: center;"
                 :style="{ background: channelCosts.gemini_free_tier.limit_reached ? 'rgba(248, 113, 113, 0.1)' : 'rgba(139, 92, 246, 0.08)', border: channelCosts.gemini_free_tier.limit_reached ? '1px solid rgba(248, 113, 113, 0.3)' : '1px solid rgba(139, 92, 246, 0.2)' }">
              <span style="font-size: 0.85rem;">
                Cota gratuita do Gemini hoje: <strong>{{ channelCosts.gemini_free_tier.requests_today }}/{{ channelCosts.gemini_free_tier.daily_limit }}</strong> requisições
              </span>
              <span v-if="channelCosts.gemini_free_tier.limit_reached" style="font-size: 0.75rem; color: #f87171; font-weight: 700;">
                LIMITE ATINGIDO - caindo para OpenAI (pago)
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
            <div v-for="v in videos" :key="v.id" @click="selectVideo(v)" class="card" style="cursor: pointer; display: flex; align-items: center; gap: 1.5rem; padding: 1.25rem;">
              <div class="video-card-thumbnail" :style="{ backgroundImage: v.thumbnail_path ? `url(${formatRelativePath(v.thumbnail_path)})` : 'linear-gradient(135deg, #18181b 0%, #27272a 100%)' }">
                <div class="play-overlay">
                  <svg width="24" height="24" viewBox="0 0 24 24" fill="currentColor">
                    <polygon points="5 3 19 12 5 21 5 3"/>
                  </svg>
                </div>
              </div>
              
              <div style="flex-grow: 1; display: flex; flex-direction: column; gap: 0.5rem;">
                <div style="display: flex; justify-content: space-between; align-items: flex-start;">
                  <h4 style="font-weight: 700; font-size: 1.05rem; display: -webkit-box; -webkit-line-clamp: 1; -webkit-box-orient: vertical; overflow: hidden; max-width: 75%;">{{ v.title }}</h4>
                  <span style="display: flex; align-items: center; gap: 0.4rem;">
                    <span v-if="v.total_scene_count > 0 && v.degraded_scene_count > 0"
                          :title="v.degraded_scene_count + ' de ' + v.total_scene_count + ' cenas usaram conteúdo de reserva'"
                          :style="{ fontSize: '0.7rem', color: (v.degraded_scene_count / v.total_scene_count > 0.3) ? 'var(--danger)' : 'var(--warning)' }">⚠️</span>
                    <span :class="getStatusBadgeClass(v.status)">{{ v.status === 'generating' ? `${v.render_progress || 0}%` : v.status }}</span>
                  </span>
                </div>
                <p style="font-size: 0.8rem; color: var(--text-muted); display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;">
                  {{ v.description }}
                </p>
                <div style="display: flex; justify-content: space-between; font-size: 0.75rem; color: var(--text-muted); margin-top: 0.5rem;">
                  <span>Cenas: {{ v.scenes ? v.scenes.length : 'N/A' }}</span>
                  <span v-if="v.scheduled_for">Agendado: {{ new Date(v.scheduled_for).toLocaleString() }}</span>
                  <span v-else>Rascunho</span>
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
            <p style="color: var(--text-muted);">Nenhum vídeo gerado ainda. Digite um tema no painel lateral para gerar o primeiro roteiro.</p>
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
                    <button @click="downloadMediaFile(activeChannel.intro_video_path, `abertura-${activeChannel.name.toLowerCase().replace(/\s+/g, '_')}.mp4`)"
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
                <button @click="generateIntro" :disabled="isGeneratingIntro" class="btn btn-primary" style="flex: 1; background: linear-gradient(135deg, #06b6d4 0%, #3b82f6 100%); border: none;">
                  <span v-if="isGeneratingIntro" class="spinner"></span>
                  <span v-else>⚡ {{ activeChannel.intro_video_path ? 'Regerar Abertura' : 'Gerar Abertura' }}</span>
                </button>
                <button v-if="activeChannel.intro_video_path || activeChannel.intro_script" @click="deleteIntro" :disabled="isDeletingIntro" class="btn btn-secondary" style="border-color: var(--danger); color: var(--danger);">
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
                    <button @click="downloadMediaFile(activeChannel.outro_video_path, `encerramento-${activeChannel.name.toLowerCase().replace(/\s+/g, '_')}.mp4`)"
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
                <button @click="generateOutro" :disabled="isGeneratingOutro" class="btn btn-primary" style="flex: 1; background: linear-gradient(135deg, #8b5cf6 0%, #06b6d4 100%); border: none;">
                  <span v-if="isGeneratingOutro" class="spinner"></span>
                  <span v-else>⚡ {{ activeChannel.outro_video_path ? 'Regerar Encerramento' : 'Gerar Encerramento' }}</span>
                </button>
                <button v-if="activeChannel.outro_video_path || activeChannel.outro_script" @click="deleteOutro" :disabled="isDeletingOutro" class="btn btn-secondary" style="border-color: var(--danger); color: var(--danger);">
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

          <div style="display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 2rem;">
            <div>
              <span :class="getStatusBadgeClass(activeVideo.status)" style="margin-bottom: 0.5rem;">{{ activeVideo.status }}</span>
              <h1 style="font-size: 1.85rem; margin-top: 0.25rem;">{{ activeVideo.title }}</h1>
            </div>
            
            <div style="display: flex; gap: 0.75rem;">
              <button v-if="activeVideo.status === 'draft' || (activeVideo.status === 'failed' && activeVideo.scenes && activeVideo.scenes.length > 0)" @click="triggerRender" class="btn btn-cyan">
                {{ activeVideo.status === 'failed' ? 'Tentar Renderizar Novamente' : 'Gerar Vídeo Completo (FFmpeg)' }}
              </button>
              
              <div v-if="activeVideo.status === 'ready' || activeVideo.status === 'scheduled'" style="display: flex; gap: 0.75rem;">
                <a v-if="activeVideo.video_path" :href="formatRelativePath(activeVideo.video_path)" download target="_blank" class="btn btn-secondary">
                  Download Vídeo (16:9)
                </a>
                <a v-if="activeVideo.shorts_path" :href="formatRelativePath(activeVideo.shorts_path)" download target="_blank" class="btn btn-secondary">
                  Download Shorts (9:16)
                </a>
                <a v-if="activeVideo.subtitles_path" :href="formatRelativePath(activeVideo.subtitles_path)" download target="_blank" class="btn btn-secondary">
                  Download Legendas (.srt)
                </a>
                <span v-if="activeVideo.media_purged" style="font-size: 0.8rem; color: var(--text-muted); align-self: center;">
                  Arquivo removido do armazenamento após publicação
                </span>
              </div>

              <button @click="deleteVideo(activeVideo)" class="btn btn-secondary" style="color: #f87171; border-color: rgba(248, 113, 113, 0.3);">
                Excluir Vídeo
              </button>
            </div>
          </div>

          <!-- Error message: never shown before, so a failed/blocked video gave no clue why -->
          <div v-if="activeVideo.error_message" class="card" style="margin-bottom: 1.25rem; padding: 1rem 1.25rem; border: 1px solid rgba(239, 68, 68, 0.35); background: rgba(239, 68, 68, 0.08);">
            <div style="display: flex; gap: 0.6rem; align-items: flex-start;">
              <span style="font-size: 1.1rem; line-height: 1;">⚠️</span>
              <div>
                <strong style="color: var(--danger); font-size: 0.85rem;">Erro</strong>
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
            <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 1.25rem;">
              <div>
                <h3 style="font-size: 1.15rem; font-weight: 800; color: #f4f4f5; display: flex; align-items: center; gap: 0.6rem;">
                  <span style="font-size: 1.3rem;">📊</span> Scorecard de Viralidade & Análise PlaySquad
                </h3>
                <p style="font-size: 0.8rem; color: var(--text-secondary); margin-top: 0.2rem;">
                  Pontuação técnica baseada em Força de Gancho, Retenção dos 2 Minutos, CTR e Originalidade.
                </p>
              </div>
              <div style="text-align: right; background: rgba(139, 92, 246, 0.15); border: 1px solid rgba(139, 92, 246, 0.4); padding: 0.5rem 1rem; border-radius: 12px;">
                <span style="font-size: 1.8rem; font-weight: 900; color: var(--accent-cyan);">{{ activeVideo.viral_score || 92 }}/100</span>
                <span style="font-size: 0.75rem; display: block; color: var(--success); font-weight: 700;">POTENCIAL VIRAL EXCELENTE</span>
              </div>
            </div>

            <!-- Scorecard Progress Bars -->
            <div style="display: grid; grid-template-columns: repeat(4, 1fr); gap: 1rem; margin-bottom: 1.25rem;">
              <div style="background: rgba(255,255,255,0.03); padding: 0.85rem 1rem; border-radius: 10px; border: 1px solid rgba(255,255,255,0.05);">
                <div style="display: flex; justify-content: space-between; font-size: 0.8rem; margin-bottom: 0.4rem;">
                  <span style="color: var(--text-muted);">⚡ Força do Hook (0-15s)</span>
                  <strong style="color: var(--accent-cyan);">{{ activeVideo.hook_power_score || 95 }}/100</strong>
                </div>
                <div style="width: 100%; height: 6px; background: rgba(255,255,255,0.1); border-radius: 3px; overflow: hidden;">
                  <div :style="{ width: (activeVideo.hook_power_score || 95) + '%' }" style="height: 100%; background: var(--accent-cyan);"></div>
                </div>
              </div>

              <div style="background: rgba(255,255,255,0.03); padding: 0.85rem 1rem; border-radius: 10px; border: 1px solid rgba(255,255,255,0.05);">
                <div style="display: flex; justify-content: space-between; font-size: 0.8rem; margin-bottom: 0.4rem;">
                  <span style="color: var(--text-muted);">⏱️ Retenção Est. (AVD)</span>
                  <strong style="color: var(--accent-purple);">{{ activeVideo.retention_score || 90 }}/100</strong>
                </div>
                <div style="width: 100%; height: 6px; background: rgba(255,255,255,0.1); border-radius: 3px; overflow: hidden;">
                  <div :style="{ width: (activeVideo.retention_score || 90) + '%' }" style="height: 100%; background: var(--accent-purple);"></div>
                </div>
              </div>

              <div style="background: rgba(255,255,255,0.03); padding: 0.85rem 1rem; border-radius: 10px; border: 1px solid rgba(255,255,255,0.05);">
                <div style="display: flex; justify-content: space-between; font-size: 0.8rem; margin-bottom: 0.4rem;">
                  <span style="color: var(--text-muted);">🧲 Potencial CTR</span>
                  <strong style="color: #f59e0b;">{{ activeVideo.ctr_score || 94 }}/100</strong>
                </div>
                <div style="width: 100%; height: 6px; background: rgba(255,255,255,0.1); border-radius: 3px; overflow: hidden;">
                  <div :style="{ width: (activeVideo.ctr_score || 94) + '%' }" style="height: 100%; background: #f59e0b;"></div>
                </div>
              </div>

              <div style="background: rgba(255,255,255,0.03); padding: 0.85rem 1rem; border-radius: 10px; border: 1px solid rgba(255,255,255,0.05);">
                <div style="display: flex; justify-content: space-between; font-size: 0.8rem; margin-bottom: 0.4rem;">
                  <span style="color: var(--text-muted);">🛡️ Monetização / Anti-Slop</span>
                  <strong style="color: var(--success);">{{ activeVideo.originality_score || 98 }}/100</strong>
                </div>
                <div style="width: 100%; height: 6px; background: rgba(255,255,255,0.1); border-radius: 3px; overflow: hidden;">
                  <div :style="{ width: (activeVideo.originality_score || 98) + '%' }" style="height: 100%; background: var(--success);"></div>
                </div>
              </div>
            </div>

            <div v-if="activeVideo.score_notes" style="background: rgba(0,0,0,0.3); border: 1px dashed rgba(255,255,255,0.1); padding: 0.75rem 1rem; border-radius: 8px; font-size: 0.82rem; color: var(--text-secondary);">
              💡 <strong>Análise de Inteligência:</strong> {{ activeVideo.score_notes }}
            </div>
          </div>

          <!-- Render Progress Bar: real, step-based progress reported by the server (renders run entirely server-side) -->
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

          <div class="grid-cols-3" style="align-items: flex-start;">
            <!-- Left Side: Script and Metadata details -->
            <div class="card" style="grid-column: span 2; display: flex; flex-direction: column; gap: 1.5rem;">
              <div>
                <h3 style="font-size: 1.1rem; font-weight: 700; margin-bottom: 0.75rem; border-bottom: 1px solid var(--border-color); padding-bottom: 0.5rem;">Metadados Gerados por IA</h3>
                <div class="form-group">
                  <label class="form-label">Título do Vídeo</label>
                  <input v-model="activeVideo.title" type="text" class="form-input" />
                </div>
                <div class="form-group">
                  <label class="form-label">Descrição SEO</label>
                  <textarea v-model="activeVideo.description" rows="4" class="form-textarea"></textarea>
                </div>
                <div class="form-group">
                  <label class="form-label">Tags (separadas por vírgula)</label>
                  <input v-model="activeVideo.tags" type="text" class="form-input" />
                </div>
              </div>

              <div>
                <h3 style="font-size: 1.1rem; font-weight: 700; margin-bottom: 0.75rem; border-bottom: 1px solid var(--border-color); padding-bottom: 0.5rem;">Roteiro Integro</h3>
                <p style="font-size: 0.95rem; color: var(--text-secondary); line-height: 1.6; white-space: pre-line; background: var(--bg-tertiary); padding: 1.25rem; border-radius: 12px; border: 1px solid var(--border-color);">
                  {{ activeVideo.script }}
                </p>
              </div>
            </div>

            <!-- Right Side: Scheduling and Video player preview -->
            <div style="display: flex; flex-direction: column; gap: 1.5rem;">
              <!-- Scheduler Card -->
              <div v-if="activeVideo.status === 'ready' || activeVideo.status === 'scheduled'" class="card" style="border-color: rgba(6, 182, 212, 0.2);">
                <h3 style="font-size: 1.1rem; font-weight: 700; margin-bottom: 1rem; color: var(--accent-cyan);">Agendar Publicação</h3>
                <p style="font-size: 0.8rem; color: var(--text-secondary); margin-bottom: 1rem;">Selecione o horário para postagem automatizada no TikTok e YouTube.</p>
                <div class="form-group">
                  <label class="form-label">Data e Hora de Postagem</label>
                  <input v-model="scheduleDate" type="datetime-local" class="form-input" />
                </div>
                <button @click="scheduleVideo" class="btn btn-cyan" style="width: 100%;">
                  {{ activeVideo.status === 'scheduled' ? 'Atualizar Agendamento' : 'Agendar Postagem' }}
                </button>
              </div>

              <div class="card">
                <h3 style="font-size: 1.1rem; font-weight: 700; margin-bottom: 1rem;">Estrutura de Cenas</h3>
                
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
            <h3 style="font-size: 1.3rem; font-weight: 800; color: var(--accent-purple);">{{ getMonthName() }}</h3>
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
                  <div v-for="post in day.posts" :key="post.id" class="calendar-post-badge" :class="post.status === 'posted' ? 'badge-posted' : 'badge-scheduled'" :title="post.title">
                    [{{ post.channelName }}] {{ post.title }}
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
            <p style="font-size: 0.85rem; color: var(--text-muted); margin-bottom: 1.25rem;">Forneça chaves da OpenAI ou Google Gemini. Se vazias, o sistema utilizará simulações mockadas de teste.</p>

            <div class="form-group">
              <label class="form-label">Google Gemini API Key <span v-if="settingsStatus.gemini_configured" style="color: var(--success);">(configurada)</span></label>
              <input v-model="settingsForm.gemini_api_key" type="password" placeholder="AIzaSy..." class="form-input" />
            </div>

            <div class="form-group">
              <label class="form-label">OpenAI API Key (para DALL-E e GPT) <span v-if="settingsStatus.openai_configured" style="color: var(--success);">(configurada)</span></label>
              <input v-model="settingsForm.openai_api_key" type="password" placeholder="sk-..." class="form-input" />
            </div>
          </div>

          <div>
            <h3 style="font-weight: 700; margin-bottom: 1rem; border-bottom: 1px solid var(--border-color); padding-bottom: 0.5rem;">Narração (Vozes)</h3>
            <div class="form-group">
              <label class="form-label">ElevenLabs API Key (Vozes de Alta Fidelidade) <span v-if="settingsStatus.elevenlabs_configured" style="color: var(--success);">(configurada)</span></label>
              <input v-model="settingsForm.elevenlabs_api_key" type="password" placeholder="Entrar ElevenLabs Key..." class="form-input" />
            </div>
          </div>

          <div>
            <h3 style="font-weight: 700; margin-bottom: 1rem; border-bottom: 1px solid var(--border-color); padding-bottom: 0.5rem;">Integrações de Postagem</h3>
            <p style="font-size: 0.85rem; color: var(--text-muted); margin-bottom: 1.25rem;">Defina os IDs de aplicativos de desenvolvedor para permitir o login OAuth2 no YouTube e TikTok.</p>

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
          </div>

          <div style="display: flex; justify-content: flex-end; gap: 1rem;">
            <button :disabled="isSavingSettings" @click="saveSettings" class="btn btn-primary">
              <span v-if="isSavingSettings" class="spinner"></span>
              <span v-else>Salvar Chaves</span>
            </button>
          </div>
        </div>
      </section>
    </main>

    <!-- Modal: Conception of Channel by AI -->
    <div v-if="showCreateChannelModal" class="modal-backdrop">
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
