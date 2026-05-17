package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// StatCard holds data for an auto-generated number/stat graphic.
// When set on a cue, it overrides image search — a clean infographic is generated instead.
type StatCard struct {
	BigNumber string `json:"big_number"` // e.g. "150,000" or "$21B" or "40%"
	Label     string `json:"label"`      // e.g. "convenience stores in the US"
	Context   string `json:"context"`    // e.g. "as of 2024" — shown smaller below label
}

type ClipCue struct {
	Phrase     string   `json:"phrase"`       // exact phrase from script where clip changes
	Keyword    string   `json:"keyword"`      // stock video fallback search keyword
	ImageQuery string   `json:"image_query"`  // precise real-image search query, e.g. "Philips CD-100 first CD player 1982 silver disc"
	VisualType string   `json:"visual_type"`  // shot type: "portrait"|"product"|"location"|"document"|"archive"|"news"|"map"|"crowd"|"infographic"|"stat"
	Person     string   `json:"person"`       // real person's full name if they are being discussed (empty otherwise)
	Company    string   `json:"company"`      // real company name if this scene is specifically about that company (empty otherwise)
	Product    string   `json:"product"`      // specific product/device/model name, e.g. "BlackBerry Bold 9900" or "iPhone 4" (empty otherwise)
	NewsRef    int      `json:"news_ref"`     // 1-based index of research source to show as news screenshot (0 = none)
	Animate    bool     `json:"animate"`      // true if the scene describes an action/incident that benefits from animation
	LowerThird string   `json:"lower_third"`  // animated lower-third label, e.g. "Ferruccio Lamborghini — Founder" or "Lamborghini Miura — 1966"
	Stat       *StatCard `json:"stat,omitempty"` // when set, auto-generates a stat callout graphic instead of image search
	PipQuery   string    `json:"pip_query,omitempty"` // if set, fetch a secondary image and composite it as a corner PiP overlay
	HookText   string    `json:"hook_text,omitempty"` // 3-6 word callout shown on screen at this moment, e.g. "But here's the catch..." or "$21 BILLION"
}

type ShortVideo struct {
	Title  string   `json:"title"`
	Hook   string   `json:"hook"`
	Script string   `json:"script"`
	Scenes []string `json:"scenes"`
}

