---
name: youtube-insight
description: Transcribe a YouTube video from a URL, understand what it is about, and analyze public opinion from its comments. Use whenever the user shares a YouTube link and asks for a summary, what the video is about, a transcript, the comment sentiment, the audience reaction, or a public-opinion analysis. Triggers on phrases like "о чем это видео", "проанализируй видео", "что говорят в комментариях", "summarize this youtube video", "what do people think about this video", "youtube транскрипт", "общественное мнение".
user-invocable: true
---

# YouTube Insight

Fetch a YouTube video's transcript, metadata, and comments, then produce
a structured analysis of its content and of public opinion in the
comments.

## When to Use

The user provides a YouTube URL (or 11-char video id) and asks for any of:
- a summary / "what is this video about"
- the transcript
- an analysis of the comments / audience reaction / public opinion
- a combined content + sentiment report

If the user only wants to *download* the video file, that is out of scope;
this skill is about understanding content and opinions, not downloading
media.

## Prerequisites

The skill ships a single Go binary, `yt-insight`, that orchestrates
`yt-dlp` (YouTube protocol work: captions, comments, metadata) and does
all text cleaning itself. No API keys are required.

On first use per machine, build the binary and confirm `yt-dlp` is on
`$PATH`:

```bash
bash "${SKILL_DIR}/scripts/install.sh"
```

`install.sh` will NOT auto-install `yt-dlp` (package managers differ per
platform). If it reports `yt-dlp` missing, install it yourself with one
of:

```
brew install yt-dlp        # macOS
pipx install yt-dlp        # cross-platform
pip install --user yt-dlp  # fallback
```

## Procedure

1. Extract the YouTube URL or video id from the user's message. Accept
   `watch?v=`, `youtu.be/`, `/shorts/`, `/embed/`, or a bare 11-char id.

2. Run the fetcher. It writes three files into a temp dir and prints a
   single JSON object on **stdout** (absolute paths) — capture the last
   line of stdout and `json.Unmarshal` it to get the locations:

   ```bash
   "${SKILL_DIR}/scripts/yt-insight" fetch \
     --sub-langs "en,ru,en.*,ru.*" \
     --max-comments 50 \
     "<URL>"
   ```

   The binary is a CLI designed for agent consumption: stdout carries
   only the final JSON summary, all yt-dlp progress goes to stderr, and
   the heavy `.work/` scratch dir is removed on success (pass
   `--keep-work` to retain it for debugging).

   Tune the flags for the request:
   - `--sub-langs` — comma-separated language priority for captions.
     Default covers English and Russian. For other languages add them
     (e.g. `"es,de,en"`). The first language that yields a caption
     track wins; later ones are ignored.
   - `--max-comments` — cap on top-level comments kept in the output
     (default 50). Raise it for a deeper opinion sample on popular
     videos; lower it to save tokens.

   The binary is resilient to partial failures: if one subtitle language
     hits HTTP 429 but another succeeded, it still produces output and
     prints a stderr note. Only a total failure (no `info.json` produced)
     exits non-zero; in that case stderr carries the yt-dlp `ERROR:`
     line — surface it to the user verbatim and stop.

3. Read the three output files (paths are in the stdout JSON):
   - `transcript.txt` — cleaned, de-duplicated plain text. Auto-generated
     rolling captions are merged via longest suffix/prefix overlap, so
     this is readable running prose, not the stuttering repeated cues
     that YouTube emits.
   - `metadata.json` — title, channel, uploader, upload_date (ISO),
     duration, view/like/comment counts, categories, tags, description,
     availability.
   - `comments.json` — `{total_fetched, top_level_count, replies_count,
     comments[]}` where each comment is `{author, text, like_count,
     is_top_level, is_from_uploader}`, sorted by `like_count` descending
     (top-level first, then top replies).

4. Produce the analysis. **Respond in the same language the user wrote
   in** (default: Russian if the user wrote in Russian). Structure:

   ### О чём видео
   - 2–4 предложения: тема, главный тезис, контекст (канал, дата,
     просмотры/лайки — взять из `metadata.json`).
   - Ключевые тезисы буллет-листом (5–8 пунктов), выведенные из
     `transcript.txt`. Цитировать дословно не нужно — пересказывать.

   ### Общественное мнение
   - **Тональность** — распределение по трём корзинам (позитивная /
     нейтральная / негативная / критическая) с примерами из
     `comments.json`. Указывать доли примерно ("~70% хвалят, ~20%
     нейтральны, ~10% критикуют").
   - **Повторяющиеся темы** — 3–5 тем, которые всплывают в нескольких
     комментариях (например "сравнение с React", "ностальгия по старому
     вебу", "жалобы на документацию"). Для каждой — 1–2 предложения и
     1 характерная цитата.
   - **Топ-комментарии** — 3–5 самых залайканных (по `like_count`), с
     автором и короткой характеристикой каждого ("кратко поддерживает
     подход", "делится своим опытом 25 лет", "возражает против тезиса
     X").
   - **Маркеры контроверсии** — есть ли жаркие споры (много ответов с
     противоположными настроениями под одним топ-комментарием), признаков
     бот-активности, brigading и т.п. Если ничего такого — так и сказать:
     "контроверсии не наблюдается".

   ### Вывод
   - 1–2 предложения: совпадает ли общественное мнение с посылом видео,
     или комментаторы видят видео иначе, чем его автор.

5. Token discipline: `comments.json` is already trimmed to the most-liked
   comments. Do NOT paste the full transcript into the answer —
   synthesize. Quote at most one short sentence per theme. If
   `transcript.txt` is very long (>30k chars), summarize sections rather
   than reading it all again.

6. **Cleanup**: after producing the analysis, remove the temp directory to
   avoid accumulating cruft in `/tmp`:

   ```bash
   rm -rf "<out_dir>"
   ```

   The `out_dir` path is in the stdout JSON from step 2. If the user
   passes `--out DIR` explicitly, ask before deleting — it's their
   chosen path.

## Edge Cases

- **No captions available** (`transcript.txt` empty, yt-dlp printed
  "unable to download subtitles"): tell the user the video has no
  captions and the skill cannot transcribe audio itself. Offer to
  analyze just the metadata + comments if useful. (A local
  faster-whisper fallback is intentionally NOT included to keep the
  skill dependency-free; it can be added later if needed.)
- **Comments disabled** (`comments.json` has `total_fetched: 0`): skip
  the public-opinion section and say so. Still deliver the content
  summary from the transcript.
- **Age-restricted / private / region-locked**: yt-dlp prints an
  `ERROR:` line and exits non-zero before producing `info.json`. Surface
  the error verbatim and stop; do not attempt workarounds.
- **HTTP 429 on some subtitle languages**: the binary continues with
  whatever captions it did get. If NO captions succeeded, treat as the
  "no captions" case above.
- **Non-English / non-Russian video**: set `--sub-langs` accordingly.
  If the user does not specify, try the default first; if the only
  available caption track is in another language, the transcript will
  come out in that language — still analyze it (the LLM can handle the
  language) and write the *analysis* in the user's language.

## Files

- `scripts/yt-insight` — built Go binary (run `install.sh` to rebuild).
- `scripts/install.sh` — builds the binary and checks for `yt-dlp`.
- `tool/cmd/yt-insight/main.go` — the source. Rebuild with `install.sh` after
  editing.
- `tool/go.mod` — Go module (stdlib only, no external dependencies).
