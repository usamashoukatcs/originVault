export type ProjectStatus =
  | 'pending'
  | 'generating'
  | 'script_done'
  | 'voicing'
  | 'voice_done'
  | 'assembling'
  | 'done'
  | 'error'

export interface ProjectSummary {
  id: string
  title: string
  topic: string
  status: ProjectStatus
  output_dir: string
  created_at: string
}

export interface Overlay {
  id: string
  text: string
  x: number        // 0–100 percentage of video width
  y: number        // 0–100 percentage of video height
  start_time: number
  end_time: number
  fontsize: number
  color: string
  bold: boolean
  bg_opacity: number
}

export interface StatCard {
  big_number: string
  label: string
  context: string
}

export interface ClipCue {
  phrase: string
  keyword: string
  image_query: string
  visual_type: string
  person: string
  company: string
  product: string
  news_ref: number
  animate: boolean
  lower_third: string
  stat?: StatCard
  pip_query: string
}

export interface SelectedClipInfo {
  clip_path: string
  source: string
  url: string
  thumb_url: string
}

export interface DashboardCue extends ClipCue {
  selected_clips?: SelectedClipInfo[]
  selected_clip_path: string
  selected_source: string
  selected_url: string
  selected_thumb_url: string
}

export interface BlogPostResult {
  title: string
  slug: string
  excerpt: string
  content_html: string
  tags: string[]
  categories: string[]
  published_url?: string
  published_id?: number
}

export interface ShortVideo {
  title: string
  hook: string
  script: string
  scenes: string[]
}

export type ShortsStatus = '' | 'assembling' | 'done' | 'error'

export interface Project {
  id: string
  topic: string
  title: string
  summary: string
  description: string
  script: string
  keywords: string[]
  hashtags: string[]
  status: ProjectStatus
  status_log: string[]
  output_dir: string
  video_path: string
  created_at: string
  updated_at: string
  cues: DashboardCue[]
  overlays: Overlay[]
  blog_post?: BlogPostResult
  shorts?: ShortVideo[]
  short_paths?: string[]
  shorts_status?: ShortsStatus
}

export type IdeaStatus = 'idea' | 'expanding' | 'ready' | 'error'

export interface Idea {
  id: string
  subject: string
  niche: string
  title: string
  summary: string
  description: string
  script: string
  keywords: string[]
  hashtags: string[]
  status: IdeaStatus
  error_msg?: string
  created_at: string
  updated_at: string
}

export interface ThumbnailPromptResult {
  focal_element: string
  visual_concept: string
  color_mood: string
  prompt: string
  negative_prompt: string
  text_overlay: string
  text_placement: string
  canva_tips: string
}

export interface SearchResult {
  thumb_url: string
  download_url: string
  page_url: string
  source: string
  score: number
  duration: number
  is_video: boolean
  width: number
  height: number
  title: string
}