type ScriptOutput struct {
	Title       string       `json:"title"`
	Script      string       `json:"script"`
	ClipCues    []ClipCue    `json:"clip_cues"`
	Summary     string       `json:"summary"`
	Description string       `json:"description"`
	Keywords    []string     `json:"keywords"`
	Hashtags    []string     `json:"hashtags"`
	Shorts      []ShortVideo `json:"shorts"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []claudeMessage `json:"messages"`
}

type claudeResponse struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

func GenerateScript(apiKey, niche string, videoLengthMinutes int, research []ResearchResult, seoData ...SEOData) (*ScriptOutput, error) {
	currentYear := time.Now().Year()
	wordsNeeded := videoLengthMinutes * 150
	clipsNeeded := videoLengthMinutes * 20 // one clip change every ~3 seconds

	// build research context block if we have real source material
	researchBlock := ""
	if len(research) > 0 {
		var rb strings.Builder
		rb.WriteString("\n\n=== RESEARCH MATERIAL — MANDATORY USAGE ===\n")
		rb.WriteString("This is REAL verified information. You MUST use the specific dates, numbers, names, and events below.\n")
		rb.WriteString("Do NOT invent facts. Do NOT round numbers. Do NOT approximate dates.\n")
		rb.WriteString("Every major claim in your script must come from this research.\n")
		rb.WriteString("For every key fact (founding year, acquisition price, product launch date), reference the source index in a news_ref cue.\n\n")
		for i, r := range research {
			rb.WriteString(fmt.Sprintf("--- Source %d: %s ---\n", i+1, r.Title))
			rb.WriteString(fmt.Sprintf("URL: %s\n", r.URL))
			rb.WriteString(r.Excerpt)
			rb.WriteString("\n\n")
		}
		rb.WriteString("=== END OF RESEARCH ===\n\n")
		rb.WriteString("CHECKLIST before writing — your script MUST include:\n")
		rb.WriteString("✓ Exact founding year and founder's full name\n")
		rb.WriteString("✓ At least 3 specific dates with what happened on those dates\n")
		rb.WriteString("✓ At least 2 specific revenue/valuation/deal figures with years\n")
		rb.WriteString("✓ Every major product launch mentioned with its real name and year\n")
		rb.WriteString("✓ The exact crisis/turning point with year and specific cause\n")
		rb.WriteString("✓ Where the company/invention stands today with a specific current stat\n")
		researchBlock = rb.String()
	}

	seoBlock := ""
	if len(seoData) > 0 {
		seo := seoData[0]
		if len(seo.Autocomplete) > 0 || len(seo.CompetitorTitles) > 0 {
			var sb strings.Builder
			sb.WriteString("\n\n=== YOUTUBE SEO DATA — USE THIS FOR TITLE, DESCRIPTION & KEYWORDS ===\n")
			sb.WriteString("These are real signals from YouTube search. Build your title, description, and keywords around these.\n\n")
			if len(seo.Autocomplete) > 0 {
				sb.WriteString("What people search on YouTube for this topic:\n")
				for _, s := range seo.Autocomplete {
					sb.WriteString("- " + s + "\n")
				}
				sb.WriteString("\n")
			}
			if len(seo.CompetitorTitles) > 0 {
				sb.WriteString("Existing YouTube video titles on this topic (these are your competition — don't copy, but use them to understand the space):\n")
				for _, t := range seo.CompetitorTitles {
					sb.WriteString("- " + t + "\n")
				}
				sb.WriteString("\n")
			}
			sb.WriteString("TITLE RULES using this SEO data:\n")
			sb.WriteString("- Use high-search-volume phrases from the autocomplete list naturally in your title\n")
			sb.WriteString("- Your title should be different from all competitor titles — find the angle they missed\n")
			sb.WriteString("- Include the subject name early in the title (within the first 4 words)\n")
			sb.WriteString("DESCRIPTION RULES:\n")
			sb.WriteString("- First 2 sentences must use keywords from the autocomplete list — this is what YouTube indexes\n")
			sb.WriteString("- Naturally weave in 5-8 of the autocomplete phrases throughout the description\n")
			sb.WriteString("- Add timestamps, a CTA to subscribe, and 5-10 relevant hashtags at the bottom\n")
			sb.WriteString("KEYWORDS: Include the exact autocomplete phrases as keywords (they reflect real search volume)\n")
			sb.WriteString("=== END SEO DATA ===\n")
			seoBlock = sb.String()
		}
	}

	prompt := fmt.Sprintf(`You are a YouTube scriptwriter for the channel "%s".%s%s

=== VOICE AND STYLE — STUDY THIS CAREFULLY ===

Your writing style is modelled on Company Man's most popular videos. Study this real excerpt and match it exactly:

  "7-Eleven is the undisputed leader when it comes to convenience stores, an industry that dates back almost exactly a century, originating in 1927 when the company that would become 7-Eleven essentially created the concept. See, back then, before the invention of Freon, it was extremely uncommon, expensive, and actually dangerous for people to have refrigerators in their homes. So instead, they would have ice boxes that were kept cool by large blocks of ice that would logically melt away and have to be replaced every so often. The Southland Ice Company in Dallas, Texas, started selling basic food items like milk and bread at one of their ice docks. And that right there is believed to be the first ever convenience store. I think that is pretty cool how it started."

Notice:
  - Opens directly with the subject. No theatrical tease. Just a calm, interesting fact.
  - Conversational "See," to introduce a deeper explanation
  - Personal asides woven in naturally ("I think that is pretty cool")
  - Specific numbers, dates, and facts in every paragraph
  - Short punchy sentences mixed with longer explanatory ones
  - He writes like he is TALKING to a friend, not presenting a documentary

OPENING — Two acceptable styles:
  Style A (direct): Open with the subject name + one compelling fact about its current or peak state.
    "7-Eleven is the undisputed leader when it comes to convenience stores..."
    "Nokia, at its peak, made 40%% of every mobile phone sold on earth."
  Style B (intrigue then reveal): One punchy sentence of intrigue WITHOUT naming the subject, then immediately reveal.
    "A farmer wrote a letter to Ferrari in 1963. The reply was so rude, it started a supercar company. That man was Ferruccio Lamborghini."

  FORBIDDEN OPENERS — any of these will be rejected:
  ✗ "In [year]..." — starting with a year is a documentary cliché, not a conversation starter
  ✗ "Today we are looking at..."
  ✗ "In this video..."
  ✗ "Welcome to OriginVault"
  ✗ Any sentence that begins with a year or a date
  The opening sentence must hook the viewer on the SUBJECT, not on a calendar date.

CONVERSATIONAL ASIDES — weave these in naturally, 6-10 times per video:
  - "I think that is pretty cool how it started."
  - "I mean, it's not even close."
  - "I almost find that hard to believe."
  - "That almost feels like an insulting way to say it, doesn't it?"
  - "Yeah, they were not kidding when they said that."
  - "I don't want to speak too much more about this because it is mostly complex speculation."
  - "I want to be clear that I am not saying [X] will be going away anytime soon."
  - "So, please let me know if that is the case for you."
  These make the viewer feel like they are watching a friend explain something, not a news anchor.

NUMBERS — always specific, always woven in naturally. Every number gets said out loud:
  - Not "a lot of money" → "$21 billion to buy Speedway"
  - Not "many stores" → "3,800 locations across 36 states"
  - Not "a big market share" → "more than twice as big as Circle K and four times as big as Casey's"
  KEY RULE: Every significant number in the script must also get a STAT CUE (see stat field below).

CURRENT YEAR IS %d. Treat all statistics accordingly:
  - Research sources may contain data from 2023 or 2024. Do NOT cite those years as "current" or "as of [year]".
  - If a stat from research is from a prior year, either find a more recent figure or write "as of their last reported figures" or "as of the latest available data" — never "as of 2024" when the viewer is watching in %d.
  - Historical dates (founding year, acquisition year, product launch) are fine to cite exactly — those are facts, not current stats.

ANALOGIES — use when explaining abstract business concepts:
  - "That's like if Lucky Charms went bankrupt and was then bought by Marshmallow Maties"
  - "Think of it like a person living so far beyond their means that a temporary pay cut led to the bank foreclosing on their house"
  Use at major turning points — debt crises, acquisitions, pivots.

NO REPETITION — this is a hard rule:
  - Every sentence must introduce information the viewer has not heard yet in this script
  - Never restate a fact in different words. If the founding year was mentioned, never say "as I mentioned, they were founded in..."
  - Never summarise what you just said before moving on. Just move on.
  - Read back each paragraph and ask: "did I already say this?" If yes, cut it.

RE-HOOK every 90 seconds — the viewer's attention dips. At every major section transition, drop one of:
  - A surprising contradiction: "But here is the thing nobody talks about..."
  - A forward tease: "And what happened next is the part that still does not make sense to me."
  - A reframe: "Up to this point this looks like a success story. It is not."
  These keep people watching past the halfway point.

HONEST AND GENUINE TONE:
  - Never use hollow words like "revolutionary", "game-changing", "iconic" without explaining WHY
  - If something failed, say it failed and say why — don't soften it
  - If the data is mixed, say it is mixed
  - The viewer should feel they are getting the real version, not the PR version

OPEN LOOPS — the single most powerful YouTube retention technique:
  An open loop is a question or mystery you plant early that you don't answer until later.
  Plant at least 3 open loops across the script:
    - Section 1: hint at the ending without revealing it. "What happened to them is still one of the most debated failures in business history."
    - Section 2→3 transition: hint at the coming conflict before the rise is done. "The problem is, the thing that made them great was also the thing that would eventually break them."
    - Section 3→4 transition: reframe the rise as false security. "Looking back now, all the warning signs were there. Nobody was reading them."
  Open loops work because the human brain cannot leave a question unanswered. Viewers watch to get the answer.
  NEVER close a loop and open a new one at the same time — stack them so multiple questions are unanswered simultaneously.

BUT / THEREFORE RULE — every paragraph transition must use causality, not sequence:
  WRONG (sequence — this is a list, not a story):
    "They launched in 1985. They expanded to Europe. They went public in 1990."
  RIGHT (causality — this is a story):
    "They launched in 1985 and it worked, THEREFORE they expanded to Europe. BUT the European market was nothing like what they expected, and THEREFORE when they went public in 1990, the timing could not have been worse."
  Audit every paragraph break. If the transition could be replaced by "and then", rewrite it with "but" or "therefore".
  "And then" is a list. "But/therefore" is cause and effect. Only stories have cause and effect.

REAL QUOTES — include at least 2 verbatim documented quotes from real people in the story:
  Not paraphrases. Actual words from real interviews, shareholder letters, news articles, or books.
  "Steve Ballmer laughed it off: 'There's no chance that the iPhone is going to get any significant market share. No chance.'"
  Why: quotes make the story feel real. They create irony when the person was spectacularly wrong.
  Place quotes at: the overconfidence moment, the turning point, or the lesson. Never quote someone saying something boring.
  If you cannot find a documented verbatim quote, write what they were reported to have believed — but do not fabricate words in quotation marks.

FORESHADOWING — plant the seed of the conflict inside the background section:
  In Section 2, include exactly one line that sounds like a positive character trait or business decision — but is actually the thing that will cause the collapse.
  It should feel neutral when first heard. Only in hindsight does the viewer realise it was the first crack.
  "Ferruccio Lamborghini had one rule: never ask anyone for money. It served him well for a decade. Then it didn't."
  "The CEO believed a great product would speak for itself and never hired a single salesperson. He was right — for a while."
  This plants dread without explaining it. The viewer doesn't know why this matters yet. They stay to find out.

MICRO-CLIFFHANGERS — every paragraph must end with a forward pull:
  A paragraph that ends with a fact is a paragraph the viewer can stop at. Do not let them stop.
  Every paragraph must end with one of:
    A) A revelation that reframes what they just heard: "And what they found in the audit was worse than anyone expected."
    B) A forward tease: "But that is not even the most interesting part of this story."
    C) A contradiction: "The irony is, the competitor they dismissed later copied exactly what they refused to do."
    D) A hanging question: "The question nobody has ever fully answered is: why did nobody stop it?"
  If your paragraph ends with a neutral fact ("they opened 200 new locations that year"), add one of the above after it.

