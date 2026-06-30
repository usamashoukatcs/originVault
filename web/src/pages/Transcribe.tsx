import { useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'

interface TranscribeResult {
  text: string
  duration: number
  language: string
}

export default function Transcribe() {
  const navigate = useNavigate()
  const inputRef = useRef<HTMLInputElement>(null)
  const [file, setFile] = useState<File | null>(null)
  const [dragging, setDragging] = useState(false)
  const [loading, setLoading] = useState(false)
  const [result, setResult] = useState<TranscribeResult | null>(null)
  const [error, setError] = useState('')
  const [copied, setCopied] = useState(false)

  function handleFile(f: File) {
    setFile(f)
    setResult(null)
    setError('')
  }

  function onDrop(e: React.DragEvent) {
    e.preventDefault()
    setDragging(false)
    const f = e.dataTransfer.files[0]
    if (f) handleFile(f)
  }

  async function transcribe() {
    if (!file) return
    setLoading(true)
    setError('')
    setResult(null)
    try {
      const form = new FormData()
      form.append('file', file)
      const res = await fetch('/api/transcribe', { method: 'POST', body: form })
      if (!res.ok) {
        const err = await res.json().catch(() => ({ error: res.statusText }))
        throw new Error(err.error || `HTTP ${res.status}`)
      }
      const data: TranscribeResult = await res.json()
      setResult(data)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Transcription failed')
    } finally {
      setLoading(false)
    }
  }

  function copyText() {
    if (!result) return
    navigator.clipboard.writeText(result.text)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  function downloadTxt() {
    if (!result) return
    const blob = new Blob([result.text], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = (file?.name.replace(/\.[^.]+$/, '') ?? 'transcript') + '.txt'
    a.click()
    URL.revokeObjectURL(url)
  }

  const wordCount = result ? result.text.trim().split(/\s+/).filter(Boolean).length : 0
  const fileSizeMB = file ? (file.size / 1024 / 1024).toFixed(1) : ''
  const durationMin = result ? Math.floor(result.duration / 60) : 0
  const durationSec = result ? Math.round(result.duration % 60) : 0

  return (
    <div className="min-h-screen flex flex-col">
      <header className="border-b border-gray-800 px-8 py-4 flex items-center gap-4">
        <button className="text-gray-400 hover:text-white text-sm" onClick={() => navigate('/')}>
          ← Back
        </button>
        <div>
          <h1 className="text-lg font-semibold text-white">Audio Transcription</h1>
          <p className="text-xs text-gray-500">Powered by OpenAI Whisper</p>
        </div>
      </header>

      <main className="flex-1 px-8 py-8 max-w-3xl mx-auto w-full space-y-6">

        {/* Upload area */}
        <div
          className={`border-2 border-dashed rounded-xl p-10 text-center cursor-pointer transition-colors ${
            dragging ? 'border-brand-500 bg-brand-500/10' : 'border-gray-700 hover:border-gray-500'
          }`}
          onClick={() => inputRef.current?.click()}
          onDragOver={e => { e.preventDefault(); setDragging(true) }}
          onDragLeave={() => setDragging(false)}
          onDrop={onDrop}
        >
          <input
            ref={inputRef}
            type="file"
            accept="audio/*,video/*,.mp3,.mp4,.m4a,.aac,.wav,.ogg,.webm,.flac"
            className="hidden"
            onChange={e => e.target.files?.[0] && handleFile(e.target.files[0])}
          />
          {file ? (
            <div className="space-y-1">
              <p className="text-white font-medium">{file.name}</p>
              <p className="text-sm text-gray-400">{fileSizeMB} MB</p>
              <p className="text-xs text-gray-600 mt-2">Click to choose a different file</p>
            </div>
          ) : (
            <div className="space-y-2">
              <p className="text-4xl">🎙</p>
              <p className="text-gray-300 font-medium">Drop audio file here or click to browse</p>
              <p className="text-sm text-gray-500">Supports MP3, AAC, M4A, WAV, OGG, FLAC, MP4 — max 25MB</p>
            </div>
          )}
        </div>

        {/* Transcribe button */}
        {file && !result && (
          <button
            className="btn-primary w-full py-3 text-base justify-center"
            disabled={loading}
            onClick={transcribe}
          >
            {loading ? (
              <span className="flex items-center gap-3 justify-center">
                <span className="animate-spin text-xl">⟳</span>
                Transcribing... this may take a minute
              </span>
            ) : 'Transcribe Audio'}
          </button>
        )}

        {/* Error */}
        {error && (
          <div className="bg-red-950 border border-red-800 rounded-lg px-4 py-3 text-red-300 text-sm">
            {error}
          </div>
        )}

        {/* Result */}
        {result && (
          <div className="space-y-4">
            {/* Stats */}
            <div className="flex items-center gap-4 text-sm">
              <span className="bg-green-900/50 text-green-300 px-3 py-1 rounded-full">
                ✓ Transcribed
              </span>
              <span className="text-gray-400">{wordCount.toLocaleString()} words</span>
              <span className="text-gray-400">{durationMin}m {durationSec}s</span>
              {result.language && (
                <span className="text-gray-400 capitalize">Language: {result.language}</span>
              )}
            </div>

            {/* Action buttons */}
            <div className="flex gap-2">
              <button className="btn-primary text-sm" onClick={copyText}>
                {copied ? '✓ Copied!' : 'Copy Text'}
              </button>
              <button className="btn-secondary text-sm" onClick={downloadTxt}>
                Download .txt
              </button>
              <button
                className="btn-secondary text-sm ml-auto"
                onClick={() => { setResult(null); setFile(null) }}
              >
                Transcribe Another
              </button>
            </div>

            {/* Transcript */}
            <div className="card p-5">
              <p className="text-sm text-gray-300 leading-relaxed whitespace-pre-wrap">
                {result.text}
              </p>
            </div>
          </div>
        )}
      </main>
    </div>
  )
}
