import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api'
import type { Idea, IdeaStatus, ProjectSummary, ProjectStatus } from '../types'

const STATUS_LABEL: Record<ProjectStatus, string> = {
  pending:     'Pending',
  generating:  'Generating Script',
  script_done: 'Script Ready',
  voicing:     'Generating Voice',
  voice_done:  'Voice Ready',
  assembling:  'Assembling',
  done:        'Done',
  error:       'Error',
}

const STATUS_COLOR: Record<ProjectStatus, string> = {
  pending:     'bg-gray-700 text-gray-300',
  generating:  'bg-yellow-900 text-yellow-300',
  script_done: 'bg-blue-900 text-blue-300',
  voicing:     'bg-yellow-900 text-yellow-300',
  voice_done:  'bg-blue-900 text-blue-300',
  assembling:  'bg-yellow-900 text-yellow-300',
  done:        'bg-green-900 text-green-300',
  error:       'bg-red-900 text-red-300',
}

const IDEA_STATUS_COLOR: Record<IdeaStatus, string> = {
  idea:      'bg-gray-700 text-gray-300',
  expanding: 'bg-yellow-900 text-yellow-300',
  ready:     'bg-green-900 text-green-300',
  error:     'bg-red-900 text-red-300',
}

const IDEA_STATUS_LABEL: Record<IdeaStatus, string> = {
  idea:      'Idea',
  expanding: 'Generating...',
  ready:     'Ready',
  error:     'Error',
}