EMOTIONAL INVESTMENT — the viewer must root for them before you show them fall:
  Section 3 must contain at least one moment where the founder or company does something genuinely admirable, risky, or relatable.
  This is the line that makes the fall matter. Without it, the collapse is just information.
  "They could have taken the safe route and licensed the technology. Instead they bet the entire company on the belief that people deserved better. For five years, that looked like genius."
  If the viewer does not care about the subject before Section 4, the video is just Wikipedia with music.
  Make them care. Then break their heart.

SENTENCE RHYTHM — alternate short and long sentences deliberately:
  The pattern that creates momentum and satisfies the ear:
    1. Short punch (5-8 words): "They were out of money."
    2. Long explanation (20-30 words): "Not the 'we need to cut costs' kind of out of money — the kind where next week's payroll is gone and nobody outside the boardroom knows yet."
    3. Short gut-punch (6-10 words): "That was a Tuesday in March, 2001."
  Use this short → long → short pattern at every major reveal.
  Never write 3 long sentences in a row — the viewer's ear loses the beat.
  Never write 4 short sentences in a row — it sounds like a children's book.

ARC — six-act story structure. Each section must FEEL like a story told by a friend, not a Wikipedia article.
No section labels. No headers. No "Now let's talk about the rise." Just keep talking.

  SECTION 1 — THE HOOK (first ~30 seconds, ~75 words)
  Start with an unresolved conflict, a shocking outcome, or a paradox — WITHOUT naming the subject yet.
  Make the viewer unable to look away before the first 30 seconds is up.
  Plant your first open loop here — hint at the ending without revealing it.
  Examples:
    "A farmer wrote a letter to Ferrari in 1963. The reply was so rude, it started a supercar company."
    "At its peak, Nokia made one in every three phones on earth. By 2013, they were gone from the phone business entirely."
    "In 2000 a company was offered $50 million to be acquired by Amazon. They said no. You know them today as Blockbuster."
  Reveal the subject name in the last sentence of this section. FORBIDDEN: starting with a year.

  SECTION 2 — THE BACKGROUND (~15% of script)
  Who are they? Where did they come from? What was the world like before they existed?
  Write it like you're telling a friend the backstory over dinner — not a company bio.
  Ground the viewer: founder's full name, personality, origin, what problem they saw, what they built first.
  Make the founder feel like a PERSON, not a Wikipedia stub. Give them a motivation or a quirk.
  Plant your FORESHADOWING line here — one positive-sounding trait that will later be their undoing.
  "Ferruccio Lamborghini was not a dreamer. He was a farmer and a mechanic who just didn't like being told his car was bad."
  End this section with a micro-cliffhanger that teases the rise: "And for a while, it worked better than anyone dared to imagine."

  SECTION 3 — THE RISE, with turning points (~20% of script)
  Don't list milestones. Show the DECISIONS and MOMENTS that changed everything.
  Every turning point: who decided what, when, and why it mattered. Use BUT/THEREFORE causality.
  "They had 10 stores. Then a manager named Harry Cunningham said: what if we let stores keep their own hours? Within 5 years they had 1,000."
  Include: specific numbers, competitor names, product launches with exact names and years. Every major number = stat cue.
  Build EMOTIONAL INVESTMENT here — give the viewer one moment to admire the company before the fall.
  End with a re-hook that opens the next loop: "Up to this point, this is a success story. And that is exactly the problem."

  SECTION 4 — THE CONFLICT OR CHALLENGE (~20% of script)
  Every great company story has a villain: bad debt, wrong technology bet, ruthless competitor, internal power struggle, founder's ego.
  Name the villain. Be specific. Who made the bad call? When? What were they thinking?
  Include at least one REAL QUOTE from someone who was there — ideally one that aged badly.
  This section should feel uncomfortable — like watching a slow-motion car crash where you already know the ending.
  "The strange thing is, they could see Netflix coming. They just didn't think it mattered."
  Re-hook: "And here is the part nobody talks about..." or "Up to this point this looks like a success story. It is not."

  SECTION 5 — THE FALL OR CURRENT SITUATION (~20% of script)
  What actually happened? Did they survive? Are they struggling? Is this a cautionary tale or a comeback story?
  Be honest. Don't soften it. If they went bankrupt, say bankrupt. If they sold for pennies on the dollar, say that.
  If they survived against the odds, make that feel EARNED, not inevitable.
  Use BUT/THEREFORE to show the chain of consequences — not a list of what happened.
  Include current state with the most recent available figures.

  SECTION 6 — THE LESSON AND TAKEAWAY (~10% of script)
  Close every open loop you planted. The viewer stayed for the answer — give it to them.
  End with 2-3 sentences that give the viewer something to walk away with and REPEAT to other people.
  Not a moral lecture. Something specific to THIS story that no other video could end with.
  "7-Eleven survived because convenience stores sell comfort, and people will always want that at 2am."
  "Blockbuster failed not because of Netflix, but because they had $1 billion to fight back and chose not to use it."
  Then: 2-3 SPECIFIC audience questions that invite real debate. End: "Thank you for watching."

