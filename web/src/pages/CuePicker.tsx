import { useEffect, useRef, useState, useCallback } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api } from '../api'
import type { DashboardCue, Project, SearchResult } from '../types'

type Source = 'serp-google' | 'serp-bing' | 'serp-yahoo' | 'serp-youtube' | 'bing' | 'flickr' | 'google-cse' | 'google' | 'duckduckgo' | 'commons' | 'pexels' | 'pexels-photo' | 'pixabay-video' | 'pixabay-photo' | 'unsplash' | 'ai'

const SOURCES: { id: Source; label: string; badge?: string; group?: string }[] = [
  { id: 'serp-google',   label: 'Google Images',  badge: 'new', group: 'SerpAPI' },
  { id: 'serp-bing',     label: 'Bing Images',    group: 'SerpAPI' },
  { id: 'serp-yahoo',    label: 'Yahoo Images',   group: 'SerpAPI' },
  { id: 'serp-youtube',  label: 'YouTube',        group: 'SerpAPI' },
  { id: 'duckduckgo',    label: 'DuckDuckGo',     group: 'Free' },
  { id: 'commons',       label: 'Wikimedia',      group: 'Free' },
  { id: 'google',        label: 'Google (Serper)', group: 'Free' },
  { id: 'pexels-photo',  label: 'Pexels',         group: 'Stock' },
  { id: 'unsplash',      label: 'Unsplash',       group: 'Stock' },
  { id: 'pixabay-photo', label: 'Pixabay',        group: 'Stock' },
  { id: 'pexels',        label: 'Pexels (video)', group: 'Stock' },
  { id: 'pixabay-video', label: 'Pixabay (video)', group: 'Stock' },
  { id: 'flickr',        label: 'Flickr',         group: 'Stock' },
  { id: 'bing',          label: 'Bing (Azure)',   group: 'Stock' },
  { id: 'google-cse',    label: 'Google CSE',     group: 'Stock' },
  { id: 'ai',            label: 'AI Generate',    group: 'AI' },
]

const AI_MODELS = [
  { id: 'flux-dev',    label: 'Flux Dev (fal.ai)' },
  { id: 'imagen3',     label: 'Imagen 3 (Google)' },
  { id: 'gpt-image-1', label: 'GPT-Image-1 (OpenAI)' },
]

