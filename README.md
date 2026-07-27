# ytsift

> **Sift through YouTube noise** — a Go CLI that extracts a video's transcript, comments and metadata, ready for AI analysis. No API keys.

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](https://go.dev)
[![yt-dlp](https://img.shields.io/badge/yt--dlp-required-orange)](https://github.com/yt-dlp/yt-dlp)
[![AI Agent Skill](https://img.shields.io/badge/AI%20Agent-Skill-purple)](SKILL.md)

---

## Overview

**YouTube Insight** is two things packaged together:

1. **`yt-insight`** — a standalone CLI tool (Go, single binary, zero deps) that orchestrates `yt-dlp` and produces clean, LLM-friendly output: transcript, metadata, and comments sorted by likes.
2. **AI Agent Skill** — a `SKILL.md` that teaches AI agents (Crush, Claude Code, Cursor, etc.) how to use the CLI automatically when a user asks about a YouTube video.

No API keys. One external dependency (`yt-dlp`). Clean output designed for token efficiency.

---

## Quick Start

```bash
git clone https://github.com/rmay1er/ytsift.git
cd ytsift
bash scripts/install.sh

# or just grab the binary directly:
# curl -Lo scripts/yt-insight https://github.com/rmay1er/ytsift/releases/latest/download/yt-insight-darwin-arm64

yt-insight fetch "https://www.youtube.com/watch?v=VIDEO_ID"
```

The `install.sh` script builds the Go binary and checks that `yt-dlp` is on `$PATH`. If `yt-dlp` is missing, install it with:

```bash
brew install yt-dlp        # macOS
pipx install yt-dlp        # cross-platform
pip install --user yt-dlp  # fallback
```

---

## CLI Usage

```bash
yt-insight fetch <url-or-video-id> [flags]
```

**Flags:**

| Flag | Default | Description |
|---|---|---|
| `--out DIR` | `$TMPDIR/yt-insight/<id>` | Output directory for the 3 result files |
| `--max-comments N` | 50 | Max top-level comments to keep in output |
| `--sub-langs LIST` | `en,ru,en.*,ru.*` | Subtitle language priority (comma-separated) |
| `--workdir DIR` | `<out>/.work` | Scratch dir for yt-dlp artifacts |
| `--keep-work` | `false` | Retain scratch dir (default: removed on success) |

**Supported URL formats:**

```
https://www.youtube.com/watch?v=dQw4w9WgXcQ
https://youtu.be/dQw4w9WgXcQ
https://www.youtube.com/shorts/dQw4w9WgXcQ
https://www.youtube.com/embed/dQw4w9WgXcQ
dQw4w9WgXcQ  (bare 11-char video id)
```

**Examples:**

```bash
# Default: English + Russian subs, 50 top-level comments
yt-insight fetch "https://youtu.be/abc123"

# Spanish + German subs, more comments, custom output dir
yt-insight fetch "https://youtube.com/watch?v=abc123" \
  --sub-langs "es,de,en" \
  --max-comments 200 \
  --out ~/reports/my-video

# Keep debug artifacts (raw info.json, subtitles)
yt-insight fetch "https://youtu.be/abc123" --keep-work
```

### stdout contract

The CLI is designed for AI agent consumption. stdout carries exactly **one JSON object** (the last line) with absolute paths:

```json
{
  "video_id": "abc123",
  "out_dir": "/tmp/yt-insight/abc123",
  "transcript": "/tmp/yt-insight/abc123/transcript.txt",
  "metadata": "/tmp/yt-insight/abc123/metadata.json",
  "comments": "/tmp/yt-insight/abc123/comments.json",
  "transcript_chars": 9607,
  "transcript_lines": 2
}
```

All yt-dlp progress and diagnostics go to **stderr**, so the agent can capture stdout with `... | tail -1 | jq`.

---

## Output Format

### `transcript.txt`
Cleaned plain-text transcript. YouTube's auto-generated rolling captions are merged via **longest suffix/prefix overlap** — the stuttering repeated cues become readable running prose. Tags, HTML entities, and timestamps are stripped.

### `metadata.json`
Compact video metadata:

```json
{
  "title": "Video Title",
  "channel": "Channel Name",
  "uploader": "@handle",
  "upload_date": "2026-07-25",
  "duration_seconds": 634,
  "duration_hmmss": "10:34",
  "view_count": 7189,
  "like_count": 259,
  "comment_count": 23,
  "categories": ["Science & Technology"],
  "tags": ["htmx", "tailwind"],
  "availability": "public",
  "description": "Video description text..."
}
```

### `comments.json`
Top-liked comments sorted by `like_count` descending. Top-level comments first, then top replies:

```json
{
  "video_id": "abc123",
  "total_fetched": 50,
  "top_level_count": 33,
  "replies_count": 17,
  "comments": [
    {
      "author": "@user",
      "text": "Comment text...",
      "like_count": 42,
      "is_top_level": true,
      "is_from_uploader": false
    }
  ]
}
```

---

## AI Agent Skill

This repository doubles as an **AI agent skill** for Crush, Claude Code, Cursor, and other agent platforms that support `SKILL.md`-based skills.

### Installing as a skill

**Crush:** If the repo is cloned (or symlinked) into one of Crush's default skill directories, it's auto-discovered:

```bash
ln -s "$PWD" ~/.config/crush/skills/ytsift
```

**Claude Code / Cursor:** Same approach — place the directory in your skills path or reference it in your project's `CLAUDE.md` / `AGENTS.md`.

### What the agent does

When you share a YouTube URL and ask for a summary, opinion analysis, or transcript, the agent:

1. Runs `yt-insight fetch <url>`
2. Reads the 3 output files
3. Produces a structured report:
   - **About the video** — summary, key thesis, context (channel, date, views)
   - **Public opinion** — sentiment distribution, recurring themes, top comments, controversy markers
   - **Conclusion** — does the audience agree with the video's premise?

### Trigger phrases

The skill activates on prompts like:
- "О чём это видео?" / "What is this video about?"
- "Проанализируй комментарии" / "Analyze the comments"
- "Что говорят в комментариях?" / "What do people think?"
- "Сделай транскрипт" / "Get the transcript"
- "Общественное мнение" / "Public opinion on this video"

---

## How It Works

```
┌─────────────┐     ┌──────────┐     ┌──────────────┐
│  YouTube    │────▶│  yt-dlp  │────▶│  .info.json  │
│  URL        │     │          │     │  .vtt subtitles│
└─────────────┘     └──────────┘     └──────┬───────┘
                                            │
                                            ▼
                                    ┌───────────────┐
                                    │  yt-insight   │
                                    │  (Go binary)  │
                                    │               │
                                    │  • Clean VTT  │
                                    │  • Merge      │
                                    │    rolling    │
                                    │    captions   │
                                    │  • Parse      │
                                    │    comments   │
                                    │  • Project    │
                                    │    metadata   │
                                    └───────┬───────┘
                                            │
                    ┌───────────────────────┼───────────────────────┐
                    ▼                       ▼                       ▼
            ┌───────────┐          ┌──────────────┐          ┌──────────────┐
            │transcript │          │ metadata.json│          │comments.json  │
            │   .txt    │          │              │          │              │
            └───────────┘          └──────────────┘          └──────────────┘
```

`yt-dlp` handles all the YouTube protocol complexity (Innertube API, signatures, comment continuation tokens, subtitle URLs). `yt-insight` does the data shaping: VTT/SRT cleaning, rolling-caption dedup, comment ranking, and metadata projection. The result is three small, clean files ready for LLM consumption.

---

## Dependencies

| Dependency | Required for | Install |
|---|---|---|
| [yt-dlp](https://github.com/yt-dlp/yt-dlp) | Runtime (YouTube protocol) | `brew install yt-dlp` / `pipx install yt-dlp` |
| [Go](https://go.dev) (1.21+) | Building the binary | `brew install go` / `go.dev/dl` |
| **None at runtime** | The binary is statically compiled | — |

---

## Project Structure

```
ytsift/
├── README.md              # this file
├── SKILL.md               # AI agent skill description
├── .gitignore
├── scripts/
│   └── install.sh         # build binary + verify yt-dlp
├── tool/
│   ├── go.mod
│   └── cmd/yt-insight/
│       └── main.go        # Go source (stdlib only)
```

---

## License

MIT