END ENGAGEMENT — Company Man style: ask 2-3 specific questions that invite real debate:
  - "Will [subject] manage to turn this around, or is the writing already on the wall?"
  - "And I'm curious — have you noticed a difference as a customer over the past few years?"
  - "Let me know in the comments. I'd like to hear what you have to say."
  End with "Thank you for watching." — simple and genuine.

=== OUTPUT FORMAT ===

Return ONLY valid JSON with this exact structure:

{
  "title": "SEO-optimized title under 60 chars. Use patterns like: 'The Untold Story of...', 'Why X Was Actually Stolen From...', 'The Accident That Created...', 'How X Almost Never Existed', 'The Real Reason X Failed', 'The Rise and Fall of...'",
  "summary": "one sentence summary",
  "description": "Full YouTube description 300-500 words. Strong hook paragraph, what viewers will learn, timestamps like '00:00 Introduction\\n02:30 The Rise\\n05:00 The Fall', call to action to subscribe, hashtags at bottom",
  "keywords": ["tag1", "tag2"],
  "hashtags": ["#tag1", "#tag2", "#tag3"],
  "script": "Full narration script. Target EXACTLY %d words — do NOT exceed %d words. No stage directions. No section labels. Just the spoken words, exactly as the narrator would say them.",
  "clip_cues": [
    {
      "phrase": "exact verbatim phrase from the script, at least 8 words long, unique in the script",
      "visual_type": "one of: portrait|product|location|document|archive|news|map|crowd|infographic",
      "image_query": "precise search query with subject + era — e.g. 'Ferruccio Lamborghini portrait 1960s founder', 'Lamborghini 350 GT 1964 first sports car', 'Lamborghini factory Sant Agata 1970s workers'",
      "keyword": "stock video fallback — literal visual description only if no real photo exists: 'tractor factory assembly line Italy 1950s workers'",
      "person": "full name only when narrator speaks their name in this cue. Empty string otherwise.",
      "company": "company name only for scenes about that company's identity/headquarters. Empty string otherwise.",
      "product": "exact Wikipedia-style product name: 'Lamborghini Miura', 'Compact Cassette'. Empty string if none.",
      "news_ref": 0,
      "animate": false,
      "lower_third": "Documentary lower-third label. Format: 'Name — Context'. E.g. 'Ferruccio Lamborghini — Lamborghini Founder', 'Lamborghini Miura — 1966', 'Sant Agata Bolognese — Italy'. Use ONLY when introducing a key person for the first time, an iconic product at its launch moment, or a key location/date. Empty string for most cues — do not overuse.",
      "stat": null,
      "hook_text": ""
    }
  ],
  "shorts": [
    {
      "title": "Short title under 50 chars",
      "hook": "Max 6 words shown on screen",
      "script": "~80 word script, starts with shocking fact",
      "scenes": ["keyword1", "keyword2", "keyword3"]
    },
    {
      "title": "Short 2 title",
      "hook": "Max 6 words",
      "script": "~80 word script",
      "scenes": ["keyword1", "keyword2", "keyword3"]
    },
    {
      "title": "Short 3 title",
      "hook": "Max 6 words",
      "script": "~80 word script",
      "scenes": ["keyword1", "keyword2", "keyword3"]
    }
  ]
}

