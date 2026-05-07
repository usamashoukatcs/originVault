import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api'

export default function NewProject() {
  const navigate = useNavigate()
  const [topic, setTopic] = useState('')
  const [subject, setSubject] = useState('')
  const [minLen, setMinLen] = useState(8)
  const [maxLen, setMaxLen] = useState(12)
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!topic.trim()) return
    setCreating(true)
    setError('')
    try {
      // 1. Create project record
      const proj = await api.createProject(topic.trim())
      // 2. Kick off async script generation
      await api.generateScript(proj.id, {
        subject: subject.trim() || undefined,
        video_length_min: minLen,
        video_length_max: maxLen,
      })
      // 3. Navigate to cue picker — it will poll until script_done
      navigate(`/projects/${proj.id}/cues`)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Failed to create project')
      setCreating(false)
    }
  }

  return (
    <div className="min-h-screen flex flex-col">
      <header className="border-b border-gray-800 px-8 py-4 flex items-center gap-4">
        <button className="text-gray-400 hover:text-white text-sm" onClick={() => navigate('/')}>
          ← Back
        </button>
        <h1 className="text-lg font-semibold text-white">New Project</h1>
      </header>

      <main className="flex-1 flex items-start justify-center px-8 py-12">
        <div className="w-full max-w-xl">
          <form onSubmit={handleSubmit} className="card p-8 space-y-6">
            <div>
              <h2 className="text-xl font-bold text-white mb-1">Create a new video project</h2>
              <p className="text-gray-500 text-sm">
                Claude will research the topic and write a script, then you pick the images.
              </p>
            </div>

            {/* Topic */}
            <div>
              <label className="block text-sm font-medium text-gray-300 mb-1.5">
                Niche / Topic Context
                <span className="text-gray-500 font-normal ml-1">(channel niche or broad theme)</span>
              </label>
              <input
                className="input"
                placeholder="e.g. untold origin stories of famous brands and inventions"
                value={topic}
                onChange={e => setTopic(e.target.value)}
                required
              />
            </div>

            {/* Optional subject */}
            <div>
              <label className="block text-sm font-medium text-gray-300 mb-1.5">
                Specific Subject
                <span className="text-gray-500 font-normal ml-1">(optional — leave blank to let AI choose)</span>
              </label>
              <input
                className="input"
                placeholder="e.g. 7-Eleven, Blockbuster, the invention of Post-it Notes"
                value={subject}
                onChange={e => setSubject(e.target.value)}
              />
            </div>

            {/* Video length */}
            <div className="grid grid-cols-2 gap-4">
              <div>
                <label className="block text-sm font-medium text-gray-300 mb-1.5">Min length (min)</label>
                <input
                  type="number"
                  className="input"
                  min={1}
                  max={20}
                  value={minLen}
                  onChange={e => setMinLen(Number(e.target.value))}
                />
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-300 mb-1.5">Max length (min)</label>
                <input
                  type="number"
                  className="input"
                  min={1}
                  max={20}
                  value={maxLen}
                  onChange={e => setMaxLen(Number(e.target.value))}
                />
              </div>
            </div>

            {error && (
              <div className="bg-red-950 border border-red-800 rounded-lg px-4 py-3 text-red-300 text-sm">
                {error}
              </div>
            )}

            <button
              type="submit"
              disabled={creating || !topic.trim()}
              className="btn-primary w-full justify-center py-2.5"
            >
              {creating ? (
                <>
                  <span className="animate-spin text-lg">⟳</span>
                  Creating project...
                </>
              ) : (
                'Create & Generate Script'
              )}
            </button>
          </form>
        </div>
      </main>
    </div>
  )
}