export default function Home() {
  const navigate = useNavigate()
  const [tab, setTab] = useState<'projects' | 'ideas'>('projects')

  // Projects
  const [projects, setProjects] = useState<ProjectSummary[]>([])
  const [loadingProjects, setLoadingProjects] = useState(true)

  // Ideas
  const [ideas, setIdeas] = useState<Idea[]>([])
  const [loadingIdeas, setLoadingIdeas] = useState(false)
  const [generatingIdeas, setGeneratingIdeas] = useState(false)
  const [ideaCount, setIdeaCount] = useState(5)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)

  // Load projects on mount
  useEffect(() => {
    api.listProjects()
      .then(setProjects)
      .catch(console.error)
      .finally(() => setLoadingProjects(false))
  }, [])

  // Load ideas when switching to ideas tab
  useEffect(() => {
    if (tab !== 'ideas') return
    setLoadingIdeas(true)
    api.listIdeas()
      .then(setIdeas)
      .catch(console.error)
      .finally(() => setLoadingIdeas(false))
  }, [tab])

  // Poll while any idea is expanding
  useEffect(() => {
    if (tab !== 'ideas') return
    const anyExpanding = ideas.some(i => i.status === 'expanding')
    if (!anyExpanding) {
      if (pollRef.current) {
        clearInterval(pollRef.current)
        pollRef.current = null
      }
      return
    }
    if (pollRef.current) return
    pollRef.current = setInterval(() => {
      api.listIdeas().then(setIdeas).catch(console.error)
    }, 3000)
    return () => {
      if (pollRef.current) { clearInterval(pollRef.current); pollRef.current = null }
    }
  }, [tab, ideas])

  async function generateIdeas() {
    setGeneratingIdeas(true)
    try {
      const updated = await api.generateIdeas(ideaCount)
      setIdeas(updated)
    } catch (e: unknown) {
      alert(e instanceof Error ? e.message : 'Failed to generate ideas')
    } finally {
      setGeneratingIdeas(false)
    }
  }

  async function expandIdea(id: string) {
    try {
      await api.expandIdea(id)
      setIdeas(prev => prev.map(i => i.id === id ? { ...i, status: 'expanding' as IdeaStatus } : i))
    } catch (e: unknown) {
      alert(e instanceof Error ? e.message : 'Failed to expand idea')
    }
  }

  async function deleteIdea(id: string) {
    if (!confirm('Delete this idea?')) return
    await api.deleteIdea(id)
    setIdeas(prev => prev.filter(i => i.id !== id))
  }

  async function createProjectFromIdea(id: string) {
    try {
      const p = await api.createProjectFromIdea(id)
      navigate(`/projects/${p.id}/cues`)
    } catch (e: unknown) {
      alert(e instanceof Error ? e.message : 'Failed to create project')
    }
  }

  async function delProject(id: string) {
    if (!confirm('Remove this project from the list? (files on disk are kept)')) return
    await api.deleteProject(id)
    setProjects(ps => ps.filter(p => p.id !== id))
  }

  function openProject(p: ProjectSummary) {
    const s = p.status as ProjectStatus
    if (s === 'done' || s === 'assembling' || s === 'voice_done' || s === 'voicing') {
      navigate(`/projects/${p.id}/assembly`)
    } else {
      navigate(`/projects/${p.id}/cues`)
    }
  }

  return (
    <div className="min-h-screen flex flex-col">
      {/* Header */}
      <header className="border-b border-gray-800 px-8 py-4 flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-white tracking-tight">OriginVault</h1>
          <p className="text-xs text-gray-500 mt-0.5">YouTube automation dashboard</p>
        </div>
        {tab === 'projects' && (
          <button className="btn-primary" onClick={() => navigate('/projects/new')}>
            + New Project
          </button>
        )}
        {tab === 'ideas' && (
          <div className="flex items-center gap-3">
            <a
              href="/api/ideas/export"
              download="ideas.csv"
              className="btn-secondary text-sm"
            >
              Export CSV
            </a>
            <select
              className="input text-sm py-1.5 w-20"
              value={ideaCount}
              onChange={e => setIdeaCount(Number(e.target.value))}
            >
              {[3, 5, 10, 15, 20].map(n => (
                <option key={n} value={n}>{n}</option>
              ))}
            </select>
            <button
              className="btn-primary text-sm"
              disabled={generatingIdeas}
              onClick={generateIdeas}
            >
              {generatingIdeas ? 'Generating...' : '+ Generate Ideas'}
            </button>
          </div>
        )}
      </header>

      {/* Tabs */}
      <div className="border-b border-gray-800 px-8">
        <div className="flex gap-6">
          {(['projects', 'ideas'] as const).map(t => (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={`py-3 text-sm font-medium border-b-2 transition-colors ${
                tab === t
                  ? 'border-brand-500 text-white'
                  : 'border-transparent text-gray-500 hover:text-gray-300'
              }`}
            >
              {t === 'projects' ? `Projects${projects.length > 0 ? ` (${projects.length})` : ''}` : `Ideas${ideas.length > 0 ? ` (${ideas.length})` : ''}`}
            </button>
          ))}
        </div>
      </div>

      <main className="flex-1 px-8 py-8 max-w-6xl mx-auto w-full">

        {/* ── Projects tab ── */}
        {tab === 'projects' && (
          <>
            {loadingProjects && <p className="text-gray-500 text-sm">Loading...</p>}

            {!loadingProjects && projects.length === 0 && (
              <div className="card p-12 text-center">
                <p className="text-gray-400 mb-4">No projects yet.</p>
                <button className="btn-primary" onClick={() => navigate('/projects/new')}>
                  Create your first project
                </button>
              </div>
            )}

            {!loadingProjects && projects.length > 0 && (
              <div className="card overflow-hidden">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-b border-gray-800 text-left text-gray-500 text-xs uppercase tracking-wider">
                      <th className="px-6 py-3 font-medium">Title / Topic</th>
                      <th className="px-6 py-3 font-medium">Status</th>
                      <th className="px-6 py-3 font-medium">Created</th>
                      <th className="px-6 py-3 font-medium text-right">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {projects.map((p, i) => (
                      <tr
                        key={p.id}
                        className={`border-b border-gray-800 hover:bg-gray-800/50 cursor-pointer transition-colors ${i === projects.length - 1 ? 'border-0' : ''}`}
                        onClick={() => openProject(p)}
                      >
                        <td className="px-6 py-4">
                          <div className="font-medium text-white">
                            {p.title || <span className="text-gray-500 italic">{p.topic}</span>}
                          </div>
                          {p.title && (
                            <div className="text-gray-500 text-xs mt-0.5 truncate max-w-xs">{p.topic}</div>
                          )}
                        </td>
                        <td className="px-6 py-4">
                          <span className={`text-xs px-2.5 py-1 rounded-full font-medium ${STATUS_COLOR[p.status as ProjectStatus] ?? 'bg-gray-700 text-gray-300'}`}>
                            {STATUS_LABEL[p.status as ProjectStatus] ?? p.status}
                          </span>
                        </td>
                        <td className="px-6 py-4 text-gray-400 text-xs">
                          {new Date(p.created_at).toLocaleDateString('en-US', {
                            month: 'short', day: 'numeric', year: 'numeric',
                          })}
                        </td>
                        <td className="px-6 py-4 text-right" onClick={e => e.stopPropagation()}>
                          <div className="flex items-center justify-end gap-2">
                            {p.status === 'done' && (
                              <a
                                href={api.downloadURL(p.id)}
                                download
                                className="btn-secondary text-xs"
                                onClick={e => e.stopPropagation()}
                              >
                                Download
                              </a>
                            )}
                            <button
                              className="btn text-xs text-gray-400 hover:text-red-400 hover:bg-red-950"
                              onClick={() => delProject(p.id)}
                            >
                              Remove
                            </button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}

        {/* ── Ideas tab ── */}
        {tab === 'ideas' && (
          <>
            {loadingIdeas && <p className="text-gray-500 text-sm">Loading...</p>}

            {!loadingIdeas && ideas.length === 0 && (
              <div className="card p-12 text-center">
                <p className="text-gray-400 mb-2">No ideas yet.</p>
                <p className="text-gray-600 text-sm mb-6">
                  Click "Generate Ideas" to auto-suggest topics from your channel niche, then expand any idea to get a full script, title, description, and tags.
                </p>
                <button className="btn-primary" disabled={generatingIdeas} onClick={generateIdeas}>
                  {generatingIdeas ? 'Generating...' : 'Generate Ideas'}
                </button>
              </div>
            )}

            {!loadingIdeas && ideas.length > 0 && (
              <div className="card overflow-hidden">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-b border-gray-800 text-left text-gray-500 text-xs uppercase tracking-wider">
                      <th className="px-6 py-3 font-medium">Subject</th>
                      <th className="px-6 py-3 font-medium">Title</th>
                      <th className="px-6 py-3 font-medium">Keywords</th>
                      <th className="px-6 py-3 font-medium">Status</th>
                      <th className="px-6 py-3 font-medium text-right">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {ideas.map((idea, i) => (
                      <tr
                        key={idea.id}
                        className={`border-b border-gray-800 ${i === ideas.length - 1 ? 'border-0' : ''}`}
                      >
                        <td className="px-6 py-4 max-w-[180px]">
                          <div className="text-white font-medium truncate">{idea.subject}</div>
                          {idea.summary && (
                            <div className="text-gray-500 text-xs mt-0.5 truncate">{idea.summary}</div>
                          )}
                        </td>
                        <td className="px-6 py-4 max-w-[240px]">
                          {idea.title
                            ? <span className="text-gray-300 truncate block">{idea.title}</span>
                            : <span className="text-gray-600 italic">—</span>}
                        </td>
                        <td className="px-6 py-4 max-w-[180px]">
                          {idea.keywords?.length > 0 ? (
                            <div className="flex flex-wrap gap-1">
                              {idea.keywords.slice(0, 3).map((k, ki) => (
                                <span key={ki} className="bg-gray-800 text-gray-400 text-xs px-1.5 py-0.5 rounded">{k}</span>
                              ))}
                              {idea.keywords.length > 3 && (
                                <span className="text-gray-600 text-xs self-center">+{idea.keywords.length - 3}</span>
                              )}
                            </div>
                          ) : <span className="text-gray-600">—</span>}
                        </td>
                        <td className="px-6 py-4">
                          <span className={`text-xs px-2.5 py-1 rounded-full font-medium ${IDEA_STATUS_COLOR[idea.status] ?? 'bg-gray-700 text-gray-300'}`}>
                            {IDEA_STATUS_LABEL[idea.status] ?? idea.status}
                          </span>
                          {idea.error_msg && (
                            <p className="text-red-400 text-xs mt-1 max-w-[120px] truncate" title={idea.error_msg}>
                              {idea.error_msg}
                            </p>
                          )}
                        </td>
                        <td className="px-6 py-4 text-right">
                          <div className="flex items-center justify-end gap-2">
                            {(idea.status === 'idea' || idea.status === 'error') && (
                              <button
                                className="btn-secondary text-xs"
                                onClick={() => expandIdea(idea.id)}
                              >
                                Expand
                              </button>
                            )}
                            {idea.status === 'expanding' && (
                              <span className="text-yellow-500 text-xs animate-pulse">Working...</span>
                            )}
                            {idea.status === 'ready' && (
                              <>
                                <button
                                  className="btn-secondary text-xs"
                                  onClick={() => expandIdea(idea.id)}
                                >
                                  Re-expand
                                </button>
                                <button
                                  className="btn-primary text-xs"
                                  onClick={() => createProjectFromIdea(idea.id)}
                                >
                                  Create Project
                                </button>
                              </>
                            )}
                            <button
                              className="text-xs text-gray-500 hover:text-red-400 px-2 py-1 rounded hover:bg-red-950 transition-colors"
                              onClick={() => deleteIdea(idea.id)}
                            >
                              Delete
                            </button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}

      </main>
    </div>
  )
}