=== CLIP CUE RULES — SHOW EXACTLY WHAT IS BEING SAID ===

THE GOLDEN RULE: Every clip must show LITERALLY what the narrator is saying at that exact moment.
  Script says "The Southland Ice Company in Dallas" → show Dallas or an ice company in 1920s
  Script says "they spent $21 billion to buy Speedway" → stat card: "$21B" / "Speedway acquisition"
  Script says "3,800 locations across 36 states" → stat card: "3,800" / "Speedway locations acquired"
  Script says "Nokia had 40% of the global phone market" → stat card: "40%" / "of global phone market"
  Script says "Ferruccio Lamborghini in 1963" → his portrait photo
  Script says "the 350 GT was their first real sports car" → photo of the Lamborghini 350 GT
  NEVER show a generic "business meeting" or "workers" clip when the script names a specific thing.
  If the script names it specifically, show it specifically.

--- STAT FIELD — USE THIS FOR EVERY SIGNIFICANT NUMBER ---

When the script says a specific number, dollar amount, percentage, or count — use visual_type "stat" and fill the stat field:
  Script: "they spent $21 billion to buy Speedway"
    → visual_type: "stat", stat: { big_number: "$21B", label: "Speedway acquisition", context: "2021" }
  Script: "more than 150,000 convenience stores in the United States"
    → visual_type: "stat", stat: { big_number: "150,000+", label: "convenience stores in the US", context: "" }
  Script: "Nokia owned 40% of the global phone market"
    → visual_type: "stat", stat: { big_number: "40%", label: "of global phone market at peak", context: "2007" }
  Script: "closing 645 US stores over the next year"
    → visual_type: "stat", stat: { big_number: "645", label: "US stores closing", context: "" }
USE THIS for every major number in the script. A stat card is more powerful than any stock photo.
When visual_type is "stat", set stat field. Leave image_query and keyword empty.

You are not picking stock photos. You are building a SHOT LIST — the same way a human documentary editor at the BBC or Netflix would plan a 3-minute segment before touching the timeline. Every cut must feel intentional, varied, and visually serve the story.

--- SHOT LIST PLANNING (do this before writing cues) ---

Plan the full visual sequence first. A good documentary editor follows this rhythm:
  - NEVER put the same visual_type twice in a row
  - NEVER show the same subject (person, building, product) in two consecutive clips
  - Alternate between wide establishing shots, close detail shots, human portraits, documents, and product shots
  - Build toward dramatic moments: wider shots early, tighter/more specific shots as tension rises
  - After a dramatic reveal (acquisition, bankruptcy, launch), cut to a reaction or consequence shot, not the same subject again

Recommended documentary rhythm pattern (adapt to the story):
  location/archive → portrait → product → document/news → location → crowd/archive → product → portrait → infographic/map → news → location → ...

--- VISUAL_TYPE FIELD (REQUIRED ON EVERY CUE) ---

Every cue must have a visual_type. Choose the ONE that best describes what the viewer should see:
  "portrait"     — a real person's face or upper body. Use when narrator names or discusses a specific individual.
  "product"      — a specific physical product, device, invention, or object being discussed.
  "location"     — a real place: factory, city, headquarters, street, country. Use for establishing shots.
  "archive"      — historical photo or footage: old newspaper, vintage photo, period-era scene.
  "document"     — a specific document, patent, letter, chart, graph, or financial record.
  "news"         — a news article screenshot or headline about a specific event.
  "map"          — a geographical map showing a country, region, expansion territory, or trade route.
  "crowd"        — a group of people: workers, consumers, protesters, crowds at a launch event.
  "infographic"  — a diagram, timeline, org chart, or data visualisation.

CONSECUTIVE SAME TYPE = EDITORIAL FAILURE. If cue 4 is "portrait", cue 5 cannot also be "portrait". Force yourself to switch.

--- IMAGE_QUERY FIELD (MOST IMPORTANT) ---