export default function CuePicker() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()

  const [project, setProject] = useState<Project | null>(null)
  const [loading, setLoading] = useState(true)
  const [polling, setPolling] = useState(false)
  const [statusLog, setStatusLog] = useState<string[]>([])

  const [cueIdx, setCueIdx] = useState(0)
  const [source, setSource] = useState<Source>('serp-google')
  const [query, setQuery] = useState('')

  // Per-source result cache — switching tabs preserves results
  const [resultCache, setResultCache] = useState<Partial<Record<Source, SearchResult[]>>>({})
  const [errorCache, setErrorCache]   = useState<Partial<Record<Source, string>>>({})
  const [hasMoreCache, setHasMoreCache] = useState<Partial<Record<Source, boolean>>>({})
  const [loadingMore, setLoadingMore] = useState(false)
  const [searching, setSearching] = useState(false)
  const [selecting, setSelecting] = useState(false)
  const sentinelRef = useRef<HTMLDivElement>(null)

  // AI generation state
  const [aiPrompt, setAiPrompt]       = useState('')
  const [aiModel, setAiModel]         = useState('flux-dev')
  const [aiGenerating, setAiGenerating] = useState(false)
  const [aiError, setAiError]         = useState('')

  const pollRef   = useRef<ReturnType<typeof setInterval> | null>(null)
  const scriptRef = useRef<HTMLDivElement>(null)
  const pageRef   = useRef<Partial<Record<Source, number>>>({})

  const searchResults = resultCache[source] ?? []
  const searchError   = errorCache[source] ?? ''

  // load project
  useEffect(() => {
    if (!id) return
    api.getProject(id)
      .then(p => {
        setProject(p)
        setLoading(false)
        if (p.status === 'generating' || p.status === 'pending') startPolling()
      })
      .catch(() => setLoading(false))
  }, [id])

  const startPolling = useCallback(() => {
    if (pollRef.current) return
    setPolling(true)
    pollRef.current = setInterval(async () => {
      if (!id) return
      try {
        const { status, log } = await api.getStatus(id)
        setStatusLog(log ?? [])
        if (status !== 'generating' && status !== 'pending') {
          clearInterval(pollRef.current!)
          pollRef.current = null
          setPolling(false)
          const p = await api.getProject(id)
          setProject(p)
        }
      } catch { /* ignore */ }
    }, 2000)
  }, [id])

  useEffect(() => () => { if (pollRef.current) clearInterval(pollRef.current) }, [])

  // When cue changes: reset search query + clear all caches (new cue = fresh search)
  useEffect(() => {
    if (!project?.cues?.length) return
    const cue = project.cues[cueIdx]
    const defaultQuery = cue?.image_query || cue?.keyword || ''
    setQuery(defaultQuery)
    setAiPrompt(defaultQuery)
    setResultCache({})
    setErrorCache({})
    setAiError('')
    pageRef.current = {}
  }, [cueIdx, project?.id])

  // highlight current phrase in script panel
  useEffect(() => {
    if (!scriptRef.current || !project?.cues?.length) return
    const cue = project.cues[cueIdx]
    if (!cue?.phrase) return
    const el = scriptRef.current
    const raw = project.script || ""
    const esc = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")

    // Word-level match: tokenise both strings into word+position pairs,
    // ignoring all punctuation so smart-quotes, commas, apostrophes never break matching.
    const tokenize = (s: string) => {
      const toks: { w: string; start: number; end: number }[] = []
      const re = /[a-zA-Z0-9]+/g
      let m
      while ((m = re.exec(s)) !== null)
        toks.push({ w: m[0].toLowerCase(), start: m.index, end: m.index + m[0].length })
      return toks
    }
    const scriptToks = tokenize(raw)
    const phraseToks = tokenize(cue.phrase)
    const numCues = project.cues.length

    // Start search near where this cue should appear in the script (avoid matching
    // an earlier cue's text when the same words appear multiple times in the script).
    const estTokStart = Math.max(0, Math.floor((cueIdx / numCues) * scriptToks.length) - 30)

    const tryFind = (n: number, fromTok: number) => {
      const need = phraseToks.slice(0, n)
      if (need.length < 3) return null
      for (let i = fromTok; i <= scriptToks.length - need.length; i++) {
        if (need.every((t, j) => scriptToks[i + j].w === t.w))
          return { start: scriptToks[i].start, end: scriptToks[i + need.length - 1].end }
      }
      return null
    }

    let found: { start: number; end: number } | null = null
    // First pass: search forward from estimated position
    for (const n of [phraseToks.length, 10, 8, 6, 5, 4]) {
      found = tryFind(n, estTokStart)
      if (found) break
    }
    // Fallback: search from beginning (handles cues near the top or mis-estimated positions)
    if (!found) {
      for (const n of [phraseToks.length, 10, 8, 6, 5, 4]) {
        found = tryFind(n, 0)
        if (found) break
      }
    }

    if (!found) { el.textContent = raw; return }

    el.innerHTML =
      esc(raw.slice(0, found.start)) +
      `<mark class="bg-yellow-500/30 text-yellow-200 rounded px-0.5">${esc(raw.slice(found.start, found.end))}</mark>` +
      esc(raw.slice(found.end))
    el.querySelector("mark")?.scrollIntoView({ block: "center", behavior: "smooth" })
  }, [cueIdx, project?.cues])

  async function doSearch(customQuery?: string) {
    if (!project || source === 'ai') return
    const q = customQuery ?? query
    if (!q.trim()) return
    // Always reset to page 1 on a new search
    pageRef.current[source] = 1
    setSearching(true)
    setErrorCache(prev => ({ ...prev, [source]: '' }))
    setHasMoreCache(prev => ({ ...prev, [source]: false }))
    try {
      const res = await api.search({ source, query: q, page: 1 })
      setResultCache(prev => ({ ...prev, [source]: res.results }))
      // if we got a full page, assume there are more
      setHasMoreCache(prev => ({ ...prev, [source]: res.results.length >= 8 }))
      if (res.results.length === 0) {
        setErrorCache(prev => ({ ...prev, [source]: 'No results — try a different query or source.' }))
      }
    } catch (err: unknown) {
      setErrorCache(prev => ({ ...prev, [source]: `search failed: ${err instanceof Error ? err.message : err}` }))
    } finally {
      setSearching(false)
    }
  }

  async function doLoadMore() {
    if (!project || source === 'ai' || searching || loadingMore) return
    if (!hasMoreCache[source]) return
    const q = query.trim()
    if (!q) return
    const nextPage = (pageRef.current[source] ?? 1) + 1
    pageRef.current[source] = nextPage
    setLoadingMore(true)
    try {
      const res = await api.search({ source, query: q, page: nextPage })
      if (res.results.length > 0) {
        setResultCache(prev => ({ ...prev, [source]: [...(prev[source] ?? []), ...res.results] }))
        setHasMoreCache(prev => ({ ...prev, [source]: res.results.length >= 8 }))
      } else {
        setHasMoreCache(prev => ({ ...prev, [source]: false }))
      }
    } catch {
      // silently fail — user can scroll up and try again
    } finally {
      setLoadingMore(false)
    }
  }

  // IntersectionObserver: auto-load more when sentinel scrolls into view
  useEffect(() => {
    const sentinel = sentinelRef.current
    if (!sentinel) return
    const observer = new IntersectionObserver(
      (entries) => { if (entries[0].isIntersecting) doLoadMore() },
      { threshold: 0.1 }
    )
    observer.observe(sentinel)
    return () => observer.disconnect()
  }, [source, searching, loadingMore, hasMoreCache])

  async function selectResult(result: SearchResult) {
    if (!project || !id) return
    setSelecting(true)
    try {
      await api.selectClip(id, cueIdx, result)
      const p = await api.getProject(id)
      setProject(p)
    } catch (err: unknown) {
      alert(err instanceof Error ? err.message : 'Select failed')
    } finally {
      setSelecting(false)
    }
  }

  async function generateAI() {
    if (!id || !aiPrompt.trim()) return
    setAiGenerating(true)
    setAiError('')
    try {
      await api.generateAIImage(id, cueIdx, aiPrompt, aiModel)
      const p = await api.getProject(id)
      setProject(p)
    } catch (err: unknown) {
      setAiError(err instanceof Error ? err.message : 'Generation failed')
    } finally {
      setAiGenerating(false)
    }
  }

  async function clearSelection() {
    if (!project || !id) return
    await api.clearClip(id, cueIdx)
    const p = await api.getProject(id)
    setProject(p)
  }

  async function removeClipFromSeq(clipIdx: number) {
    if (!id) return
    await api.removeClipFromSequence(id, cueIdx, clipIdx)
    const p = await api.getProject(id)
    setProject(p)
  }

  const cue: DashboardCue | undefined = project?.cues?.[cueIdx]
  const totalCues = project?.cues?.length ?? 0
  const hasClip = (c: DashboardCue) => (c.selected_clips?.length ?? 0) > 0 || !!c.selected_clip_path
  const doneCount = project?.cues?.filter(hasClip).length ?? 0
  const cueSeqCount = (cue?.selected_clips?.length ?? 0) || (cue?.selected_clip_path ? 1 : 0)

  if (loading) {
    return <div className="min-h-screen flex items-center justify-center"><p className="text-gray-400">Loading project...</p></div>
  }

  if (project && (project.status === 'generating' || project.status === 'pending')) {
    return (
      <div className="min-h-screen flex flex-col">
        <header className="border-b border-gray-800 px-8 py-4 flex items-center gap-4">
          <button className="text-gray-400 hover:text-white text-sm" onClick={() => navigate('/')}>← Back</button>
          <h1 className="text-lg font-semibold text-white">Generating Script...</h1>
        </header>
        <main className="flex-1 flex flex-col items-center justify-center gap-6 px-8">
          <div className="flex items-center gap-3 text-yellow-400">
            <span className="animate-spin text-2xl">⟳</span>
            <span className="text-lg font-medium">Claude is writing your script</span>
          </div>
          <div className="card p-4 w-full max-w-lg max-h-64 overflow-y-auto font-mono text-xs text-gray-400 space-y-1">
            {statusLog.length === 0 && <p className="text-gray-600">Waiting for updates...</p>}
            {statusLog.map((line, i) => <p key={i}>{line}</p>)}
          </div>
        </main>
      </div>
    )
  }

  if (project?.status === 'error') {
    return (
      <div className="min-h-screen flex flex-col items-center justify-center gap-4">
        <p className="text-red-400 text-lg font-semibold">Script generation failed</p>
        <div className="card p-4 max-w-lg font-mono text-xs text-gray-400">
          {project.status_log?.slice(-10).map((l, i) => <p key={i}>{l}</p>)}
        </div>
        <button className="btn-secondary" onClick={() => navigate('/')}>← Home</button>
      </div>
    )
  }

  return (
    <div className="min-h-screen flex flex-col h-screen">
      {/* Header */}
      <header className="border-b border-gray-800 px-6 py-3 flex items-center gap-4 shrink-0">
        <button className="text-gray-400 hover:text-white text-sm" onClick={() => navigate('/')}>← Back</button>
        <div className="flex-1 min-w-0">
          <h1 className="text-base font-semibold text-white truncate">{project?.title || 'Cue Picker'}</h1>
          <p className="text-xs text-gray-500">{doneCount} of {totalCues} cues assigned</p>
        </div>
        {/* Progress dots */}
        <div className="hidden sm:flex items-center gap-1">
          {project?.cues?.map((c, i) => (
            <button
              key={i}
              onClick={() => setCueIdx(i)}
              title={`Cue ${i+1}: ${c.keyword}`}
              className={`w-2.5 h-2.5 rounded-full transition-colors ${
                i === cueIdx ? 'bg-brand-500 scale-125' :
                hasClip(c) ? 'bg-green-500' : 'bg-gray-600 hover:bg-gray-500'
              }`}
            />
          ))}
        </div>
        <button className="btn-primary shrink-0" onClick={() => navigate(`/projects/${id}/assembly`)}>
          Proceed to Assembly →
        </button>
      </header>

      {/* Main: script left | picker right */}
      <div className="flex-1 flex overflow-hidden">

        {/* Script panel */}
        <div className="w-80 shrink-0 border-r border-gray-800 flex flex-col overflow-hidden">
          <div className="px-4 py-3 border-b border-gray-800 shrink-0">
            <p className="text-xs font-semibold text-gray-400 uppercase tracking-wider">Script</p>
          </div>
          <div
            ref={scriptRef}
            className="flex-1 overflow-y-auto px-4 py-4 text-sm text-gray-300 leading-relaxed whitespace-pre-wrap"
          >
            {project?.script || ''}
          </div>
        </div>

        {/* Cue picker */}
        <div className="flex-1 flex flex-col overflow-hidden">

          {/* Cue nav + metadata */}
          <div className="px-5 py-3 border-b border-gray-800 shrink-0">
            <div className="flex items-center justify-between mb-2">
              <div className="flex items-center gap-3">
                <button
                  disabled={cueIdx === 0}
                  onClick={() => setCueIdx(i => i - 1)}
                  className="btn-secondary text-xs px-2.5 py-1 disabled:opacity-30"
                >
                  ← Prev
                </button>
                <span className="text-sm font-semibold text-white">Cue {cueIdx + 1} / {totalCues}</span>
                <button
                  disabled={cueIdx >= totalCues - 1}
                  onClick={() => setCueIdx(i => i + 1)}
                  className="btn-secondary text-xs px-2.5 py-1 disabled:opacity-30"
                >
                  Next →
                </button>
              </div>
              {cueSeqCount > 0 && (
                <button className="text-xs text-gray-500 hover:text-red-400" onClick={clearSelection}>
                  Clear all
                </button>
              )}
            </div>

            {cue && (
              <div className="flex flex-wrap gap-2 text-xs">
                {cue.visual_type && <span className="bg-gray-800 text-gray-400 px-2 py-0.5 rounded">{cue.visual_type}</span>}
                {cue.person  && <span className="bg-blue-950 text-blue-300 px-2 py-0.5 rounded">Person: {cue.person}</span>}
                {cue.company && <span className="bg-purple-950 text-purple-300 px-2 py-0.5 rounded">Company: {cue.company}</span>}
                {cue.product && <span className="bg-orange-950 text-orange-300 px-2 py-0.5 rounded">Product: {cue.product}</span>}
                <span className="text-gray-500 italic w-full">"{cue.phrase}"</span>
              </div>
            )}

            {/* Sequence filmstrip */}
            {cue && (
              <div className="flex items-center gap-2 mt-2">
                <span className="text-xs text-gray-500 shrink-0">Clips ({cueSeqCount}/3):</span>
                <div className="flex items-center gap-1.5">
                  {(cue.selected_clips ?? (cue.selected_clip_path ? [{ clip_path: cue.selected_clip_path, source: cue.selected_source, url: cue.selected_url, thumb_url: cue.selected_thumb_url }] : [])).map((clip, ci) => (
                    <div key={ci} className="relative group w-16 h-10 rounded overflow-hidden bg-gray-800 border border-gray-700 shrink-0">
                      {clip.thumb_url ? (
                        <img src={clip.thumb_url} alt="" className="w-full h-full object-cover" />
                      ) : (
                        <div className="w-full h-full flex items-center justify-center text-gray-600 text-[10px]">
                          {clip.source?.startsWith('ai-') ? 'AI' : '✓'}
                        </div>
                      )}
                      <button
                        className="absolute inset-0 flex items-center justify-center bg-black/0 group-hover:bg-black/70 transition-colors"
                        onClick={() => removeClipFromSeq(ci)}
                        title="Remove clip"
                      >
                        <span className="opacity-0 group-hover:opacity-100 text-white text-base font-bold leading-none">×</span>
                      </button>
                    </div>
                  ))}
                  {cueSeqCount < 3 && (
                    <div className="w-16 h-10 rounded border border-dashed border-gray-700 flex items-center justify-center text-gray-600 text-sm shrink-0">
                      +
                    </div>
                  )}
                </div>
              </div>
            )}
          </div>

          {/* Source tabs */}
          <div className="px-5 pt-3 pb-2 border-b border-gray-800 shrink-0 space-y-2">
            {/* Render tabs grouped by group label */}
            {(() => {
              const groups: string[] = []
              SOURCES.forEach(s => { if (s.group && !groups.includes(s.group)) groups.push(s.group) })
              return groups.map(group => (
                <div key={group} className="flex items-center gap-1 flex-wrap">
                  <span className="text-[10px] text-gray-600 uppercase tracking-wider w-14 shrink-0">{group}</span>
                  {SOURCES.filter(s => s.group === group).map(s => (
                    <button
                      key={s.id}
                      onClick={() => setSource(s.id)}
                      className={`text-xs px-3 py-1.5 rounded-md font-medium transition-colors ${
                        source === s.id
                          ? 'bg-brand-500 text-white'
                          : 'bg-gray-800 text-gray-400 hover:bg-gray-700'
                      }`}
                    >
                      {s.label}
                      {s.badge && (
                        <span className="ml-1.5 bg-green-700 text-green-200 px-1 py-0.5 rounded text-[10px] leading-none">
                          {s.badge}
                        </span>
                      )}
                      {s.id !== 'ai' && resultCache[s.id] && resultCache[s.id]!.length > 0 && (
                        <span className="ml-1 text-gray-500">({resultCache[s.id]!.length})</span>
                      )}
                    </button>
                  ))}
                </div>
              ))
            })()}

            {/* Search bar (hidden for AI tab) */}
            {source !== 'ai' && (
              <div className="space-y-1.5">
                <div className="flex gap-2">
                  <input
                    className="input text-xs flex-1"
                    placeholder={`Search ${source}...`}
                    value={query}
                    onChange={e => setQuery(e.target.value)}
                    onKeyDown={e => e.key === 'Enter' && doSearch()}
                  />
                  <button
                    className="btn-primary text-xs px-4 shrink-0"
                    disabled={searching || !query.trim()}
                    onClick={() => doSearch()}
                  >
                    {searching ? '...' : 'Search'}
                  </button>
                </div>
                {/* Quick-fill chips from cue metadata */}
                {cue && (cue.image_query || cue.keyword) && (
                  <div className="flex flex-wrap gap-1.5 items-center">
                    <span className="text-xs text-gray-600">Suggestions:</span>
                    {cue.image_query && (
                      <button
                        className="text-xs bg-gray-800 hover:bg-gray-700 text-gray-400 px-2 py-0.5 rounded truncate max-w-[280px]"
                        title={cue.image_query}
                        onClick={() => { setQuery(cue.image_query); doSearch(cue.image_query) }}
                      >
                        {cue.image_query.slice(0, 50)}{cue.image_query.length > 50 ? '…' : ''}
                      </button>
                    )}
                    {cue.keyword && cue.keyword !== cue.image_query && (
                      <button
                        className="text-xs bg-gray-800 hover:bg-gray-700 text-gray-500 px-2 py-0.5 rounded truncate max-w-[200px]"
                        title={cue.keyword}
                        onClick={() => { setQuery(cue.keyword); doSearch(cue.keyword) }}
                      >
                        {cue.keyword.slice(0, 40)}{cue.keyword.length > 40 ? '…' : ''}
                      </button>
                    )}
                  </div>
                )}
              </div>
            )}
          </div>

          {/* Results area */}
          <div className="flex-1 overflow-y-auto px-5 py-4">

            {/* AI Generate tab */}
            {source === 'ai' && (
              <div className="max-w-lg space-y-4">
                <p className="text-xs text-gray-500">
                  Generate a custom image using AI. The image will be automatically selected for this cue.
                  Takes 10–40 seconds depending on the model.
                </p>
                <div className="space-y-3">
                  <div>
                    <label className="block text-xs text-gray-400 mb-1">Prompt</label>
                    <textarea
                      className="input text-xs w-full resize-none"
                      rows={3}
                      value={aiPrompt}
                      onChange={e => setAiPrompt(e.target.value)}
                      placeholder="Describe the image you want to generate..."
                    />
                  </div>
                  <div>
                    <label className="block text-xs text-gray-400 mb-1">Model</label>
                    <select
                      className="input text-xs w-full"
                      value={aiModel}
                      onChange={e => setAiModel(e.target.value)}
                    >
                      {AI_MODELS.map(m => (
                        <option key={m.id} value={m.id}>{m.label}</option>
                      ))}
                    </select>
                  </div>
                  <button
                    className="btn-primary text-sm w-full justify-center py-2.5"
                    disabled={aiGenerating || !aiPrompt.trim()}
                    onClick={generateAI}
                  >
                    {aiGenerating ? (
                      <span className="flex items-center gap-2 justify-center">
                        <span className="animate-spin">⟳</span> Generating... (this takes ~20–40s)
                      </span>
                    ) : 'Generate & Select'}
                  </button>
                  {aiError && (
                    <div className="bg-red-950 border border-red-800 rounded-lg px-4 py-3 text-red-300 text-xs">
                      {aiError}
                    </div>
                  )}
                  {cue?.selected_source?.startsWith('ai-') && (
                    <div className="bg-green-950 border border-green-800 rounded-lg px-4 py-3">
                      <p className="text-green-300 text-xs font-medium">✓ AI image selected: {cue.selected_source}</p>
                      <p className="text-green-500 text-xs mt-0.5">{cue.selected_url}</p>
                    </div>
                  )}
                </div>
              </div>
            )}

            {/* Search results */}
            {source !== 'ai' && (
              <>
                {searching && (
                  <div className="flex items-center justify-center h-32 text-gray-500">
                    <span className="animate-spin mr-2">⟳</span> Searching...
                  </div>
                )}

                {!searching && searchError && (
                  <div className="text-sm text-red-400/80 text-center py-8 px-4">{searchError}</div>
                )}

                {!searching && searchResults.length === 0 && !searchError && (
                  <div className="text-sm text-gray-600 text-center py-12">
                    <p>Enter a query above and click Search.</p>
                  </div>
                )}

                {!searching && searchResults.length > 0 && (
                  <>
                    <div className="grid grid-cols-2 lg:grid-cols-3 gap-3">
                      {searchResults.map((result, i) => {
                        const isSelected = cue?.selected_clips?.some(c => c.thumb_url === result.thumb_url) ??
                          (cue?.selected_url === result.page_url && cue?.selected_source === result.source)
                        return (
                          <ResultCard
                            key={i}
                            result={result}
                            selected={isSelected}
                            selecting={selecting}
                            seqCount={cueSeqCount}
                            onSelect={() => selectResult(result)}
                          />
                        )
                      })}
                    </div>
                    {/* Infinite scroll sentinel */}
                    {hasMoreCache[source] && (
                      <div ref={sentinelRef} className="py-4 text-center">
                        {loadingMore
                          ? <p className="text-xs text-gray-500 animate-pulse">Loading more...</p>
                          : <p className="text-xs text-gray-700">Scroll for more</p>
                        }
                      </div>
                    )}
                    {!hasMoreCache[source] && searchResults.length > 0 && (
                      <p className="text-xs text-gray-700 text-center py-3">{searchResults.length} results</p>
                    )}
                  </>
                )}
              </>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

function ResultCard({
  result, selected, selecting, seqCount, onSelect
}: {
  result: SearchResult
  selected: boolean
  selecting: boolean
  seqCount: number
  onSelect: () => void
}) {
  const [imgErr, setImgErr] = useState(false)

  return (
    <div
      className={`relative rounded-lg overflow-hidden cursor-pointer border-2 transition-all group ${
        selected ? 'border-green-500 ring-2 ring-green-500/30' : 'border-gray-700 hover:border-brand-500'
      }`}
      onClick={selecting ? undefined : onSelect}
    >
      <div className="aspect-video bg-gray-800 relative">
        {result.is_video ? (
          <video
            src={result.thumb_url}
            className="w-full h-full object-cover"
            muted loop
            onMouseEnter={e => (e.currentTarget as HTMLVideoElement).play()}
            onMouseLeave={e => { (e.currentTarget as HTMLVideoElement).pause(); (e.currentTarget as HTMLVideoElement).currentTime = 0 }}
          />
        ) : imgErr ? (
          <div className="w-full h-full flex items-center justify-center text-gray-600 text-xs">No preview</div>
        ) : (
          <img src={result.thumb_url} alt={result.title} className="w-full h-full object-cover" onError={() => setImgErr(true)} />
        )}

        <div className="absolute inset-0 bg-black/0 group-hover:bg-black/30 transition-colors flex items-center justify-center">
          {!selected && (
            <span className="opacity-0 group-hover:opacity-100 transition-opacity text-white text-sm font-semibold bg-brand-500 px-3 py-1.5 rounded-lg">
              {selecting ? '...' : seqCount >= 3 ? 'Replace (3/3)' : `Add (${seqCount + 1}/3)`}
            </span>
          )}
          {selected && (
            <span className="text-green-400 text-xl font-bold bg-black/40 px-3 py-1 rounded-lg">✓ Selected</span>
          )}
        </div>

        {result.is_video && result.duration > 0 && (
          <div className="absolute bottom-1.5 right-1.5 bg-black/70 text-white text-xs px-1.5 py-0.5 rounded">
            {result.duration}s
          </div>
        )}
        {result.score > 0 && (
          <div className={`absolute top-1.5 left-1.5 text-xs px-1.5 py-0.5 rounded font-medium ${
            result.score >= 3 ? 'bg-green-900/80 text-green-300' :
            result.score >= 2 ? 'bg-yellow-900/80 text-yellow-300' :
            'bg-gray-800/80 text-gray-400'
          }`}>
            {result.score >= 3 ? '●●●' : result.score === 2 ? '●●○' : '●○○'}
          </div>
        )}
      </div>
      <div className="p-2 bg-gray-900">
        <p className="text-xs text-gray-400 truncate">{result.title || result.source}</p>
      </div>
    </div>
  )
}
