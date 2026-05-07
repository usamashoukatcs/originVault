import type { Project, ProjectSummary, SearchResult, Overlay, Idea } from './types'

const BASE = '/api'

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(BASE + path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(err.error || `HTTP ${res.status}`)
  }
  return res.json()
}

// Projects
export const api = {
  listProjects: () => req<ProjectSummary[]>('GET', '/projects'),
  getProject: (id: string) => req<Project>('GET', `/projects/${id}`),
  createProject: (topic: string) =>
    req<Project>('POST', '/projects', { topic }),
  deleteProject: (id: string) => req<void>('DELETE', `/projects/${id}`),

  // Async steps
  getStatus: (id: string) =>
    req<{ status: string; log: string[] }>('GET', `/projects/${id}/status`),
  generateScript: (
    id: string,
    opts: { subject?: string; video_length_min?: number; video_length_max?: number }
  ) => req<{ status: string }>('POST', `/projects/${id}/generate`, opts),
  generateVoice: (id: string) =>
    req<{ status: string }>('POST', `/projects/${id}/voice`),
  assemble: (id: string) =>
    req<{ status: string }>('POST', `/projects/${id}/assemble`),
  downloadURL: (id: string) => `${BASE}/projects/${id}/download`,

  // Cue selection
  selectClip: (
    id: string,
    idx: number,
    result: SearchResult
  ) =>
    req<{ clip_path: string }>('POST', `/projects/${id}/cues/${idx}/select`, {
      download_url: result.download_url,
      thumb_url:    result.thumb_url,
      page_url:     result.page_url,
      source:       result.source,
      is_video:     result.is_video,
    }),
  clearClip: (id: string, idx: number) =>
    req<void>('DELETE', `/projects/${id}/cues/${idx}/select`),
  removeClipFromSequence: (id: string, idx: number, clipIdx: number) =>
    req<void>('DELETE', `/projects/${id}/cues/${idx}/clips/${clipIdx}`),
  generateAIImage: (id: string, idx: number, prompt: string, model: string) =>
    req<{ clip_path: string; source: string }>('POST', `/projects/${id}/cues/${idx}/ai-generate`, { prompt, model }),

  // Image search
  search: (params: {
    source: string
    query: string
    portrait?: boolean
    page?: number
  }): Promise<{ results: SearchResult[] }> => {
    const qs = new URLSearchParams({
      source: params.source,
      query: params.query,
      ...(params.portrait ? { portrait: 'true' } : {}),
      ...(params.page && params.page > 1 ? { page: String(params.page) } : {}),
    })
    return req('GET', `/search?${qs}`)
  },

  // Ideas
  listIdeas: () => req<Idea[]>('GET', '/ideas'),
  generateIdeas: (count: number) => req<Idea[]>('POST', '/ideas/generate', { count }),
  expandIdea: (id: string) => req<Idea>('POST', `/ideas/${id}/expand`),
  deleteIdea: (id: string) => req<void>('DELETE', `/ideas/${id}`),
  createProjectFromIdea: (id: string) => req<Project>('POST', `/ideas/${id}/project`),

  // Overlays
  listOverlays: (id: string) => req<Overlay[]>('GET', `/projects/${id}/overlays`),
  createOverlay: (id: string, ov: Partial<Overlay>) =>
    req<Overlay>('POST', `/projects/${id}/overlays`, ov),
  updateOverlay: (id: string, oid: string, ov: Partial<Overlay>) =>
    req<Overlay>('PUT', `/projects/${id}/overlays/${oid}`, ov),
  deleteOverlay: (id: string, oid: string) =>
    req<void>('DELETE', `/projects/${id}/overlays/${oid}`),
}