The precise search string used to find the REAL photo on Wikimedia Commons and image databases.
MANDATORY: always include the brand/name + specific subject + decade/era (e.g. "1930s", "1960s", "early 2000s").
Think: "what would I type into Getty Images to find this EXACT real photo?"

  Script says "Philips launched the first CD player in 1982" → image_query: "Philips CD-100 compact disc player 1982 silver"
  Script says "Gerard Philips founded the company in 1891" → image_query: "Gerard Philips portrait founder 1890s black white"
  Script says "the factory in Eindhoven in the 1920s" → image_query: "Philips factory Eindhoven Netherlands 1920s workers historic"
  Script says "they invented the cassette tape" → image_query: "Philips compact cassette tape 1963 original magnetic"
  Script says "Ferruccio Lamborghini in his early years" → image_query: "Ferruccio Lamborghini portrait 1960s founder"
  Script says "the 350 GT was their first real sports car" → image_query: "Lamborghini 350 GT 1964 first sports car red"

TEASER OPENING RULE — You have the FULL script before writing any cue. When the opening line is a mystery tease that describes the subject WITHOUT naming it yet, you already know what the video is about. Query the specific SUBJECT, not the atmospheric scene.

  Opening tease: "There was a moment in the early 2000s when some of the smartest people on earth were convinced a two-wheeled scooter would make the car obsolete. That invention was the Segway."
    WRONG: "early 2000s technology trade show crowd convention center United States"  ← describes the atmosphere, not the subject
    RIGHT:  "Segway scooter Dean Kamen 2001 prototype early model"                    ← shows what the video is actually about

  Opening tease: "A farmer wrote a letter to Ferrari in 1963. The reply was so rude, it started a supercar company."
    WRONG: "1960s businessman writing letter angry"
    RIGHT:  "Ferruccio Lamborghini portrait 1963 founder"

  Rule: The viewer must see the VIDEO SUBJECT from frame one, even if the narrator has not said its name yet. Never let the scene-setting language ("a moment", "the smartest people", "a farmer") pull your query away from the specific thing the story is about.

NEVER write a generic query. "electronics factory" is wrong. "Philips Eindhoven factory assembly line 1950s" is right.
NEVER use a scene-setting phrase ("trade show", "convention center", "crowd of people", "businessman meeting") as the query when the video is about a specific named product, person, or company.

--- PERSON FIELD ---

Full name of the real person — SET ONLY on the cue whose phrase contains or immediately precedes their name being spoken aloud.
The photo appears THE MOMENT the cue starts — do not set it early.
Empty string if no specific real person is being named in this cue.

--- COMPANY FIELD ---

Set ONLY when the scene is specifically about that company's identity — headquarters, logo, brand moment, founding.
Empty string for generic industry scenes or when a product cue is more specific.

--- PRODUCT FIELD ---

EXACT product model name formatted like the Wikipedia article title.
E.g. "Philips CD-100", "Lamborghini Miura", "Compact Cassette", "Nokia 3310".
Set whenever the script discusses a specific product launch, invention, or device.
Wikimedia Commons has photos of almost every product ever made — be exact.

--- NEWS_REF FIELD ---

Set to the 1-based index of a research source when citing that article's specific fact.
Only for the 3-5 most important factual claims. Set to 0 otherwise.
Do NOT combine with person/company/product.

--- ANIMATE FIELD ---

true for dramatic events: stock crash, bankruptcy, acquisition, product launch, scandal, factory fire, hostile takeover.
false for everything else.

--- KEYWORD FIELD (LAST RESORT) ---

Only used when no real image exists. Must describe a SPECIFIC literal visual scene:
  ✓ "Eindhoven Netherlands city street 1900s cobblestone historic"
  ✓ "tractor factory assembly line workers Italy 1950s"
  ✗ "technology innovation" — not visual, too generic
  ✗ "success and growth" — not visual, too generic
  ✗ "workers" alone — matches anything including a butcher shop
  The keyword must be specific enough that a wrong result would be IMPOSSIBLE.
  Always include: location OR brand name + what the scene shows + era.

--- HOOK_TEXT FIELD (AUTO ON-SCREEN TEXT) ---

hook_text places big bold text on screen at KEY dramatic moments. It is NOT a subtitle — it is a punch-line callout.
Use it SPARINGLY: max 6-8 times per full video. Empty string on all other cues.

When to use it:
  - First cue of the video: 3-6 word teaser that makes the viewer lean in
    hook_text: "The company that changed everything"
  - Major stat reveal: just the number, nothing else
    hook_text: "$21 BILLION" or "40% of global market"
  - End of Section 2 → entering the rise: a forward tease
    hook_text: "And then they got serious"
  - The pivot moment at Section 3 → 4: re-hook signal
    hook_text: "But here's the catch..."
  - The fall moment: the brutal summary
    hook_text: "Gone in 18 months" or "Bankrupt."
  - The lesson: the single takeaway line
    hook_text: "The real reason they failed"

Rules:
  - Hard limit: 6 words maximum
  - ALL CAPS for pure number/stat reveals: "$21 BILLION", "40%%", "645 STORES"
  - Title Case for narrative hooks: "But Here's The Catch"
  - Must feel like a punch line, not a caption describing the image
  - Empty string on at least 90%% of cues

--- GENERAL RULES ---

  - Provide exactly %d clip cues spread through the video (one every ~3 seconds of speech — fast cuts keep attention)
  - Each cue must reflect what the narrator says AT THAT EXACT MOMENT
  - First cue phrase must be the very first words of the script
  - Each phrase must appear VERBATIM in the script — copy it character-for-character
  - PHRASE LENGTH: every phrase must be AT LEAST 8 words long and appear only ONCE in the script. Short phrases like "In 1963" break audio sync because speech recognition matches them in the wrong place. Use long, unique sentence openings.
  - NEVER recycle the same image_query or visual_type consecutively
  - Even if two cues share company:"Philips", their image_query MUST show completely different things — different decade, different product, different location
  - Priority: person > product > company > news_ref > image_query > keyword

