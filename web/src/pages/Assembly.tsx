import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api } from '../api'
import type { Overlay, Project, ThumbnailPromptResult } from '../types'

export default function Assembly() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()

  const [project, setProject] = useState<Project | null>(null)
  const [loading, setLoading] = useState(true)
  const [log, setLog] = useState<string[]>([])
  const [polling, setPolling] = useState(false)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const logEndRef = useRef<HTMLDivElement>(null)

  // Thumbnail prompt state
  const [thumbPrompt, setThumbPrompt] = useState<ThumbnailPromptResult | null>(null)
  const [thumbTheme, setThumbTheme] = useState<'red' | 'gold' | 'blue'>('red')
  const [generatingThumb, setGeneratingThumb] = useState(false)
  const [thumbError, setThumbError] = useState('')
  const [copied, setCopied] = useState<string>('')

  // Overlay editor state
  const [showOverlayForm, setShowOverlayForm] = useState(false)
  const [editingOverlay, setEditingOverlay] = useState<Overlay | null>(null)
  const [ovText, setOvText]           = useState('')
  const [ovX, setOvX]                 = useState(50)
  const [ovY, setOvY]                 = useState(85)
  const [ovStart, setOvStart]         = useState(0)
  const [ovEnd, setOvEnd]             = useState(5)
  const [ovSize, setOvSize]           = useState(40)
  const [ovColor, setOvColor]         = useState('white')
  const [ovBold, setOvBold]           = useState(true)
  const [ovBg, setOvBg]               = useState(0.6)
  const [savingOv, setSavingOv]       = useState(false)

  useEffect(() => {
    if (!id) return
    api.getProject(id)
      .then(p => { setProject(p); setLog(p.status_log ?? []); setLoading(false) })
      .catch(() => setLoading(false))
  }, [id])

  useEffect(() => {
    logEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [log])

  function startPolling() {
    if (pollRef.current) return
    setPolling(true)
    pollRef.current = setInterval(async () => {
      if (!id) return
      try {
        const { status, log: newLog } = await api.getStatus(id)
        setLog(newLog ?? [])
        if (status !== 'assembling' && status !== 'voicing') {
          clearInterval(pollRef.current!)
          pollRef.current = null
          setPolling(false)
          const p = await api.getProject(id)
          setProject(p)
        }
      } catch { /* ignore */ }
    }, 2000)
  }

  useEffect(() => () => { if (pollRef.current) clearInterval(pollRef.current) }, [])

  async function handleVoice() {
    if (!id) return
    await api.generateVoice(id)
    startPolling()
  }

  async function handleAssemble() {
    if (!id) return
    await api.assemble(id)
    startPolling()
  }

  // Overlay CRUD
  function openNewOverlay() {
    setEditingOverlay(null)
    setOvText(''); setOvX(50); setOvY(85); setOvStart(0); setOvEnd(5)
    setOvSize(40); setOvColor('white'); setOvBold(true); setOvBg(0.6)
    setShowOverlayForm(true)
  }

  function openEditOverlay(ov: Overlay) {
    setEditingOverlay(ov)
    setOvText(ov.text); setOvX(ov.x); setOvY(ov.y)
    setOvStart(ov.start_time); setOvEnd(ov.end_time)
    setOvSize(ov.fontsize); setOvColor(ov.color); setOvBold(ov.bold); setOvBg(ov.bg_opacity)
    setShowOverlayForm(true)
  }

  async function saveOverlay() {
    if (!id || !ovText.trim()) return
    setSavingOv(true)
    try {
      const payload = {
        text: ovText, x: ovX, y: ovY,
        start_time: ovStart, end_time: ovEnd,
        fontsize: ovSize, color: ovColor, bold: ovBold, bg_opacity: ovBg,
      }
      if (editingOverlay) {
        await api.updateOverlay(id, editingOverlay.id, payload)
      } else {
        await api.createOverlay(id, payload)
      }
      const p = await api.getProject(id)
      setProject(p)
      setShowOverlayForm(false)
    } finally {
      setSavingOv(false)
    }
  }

  async function deleteOverlay(oid: string) {
    if (!id || !confirm('Delete this overlay?')) return
    await api.deleteOverlay(id, oid)
    const p = await api.getProject(id)
    setProject(p)
  }

  async function handleGenerateThumbPrompt() {
    if (!id) return
    setGeneratingThumb(true)
    setThumbError('')
    setThumbPrompt(null)
    try {
      const result = await api.generateThumbnailPrompt(id, thumbTheme)
      setThumbPrompt(result)
    } catch (e: any) {
      setThumbError(e.message || 'Failed to generate prompt')
    } finally {
      setGeneratingThumb(false)
    }
  }

  function copyText(text: string, key: string) {
    navigator.clipboard.writeText(text)
    setCopied(key)
    setTimeout(() => setCopied(''), 2000)
  }

  if (loading) {
    return <div className="min-h-screen flex items-center justify-center"><p className="text-gray-400">Loading...</p></div>
  }

  const p = project
  const doneCount  = p?.cues?.filter(c => c.selected_clip_path).length ?? 0
  const totalCues  = p?.cues?.length ?? 0
  const unassigned = totalCues - doneCount
  const isRunning  = p?.status === 'assembling' || p?.status === 'voicing'

  return (
    <div className="min-h-screen flex flex-col">
      <header className="border-b border-gray-800 px-8 py-4 flex items-center gap-4">
        <button className="text-gray-400 hover:text-white text-sm" onClick={() => navigate(`/projects/${id}/cues`)}>
          ← Cue Picker
        </button>
        <div className="flex-1">
          <h1 className="text-lg font-semibold text-white">{p?.title || 'Assembly'}</h1>
          <p className="text-xs text-gray-500">
            {doneCount}/{totalCues} cues assigned
            {unassigned > 0 && <span className="text-yellow-500 ml-2">· {unassigned} will auto-download</span>}
          </p>
        </div>
        {p?.status === 'done' && (
          <a href={api.downloadURL(id!)} download className="btn-primary">
            Download Video
          </a>
        )}
      </header>

      <main className="flex-1 px-8 py-8 max-w-4xl mx-auto w-full space-y-6">

        {/* Script summary */}
        {p && (
          <div className="card p-5 space-y-3">
            <h2 className="text-sm font-semibold text-gray-300 uppercase tracking-wider">Script Summary</h2>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 text-sm">
              <div>
                <p className="text-gray-500 text-xs mb-0.5">Title</p>
                <p className="text-white font-medium">{p.title || '—'}</p>
              </div>
              <div>
                <p className="text-gray-500 text-xs mb-0.5">Summary</p>
                <p className="text-gray-300">{p.summary || '—'}</p>
              </div>
              {p.keywords?.length > 0 && (
                <div className="sm:col-span-2">
                  <p className="text-gray-500 text-xs mb-1">Keywords</p>
                  <div className="flex flex-wrap gap-1.5">
                    {p.keywords.map((k, i) => (
                      <span key={i} className="bg-gray-800 text-gray-400 text-xs px-2 py-0.5 rounded">{k}</span>
                    ))}
                  </div>
                </div>
              )}
            </div>
            {p.description && (
              <details className="text-sm">
                <summary className="text-gray-500 text-xs cursor-pointer hover:text-gray-300">Show description</summary>
                <pre className="mt-2 text-gray-400 text-xs whitespace-pre-wrap leading-relaxed">{p.description}</pre>
              </details>
            )}
          </div>
        )}

        {/* AI Thumbnail Prompt — Imagine Art */}
        {p?.script && (
          <div className="card p-5 space-y-4">
            {/* Header */}
            <div className="flex items-center justify-between flex-wrap gap-3">
              <div>
                <h2 className="text-sm font-semibold text-gray-300 uppercase tracking-wider">Thumbnail Prompt</h2>
                <p className="text-xs text-gray-500 mt-0.5">
                  Optimized for{' '}
                  <a href="https://www.imagine.art" target="_blank" rel="noopener noreferrer" className="text-blue-400 hover:underline">
                    Imagine Art ↗
                  </a>
                  {' '}· Set aspect ratio to 16:9 in the app
                </p>
              </div>
              <div className="flex items-center gap-2 flex-wrap">
                <span className="text-xs text-gray-500">Story tone:</span>
                {(['red', 'gold', 'blue'] as const).map(t => (
                  <button
                    key={t}
                    onClick={() => { setThumbTheme(t); setThumbPrompt(null) }}
                    className={`px-3 py-1 rounded text-xs font-medium transition-colors ${
                      thumbTheme === t
                        ? t === 'red' ? 'bg-red-800 text-red-200 ring-1 ring-red-600'
                          : t === 'gold' ? 'bg-yellow-800 text-yellow-200 ring-1 ring-yellow-600'
                          : 'bg-blue-800 text-blue-200 ring-1 ring-blue-600'
                        : 'bg-gray-800 text-gray-400 hover:bg-gray-700'
                    }`}
                  >
                    {t === 'red' ? 'Drama / Failure' : t === 'gold' ? 'Success / Rise' : 'Tech / Innovation'}
                  </button>
                ))}
                <button
                  className="btn-primary text-sm"
                  disabled={generatingThumb}
                  onClick={handleGenerateThumbPrompt}
                >
                  {generatingThumb ? 'Generating...' : thumbPrompt ? 'Regenerate' : 'Generate'}
                </button>
              </div>
            </div>

            {thumbError && <p className="text-red-400 text-sm">{thumbError}</p>}

            {generatingThumb && (
              <p className="text-gray-400 text-sm animate-pulse">Crafting your Imagine Art prompt...</p>
            )}

            {thumbPrompt && (
              <div className="space-y-4">

                {/* Concept overview */}
                <div className="bg-gray-900 rounded-lg px-4 py-3 border border-gray-700 grid grid-cols-1 sm:grid-cols-3 gap-2 text-xs">
                  <div>
                    <p className="text-gray-500 mb-0.5">Concept</p>
                    <p className="text-white font-semibold">{thumbPrompt.visual_concept}</p>
                  </div>
                  <div>
                    <p className="text-gray-500 mb-0.5">Subject</p>
                    <p className="text-gray-300">{thumbPrompt.focal_element}</p>
                  </div>
                  <div>
                    <p className="text-gray-500 mb-0.5">Mood</p>
                    <p className="text-gray-300">{thumbPrompt.color_mood}</p>
                  </div>
                </div>

                {/* Positive prompt */}
                <PromptBox
                  label="Positive Prompt"
                  sublabel="Paste into Imagine Art's main prompt field"
                  text={thumbPrompt.prompt}
                  copyKey="prompt"
                  copied={copied}
                  onCopy={copyText}
                />

                {/* Negative prompt */}
                <PromptBox
                  label="Negative Prompt"
                  sublabel="Paste into Imagine Art's negative prompt field"
                  text={thumbPrompt.negative_prompt}
                  copyKey="neg"
                  copied={copied}
                  onCopy={copyText}
                />

                {/* Text overlay */}
                <div className="bg-gray-900 rounded-lg px-4 py-3 border border-gray-700 space-y-2">
                  <div className="flex items-center justify-between">
                    <div>
                      <span className="text-xs font-semibold text-gray-300">Text Overlay</span>
                      <span className="text-xs text-gray-600 ml-2">Add this in Canva after generating the image</span>
                    </div>
                    <button
                      onClick={() => copyText(thumbPrompt.text_overlay, 'text')}
                      className="text-xs px-2 py-0.5 rounded bg-gray-800 hover:bg-gray-700 text-gray-400 hover:text-white transition-colors"
                    >
                      {copied === 'text' ? '✓ Copied' : 'Copy'}
                    </button>
                  </div>
                  <p
                    className="text-white font-black text-xl tracking-widest cursor-pointer select-all"
                    onClick={() => copyText(thumbPrompt.text_overlay, 'text')}
                  >
                    {thumbPrompt.text_overlay}
                  </p>
                  <p className="text-gray-500 text-xs">{thumbPrompt.text_placement}</p>
                </div>

                {/* Canva tips */}
                <details>
                  <summary className="text-xs text-gray-500 cursor-pointer hover:text-gray-300 select-none">
                    Canva finishing tips ▸
                  </summary>
                  <div className="mt-2 bg-gray-900 rounded-lg px-4 py-3 border border-gray-800">
                    <p className="text-gray-400 text-xs whitespace-pre-wrap leading-relaxed">{thumbPrompt.canva_tips}</p>
                  </div>
                </details>

                {/* Workflow reminder */}
                <div className="flex items-start gap-2 bg-blue-950/40 border border-blue-900/50 rounded-lg px-4 py-3 text-xs text-blue-300">
                  <span className="mt-0.5">ℹ</span>
                  <div className="space-y-0.5">
                    <p className="font-medium">Workflow: Imagine Art → download image → Canva → add text overlay → export 1280×720</p>
                    <p className="text-blue-400 opacity-75">In Imagine Art: set aspect ratio to 16:9, use Realistic Vision V5.1 or Imagine V5 model for best photorealism</p>
                  </div>
                </div>

              </div>
            )}
          </div>
        )}

        {/* Text Overlays */}
        <div className="card p-5 space-y-4">
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-semibold text-gray-300 uppercase tracking-wider">
              Text Overlays
            </h2>
            <button className="btn-secondary text-xs" onClick={openNewOverlay}>+ Add Overlay</button>
          </div>

          {p?.overlays?.length === 0 && (
            <p className="text-gray-600 text-sm">No overlays. Add text that will be burned into the video at specific timestamps.</p>
          )}

          {p?.overlays?.map(ov => (
            <div key={ov.id} className="flex items-center gap-3 bg-gray-800 rounded-lg px-4 py-3">
              <div className="flex-1 min-w-0">
                <p className="text-white text-sm font-medium truncate">{ov.text}</p>
                <p className="text-gray-500 text-xs mt-0.5">
                  {ov.start_time}s – {ov.end_time}s · pos ({ov.x}%, {ov.y}%) · {ov.fontsize}px · {ov.color}
                </p>
              </div>
              <button className="text-xs text-gray-400 hover:text-white" onClick={() => openEditOverlay(ov)}>Edit</button>
              <button className="text-xs text-gray-400 hover:text-red-400" onClick={() => deleteOverlay(ov.id)}>Delete</button>
            </div>
          ))}

          {/* Inline overlay form */}
          {showOverlayForm && (
            <div className="bg-gray-800 rounded-xl p-5 border border-gray-700 space-y-4">
              <h3 className="text-sm font-semibold text-white">{editingOverlay ? 'Edit Overlay' : 'New Overlay'}</h3>
              <div>
                <label className="block text-xs text-gray-400 mb-1">Text</label>
                <input className="input text-sm" value={ovText} onChange={e => setOvText(e.target.value)} placeholder="Subscribe!" />
              </div>
              <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
                <div>
                  <label className="block text-xs text-gray-400 mb-1">X position (%)</label>
                  <input type="number" className="input text-sm" min={0} max={100} value={ovX} onChange={e => setOvX(Number(e.target.value))} />
                </div>
                <div>
                  <label className="block text-xs text-gray-400 mb-1">Y position (%)</label>
                  <input type="number" className="input text-sm" min={0} max={100} value={ovY} onChange={e => setOvY(Number(e.target.value))} />
                </div>
                <div>
                  <label className="block text-xs text-gray-400 mb-1">Start (sec)</label>
                  <input type="number" className="input text-sm" min={0} value={ovStart} onChange={e => setOvStart(Number(e.target.value))} />
                </div>
                <div>
                  <label className="block text-xs text-gray-400 mb-1">End (sec)</label>
                  <input type="number" className="input text-sm" min={0} value={ovEnd} onChange={e => setOvEnd(Number(e.target.value))} />
                </div>
                <div>
                  <label className="block text-xs text-gray-400 mb-1">Font size (px)</label>
                  <input type="number" className="input text-sm" min={12} max={200} value={ovSize} onChange={e => setOvSize(Number(e.target.value))} />
                </div>
                <div>
                  <label className="block text-xs text-gray-400 mb-1">Color</label>
                  <select className="input text-sm" value={ovColor} onChange={e => setOvColor(e.target.value)}>
                    {['white','yellow','#FF4444','#00FF00','#00CCFF','#FF9900','#FF00CC'].map(c => (
                      <option key={c} value={c}>{c}</option>
                    ))}
                  </select>
                </div>
                <div>
                  <label className="block text-xs text-gray-400 mb-1">BG opacity</label>
                  <input type="number" className="input text-sm" min={0} max={1} step={0.1} value={ovBg} onChange={e => setOvBg(Number(e.target.value))} />
                </div>
                <div className="flex items-end">
                  <label className="flex items-center gap-2 text-sm text-gray-300 cursor-pointer">
                    <input type="checkbox" checked={ovBold} onChange={e => setOvBold(e.target.checked)} className="rounded" />
                    Bold
                  </label>
                </div>
              </div>
              <div className="flex gap-2 pt-1">
                <button className="btn-primary text-sm" disabled={savingOv || !ovText.trim()} onClick={saveOverlay}>
                  {savingOv ? 'Saving...' : 'Save Overlay'}
                </button>
                <button className="btn-secondary text-sm" onClick={() => setShowOverlayForm(false)}>Cancel</button>
              </div>
            </div>
          )}
        </div>

        {/* Assembly controls */}
        <div className="card p-5 space-y-4">
          <h2 className="text-sm font-semibold text-gray-300 uppercase tracking-wider">Assemble Video</h2>

          <div className="flex flex-wrap gap-3">
            {/* Generate voice button */}
            {(p?.status === 'script_done' || p?.status === 'pending') && (
              <button
                className="btn-secondary"
                disabled={isRunning}
                onClick={handleVoice}
              >
                1. Generate Voiceover
              </button>
            )}

            {/* Assemble button */}
            <button
              className="btn-primary"
              disabled={isRunning || p?.status === 'pending' || p?.status === 'generating'}
              onClick={handleAssemble}
            >
              {p?.status === 'voice_done' ? '2. Assemble Video' : 'Assemble (with voiceover)'}
            </button>
          </div>

          {p?.status === 'done' && (
            <div className="bg-green-950 border border-green-800 rounded-lg px-4 py-3 flex items-center justify-between">
              <div>
                <p className="text-green-300 font-semibold">Video is ready!</p>
                <p className="text-green-500 text-xs mt-0.5">{p.video_path}</p>
              </div>
              <a href={api.downloadURL(id!)} download className="btn-primary text-sm">
                Download .mp4
              </a>
            </div>
          )}

          {p?.status === 'error' && (
            <div className="bg-red-950 border border-red-800 rounded-lg px-4 py-3">
              <p className="text-red-300 font-semibold">Assembly failed</p>
              <p className="text-red-400 text-xs mt-1">Check the log below for details.</p>
            </div>
          )}

          {/* Status log */}
          {(log.length > 0 || isRunning) && (
            <div className="bg-gray-950 border border-gray-800 rounded-lg p-4 max-h-72 overflow-y-auto font-mono text-xs text-gray-400 space-y-0.5">
              {isRunning && <p className="text-yellow-400 animate-pulse">● Running...</p>}
              {log.map((line, i) => <p key={i}>{line}</p>)}
              <div ref={logEndRef} />
            </div>
          )}
        </div>

        {/* Cue overview table */}
        <div className="card overflow-hidden">
          <div className="px-5 py-3 border-b border-gray-800">
            <h2 className="text-sm font-semibold text-gray-300 uppercase tracking-wider">Cue Overview</h2>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="border-b border-gray-800 text-gray-500 text-left">
                  <th className="px-4 py-2">#</th>
                  <th className="px-4 py-2">Keyword</th>
                  <th className="px-4 py-2">Type</th>
                  <th className="px-4 py-2">Selected</th>
                </tr>
              </thead>
              <tbody>
                {p?.cues?.map((c, i) => (
                  <tr
                    key={i}
                    className="border-b border-gray-800/50 hover:bg-gray-800/30 cursor-pointer"
                    onClick={() => navigate(`/projects/${id}/cues`)}
                  >
                    <td className="px-4 py-2 text-gray-500">{i+1}</td>
                    <td className="px-4 py-2 text-gray-300 max-w-xs truncate">{c.keyword}</td>
                    <td className="px-4 py-2 text-gray-500">{c.visual_type || '—'}</td>
                    <td className="px-4 py-2">
                      {c.selected_clip_path ? (
                        <span className="text-green-400">✓ {c.selected_source}</span>
                      ) : (
                        <span className="text-gray-600">auto-download</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>

      </main>
    </div>
  )
}

function PromptBox({
  label, sublabel, text, copyKey, copied, onCopy,
}: {
  label: string
  sublabel: string
  text: string
  copyKey: string
  copied: string
  onCopy: (text: string, key: string) => void
}) {
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between">
        <div>
          <span className="text-xs font-semibold text-gray-300">{label}</span>
          <span className="text-xs text-gray-600 ml-2">{sublabel}</span>
        </div>
        <button
          onClick={() => onCopy(text, copyKey)}
          className="text-xs px-2 py-0.5 rounded bg-gray-800 hover:bg-gray-700 text-gray-400 hover:text-white transition-colors"
        >
          {copied === copyKey ? '✓ Copied' : 'Copy'}
        </button>
      </div>
      <div
        className="bg-gray-950 border border-gray-800 rounded-lg p-3 text-xs text-gray-300 leading-relaxed cursor-pointer hover:border-gray-600 transition-colors select-all"
        onClick={() => onCopy(text, copyKey)}
      >
        {text}
      </div>
    </div>
  )
}