=== CUE DENSITY — THIS IS THE #1 RULE. EVERY SENTENCE. NO EXCEPTIONS. ===

A sentence without its own cue = a FROZEN FRAME in the final video. Viewers will see a still image stuck on screen while the narrator moves on. This is a production failure.

RULE: Every sentence of 6+ words must have its own cue. One cue, one sentence minimum. A sentence may have MORE than one cue (multiple image changes within a sentence) — that is fine. But it cannot have ZERO.

If your script has 90 sentences → you need at least 90 cues.
If your script has 120 sentences → you need at least 120 cues.

PROOF — this is how a proper cue sequence looks. Every single sentence has a cue starting with its first words:

  SCRIPT: "The Segway was supposed to change the world. Dean Kamen built it in secret for years. He called it Ginger. He was convinced it would make cars obsolete."

  cue 1 phrase: "The Segway was supposed to change the world"  ← sentence 1 ✓
  cue 2 phrase: "Dean Kamen built it in secret for years"     ← sentence 2 ✓
  cue 3 phrase: "He called it Ginger"                         ← sentence 3 (only 4 words — OK to skip short ones)
  cue 4 phrase: "He was convinced it would make cars obsolete" ← sentence 4 ✓

BAD — two sentences share one cue, middle sentence frozen:
  cue 1 phrase: "The Segway was supposed to change the world"
  cue 2 phrase: "He was convinced it would make cars obsolete"
  ← "Dean Kamen built it in secret for years" had NO cue = frozen frame ✗

MANDATORY FINAL CHECK before outputting:
  Step 1: Count sentences in your script (count every . ? ! boundary).
  Step 2: Count your cues.
  Step 3: If cues < (sentence count × 0.9), you have failed — add missing cues now.
  Step 4: Re-read sentence by sentence. Any sentence of 6+ words with no cue → add one.

=== CRITICAL JSON RULES ===
- Return raw JSON only. No markdown. No code blocks. No backticks.
- Never use double-quote characters (") inside any JSON string value. Use single quotes (') instead.
- Every backslash inside a string must be doubled: use \\ not \.
- Newlines inside strings must be written as \n, not actual newlines.`,
		niche, researchBlock, seoBlock, currentYear, currentYear, wordsNeeded, wordsNeeded, clipsNeeded)

	reqBody := claudeRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 32000,
		Messages:  []claudeMessage{{Role: "user", Content: prompt}},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Claude API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Claude API error %d: %s", resp.StatusCode, string(respBytes))
	}

	var claudeResp claudeResponse
	if err := json.Unmarshal(respBytes, &claudeResp); err != nil {
		return nil, fmt.Errorf("failed to parse Claude response: %w", err)
	}

	if len(claudeResp.Content) == 0 {
		return nil, fmt.Errorf("empty response from Claude")
	}

	rawText := strings.TrimSpace(claudeResp.Content[0].Text)

	// strip markdown code block if Claude wrapped the response
	if strings.HasPrefix(rawText, "```") {
		// remove first line (```json or ```)
		if idx := strings.Index(rawText, "\n"); idx != -1 {
			rawText = rawText[idx+1:]
		}
		// remove trailing ```
		if idx := strings.LastIndex(rawText, "```"); idx != -1 {
			rawText = strings.TrimSpace(rawText[:idx])
		}
	}

	var output ScriptOutput
	if err := json.Unmarshal([]byte(rawText), &output); err != nil {
		// first parse failed — try repairing unescaped quotes inside strings
		repaired := repairJSON(rawText)
		if err2 := json.Unmarshal([]byte(repaired), &output); err2 != nil {
			return nil, fmt.Errorf("failed to parse script JSON: %w\nRaw response: %s", err, rawText)
		}
	}

	// Hard trim: if Claude still exceeded the word limit, truncate at the last
	// sentence boundary within the allowed word count.
	output.Script = trimToWordLimit(output.Script, wordsNeeded)

	// Ensure every sentence has at least one cue — insert fillers for any gaps.
	output.ClipCues = fillMissingCues(output.Script, output.ClipCues)

	return &output, nil
}

// sentenceSplitRe splits on sentence-ending punctuation followed by a space or end.
var sentenceSplitRe = regexp.MustCompile(`[.!?]+[\s]+`)

// splitSentences splits a script into individual sentences.
func splitSentences(script string) []string {
	locs := sentenceSplitRe.FindAllStringIndex(script, -1)
	var sentences []string
	prev := 0
	for _, loc := range locs {
		end := loc[1]
		s := strings.TrimSpace(script[prev:end])
		if s != "" {
			sentences = append(sentences, s)
		}
		prev = end
	}
	if tail := strings.TrimSpace(script[prev:]); tail != "" {
		sentences = append(sentences, tail)
	}
	return sentences
}

// fillMissingCues ensures every sentence of 6+ words in the script has at least one cue.
// For any sentence with no cue covering it, a filler cue is inserted that inherits the
// image_query and visual_type from the nearest previous cue so the screen never freezes.
func fillMissingCues(script string, cues []ClipCue) []ClipCue {
	if len(cues) == 0 {
		return cues
	}

	// Locate each cue's character position in the script.
	type posCue struct {
		cue ClipCue
		pos int
	}
	scriptLower := strings.ToLower(script)
	var positioned []posCue
	searchFrom := 0
	for _, cue := range cues {
		if cue.Phrase == "" {
			continue
		}
		phraseLower := strings.ToLower(cue.Phrase)
		idx := strings.Index(scriptLower[searchFrom:], phraseLower)
		pos := searchFrom
		if idx >= 0 {
			pos = searchFrom + idx
			searchFrom = pos + 1
		}
		positioned = append(positioned, posCue{cue, pos})
	}
	sort.Slice(positioned, func(i, j int) bool { return positioned[i].pos < positioned[j].pos })

	// Locate each sentence's start position in the script.
	type sentSpan struct {
		text  string
		start int
	}
	var spans []sentSpan
	cursor := 0
	for _, s := range splitSentences(script) {
		lookup := s
		if len(lookup) > 40 {
			lookup = lookup[:40]
		}
		idx := strings.Index(script[cursor:], lookup)
		if idx < 0 {
			continue
		}
		start := cursor + idx
		spans = append(spans, sentSpan{s, start})
		cursor = start + 1
	}

	// Walk sentences, emitting cues that fall in each sentence's range.
	// Insert a filler cue for any sentence that has no cue.
	var result []posCue
	ci := 0

	for si, span := range spans {
		wordCount := len(strings.Fields(span.text))
		if wordCount < 6 {
			// Flush any cues that belong here anyway.
			sentEnd := len(script)
			if si+1 < len(spans) {
				sentEnd = spans[si+1].start
			}
			for ci < len(positioned) && positioned[ci].pos < sentEnd {
				result = append(result, positioned[ci])
				ci++
			}
			continue
		}

		sentEnd := len(script)
		if si+1 < len(spans) {
			sentEnd = spans[si+1].start
		}

		countBefore := len(result)
		for ci < len(positioned) && positioned[ci].pos < sentEnd {
			result = append(result, positioned[ci])
			ci++
		}

		if len(result) == countBefore {
			// No cue fell in this sentence — create a filler.
			words := strings.Fields(span.text)
			phraseLen := 12
			if phraseLen > len(words) {
				phraseLen = len(words)
			}
			phrase := strings.Join(words[:phraseLen], " ")

			// Inherit visual query from nearest previous cue.
			var ref ClipCue
			if len(result) > 0 {
				ref = result[len(result)-1].cue
			} else if ci < len(positioned) {
				ref = positioned[ci].cue
			}
			result = append(result, posCue{
				cue: ClipCue{
					Phrase:     phrase,
					Keyword:    ref.Keyword,
					ImageQuery: ref.ImageQuery,
					VisualType: ref.VisualType,
				},
				pos: span.start,
			})
		}
	}

	// Flush any remaining cues after the last sentence.
	for ci < len(positioned) {
		result = append(result, positioned[ci])
		ci++
	}

	out := make([]ClipCue, len(result))
	for i, pc := range result {
		out[i] = pc.cue
	}
	return out
}

// trimToWordLimit truncates text to at most maxWords words, ending at the last
// sentence boundary (. ! ?) within that limit so the audio doesn't cut mid-sentence.
func trimToWordLimit(text string, maxWords int) string {
	words := strings.Fields(text)
	if len(words) <= maxWords {
		return text
	}
	// find position of the maxWords-th word in the original string
	count := 0
	cutPos := len(text)
	for i, ch := range text {
		if ch == ' ' || ch == '\t' || ch == '\n' {
			count++
			if count >= maxWords {
				cutPos = i
				break
			}
		}
	}
	trimmed := strings.TrimSpace(text[:cutPos])
	// walk backwards to find last sentence-ending punctuation
	for i := len(trimmed) - 1; i >= 0; i-- {
		if trimmed[i] == '.' || trimmed[i] == '!' || trimmed[i] == '?' {
			return trimmed[:i+1]
		}
	}
	return trimmed
}

// repairJSON fixes the most common LLM JSON mistake: unescaped double-quote
// characters inside string values (e.g. "code name "Ginger" was secret").
// It uses a simple state machine: when inside a JSON string, any " not
// followed by :  ,  }  ] (possible end-of-value markers) is escaped.
func repairJSON(s string) string {
	// replace Unicode smart quotes with plain ASCII equivalents first
	s = strings.ReplaceAll(s, "\u201C", "'") // "
	s = strings.ReplaceAll(s, "\u201D", "'") // "
	s = strings.ReplaceAll(s, "\u2018", "'") // '
	s = strings.ReplaceAll(s, "\u2019", "'") // '

	var out strings.Builder
	out.Grow(len(s))
	inString := false
	escaped := false

	for i := 0; i < len(s); i++ {
		c := s[i]

		if escaped {
			out.WriteByte(c)
			escaped = false
			continue
		}
		if c == '\\' {
			out.WriteByte(c)
			escaped = true
			continue
		}
		if c == '"' {
			if !inString {
				inString = true
				out.WriteByte(c)
				continue
			}
			// peek ahead past whitespace to determine if this closes the string
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\r' || s[j] == '\n') {
				j++
			}
			next := byte(0)
			if j < len(s) {
				next = s[j]
			}
			if next == ':' || next == ',' || next == '}' || next == ']' || next == 0 {
				// legitimate end of string
				inString = false
				out.WriteByte(c)
			} else {
				// unescaped quote inside the string — escape it
				out.WriteString(`\"`)
			}
			continue
		}
		out.WriteByte(c)
	}
	return out.String()
}
