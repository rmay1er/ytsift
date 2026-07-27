// Command yt-insight is a CLI tool for AI agents. It orchestrates yt-dlp
// to fetch a YouTube video's transcript, metadata and comments, then
// emits clean, LLM-friendly output files and a one-line JSON summary on
// stdout that the agent can parse to locate them.
//
// stdout contract: exactly one JSON object on the last line (the
// summary). All yt-dlp progress and diagnostics go to stderr, so an
// agent can capture stdout with `... | tail -1` and json.Unmarshal it.
//
// yt-dlp handles the YouTube protocol work (signatures, Innertube,
// comment continuation tokens, subtitle URLs). This program does the
// data shaping: VTT/SRT cleaning, rolling-caption de-duplication,
// comment ranking, metadata projection.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const usage = `yt-insight — YouTube transcript + comments + metadata fetcher (CLI for AI agents)

Usage:
  yt-insight fetch <url-or-video-id> [flags]

Flags:
  --out DIR          output directory (default: $TMPDIR/yt-insight/<videoId>)
  --max-comments N   max top-level comments to keep in output (default 50)
  --sub-langs LIST   subtitle language priority, comma-separated (default "en,ru,en.*,ru.*")
  --workdir DIR      scratch dir for yt-dlp artifacts (default: <out>/.work)
  --keep-work        retain the scratch dir after success (default: removed)

stdout: a single JSON object with absolute paths to the outputs:
  {"video_id","out_dir","transcript","metadata","comments",
   "transcript_chars","transcript_lines"[,"work_dir"]}
stderr: yt-dlp progress and any non-fatal diagnostics.

Files written:
  <out>/transcript.txt   cleaned, de-duplicated plain-text transcript
  <out>/metadata.json    compact video metadata (title, channel, counts, ...)
  <out>/comments.json    top comments by likes, ready for LLM analysis`

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" {
		fmt.Println(usage)
		os.Exit(0)
	}
	switch os.Args[1] {
	case "fetch":
		if err := runFetch(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "yt-insight: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n%s\n", os.Args[1], usage)
		os.Exit(2)
	}
}

func runFetch(args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	var (
		out       = fs.String("out", "", "output directory")
		maxCom    = fs.Int("max-comments", 50, "max top-level comments to keep")
		subLangs  = fs.String("sub-langs", "en,ru,en.*,ru.*", "subtitle language priority")
		workdir   = fs.String("workdir", "", "scratch dir for yt-dlp artifacts")
		keepWork  = fs.Bool("keep-work", false, "retain the .work scratch dir (default: removed on success)")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return errors.New("expected exactly one YouTube URL argument")
	}
	url := rest[0]

	vid, err := videoID(url)
	if err != nil {
		return fmt.Errorf("parse video id: %w", err)
	}

	outDir := *out
	if outDir == "" {
		tmp := os.TempDir()
		outDir = filepath.Join(tmp, "yt-insight", vid)
	}
	work := *workdir
	if work == "" {
		work = filepath.Join(outDir, ".work")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}

	ytdErr := runYtDlp(url, work, *subLangs, *maxCom)
	// yt-dlp returns non-zero for partial failures (e.g. one subtitle
	// language hit HTTP 429 after others succeeded). Proceed as long as
	// the info.json we depend on actually landed on disk.
	infoPath, err := findFile(work, vid, ".info.json")
	if err != nil {
		if ytdErr != nil {
			return fmt.Errorf("yt-dlp failed and no info.json produced: %w", ytdErr)
		}
		return fmt.Errorf("locate info.json: %w", err)
	}
	if ytdErr != nil {
		fmt.Fprintf(os.Stderr, "yt-insight: yt-dlp reported errors (continuing with partial data): %v\n", ytdErr)
	}

	raw, err := os.ReadFile(infoPath)
	if err != nil {
		return err
	}
	var info map[string]any
	if err := json.Unmarshal(raw, &info); err != nil {
		return fmt.Errorf("parse info.json: %w", err)
	}

	if err := writeMetadata(outDir, vid, url, info); err != nil {
		return err
	}
	if err := writeComments(outDir, *maxCom, info); err != nil {
		return err
	}

	// Transcript: prefer manual subs over auto, try every written file.
	transcript, terr := buildTranscript(work, vid)
	if terr != nil {
		transcript = "" // absent captions are non-fatal
	}
	if err := os.WriteFile(filepath.Join(outDir, "transcript.txt"), []byte(transcript), 0o644); err != nil {
		return err
	}

	summary := map[string]any{
		"video_id":         vid,
		"out_dir":          outDir,
		"transcript_chars": len(transcript),
		"transcript_lines": strings.Count(transcript, "\n") + 1,
		"transcript":       filepath.Join(outDir, "transcript.txt"),
		"metadata":         filepath.Join(outDir, "metadata.json"),
		"comments":         filepath.Join(outDir, "comments.json"),
	}
	if !*keepWork {
		if rmErr := os.RemoveAll(work); rmErr != nil {
			fmt.Fprintf(os.Stderr, "yt-insight: could not remove workdir %s: %v\n", work, rmErr)
		}
	} else {
		summary["work_dir"] = work
	}
	b, _ := json.Marshal(summary)
	fmt.Println(string(b))
	return nil
}

// runYtDlp invokes yt-dlp to write info.json (with comments), subtitles,
// and nothing else. max-comments maps to yt-dlp's extractor arg
// "max-comments,max-parents,max-replies,max-replies-per-thread,max-depth":
// we cap top-level at max, allow a few replies per thread, depth 2.
func runYtDlp(url, work, subLangs string, maxComments int) error {
	maxArgs := fmt.Sprintf("youtube:max_comments=%d,all,all,5,2", maxComments)
	cmd := exec.Command("yt-dlp",
		"--skip-download",
		"--ignore-errors",
		"--write-info-json",
		"--write-comments",
		"--write-subs",
		"--write-auto-subs",
		"--sub-format", "vtt/srt/best",
		"--sub-langs", subLangs,
		"--no-clean-info-json",
		"--no-playlist",
		"--extractor-args", maxArgs,
		"-o", filepath.Join(work, "%(id)s.%(ext)s"),
		url,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

var (
	idRe   = regexp.MustCompile(`[A-Za-z0-9_-]{11}`)
	hostRe = regexp.MustCompile(`(?:youtu\.be/|youtube\.com/(?:watch\?(?:.*&)?v=|shorts/|embed/|v/))`)
)

func videoID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !hostRe.MatchString(s) {
		// bare id
		if len(s) == 11 && idRe.MatchString(s) {
			return s, nil
		}
		return "", fmt.Errorf("not a youtube url or 11-char id: %q", s)
	}
	if m := idRe.FindString(hostRe.ReplaceAllString(s, "")); m != "" {
		return m, nil
	}
	return "", errors.New("could not extract 11-char video id")
}

func findFile(dir, vid, suffix string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, vid+suffix))
	if err != nil || len(matches) == 0 {
		// fall back to any file with the suffix (yt-dlp may use .<lang> before ext)
		matches, err = filepath.Glob(filepath.Join(dir, "*"+suffix))
		if err != nil || len(matches) == 0 {
			return "", errors.New("no matching file")
		}
	}
	return matches[0], nil
}

// ---------- metadata ----------

func writeMetadata(outDir, vid, srcURL string, info map[string]any) error {
	uploadDate := str(info, "upload_date")
	if len(uploadDate) == 8 {
		uploadDate = uploadDate[:4] + "-" + uploadDate[4:6] + "-" + uploadDate[6:]
	}
	dur := num(info, "duration")
	meta := map[string]any{
		"id":                 vid,
		"url":                srcURL,
		"title":             str(info, "title"),
		"channel":           str(info, "channel"),
		"uploader":          str(info, "uploader"),
		"upload_date":       uploadDate,
		"duration_seconds":  dur,
		"duration_hmmss":    fmtDur(dur),
		"view_count":       num(info, "view_count"),
		"like_count":       num(info, "like_count"),
		"comment_count":     num(info, "comment_count"),
		"categories":        info["categories"],
		"tags":              info["tags"],
		"availability":      str(info, "availability"),
		"description":       str(info, "description"),
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "metadata.json"), b, 0o644)
}

func fmtDur(sec float64) string {
	if sec <= 0 {
		return ""
	}
	h := int(sec) / 3600
	m := (int(sec) % 3600) / 60
	s := int(sec) % 60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// ---------- comments ----------

type outComment struct {
	Author         string `json:"author"`
	Text           string `json:"text"`
	LikeCount      int    `json:"like_count"`
	IsTopLevel     bool   `json:"is_top_level"`
	IsFromUploader bool   `json:"is_from_uploader"`
}

func writeComments(outDir string, max int, info map[string]any) error {
	raw, _ := info["comments"].([]any)
	top := make([]outComment, 0, len(raw))
	all := make([]outComment, 0, len(raw))
	for _, r := range raw {
		c, ok := r.(map[string]any)
		if !ok {
			continue
		}
		text := strings.TrimSpace(str(c, "text"))
		if text == "" {
			continue
		}
		oc := outComment{
			Author:         str(c, "author"),
			Text:           text,
			LikeCount:      int(num(c, "like_count")),
			IsTopLevel:     str(c, "parent") == "root",
			IsFromUploader: boolVal(c, "author_is_uploader"),
		}
		if oc.IsTopLevel {
			top = append(top, oc)
		} else {
			all = append(all, oc)
		}
	}
	sort.SliceStable(top, func(i, j int) bool { return top[i].LikeCount > top[j].LikeCount })
	sort.SliceStable(all, func(i, j int) bool { return all[i].LikeCount > all[j].LikeCount })

	if max > 0 && len(top) > max {
		top = top[:max]
	}
	// keep a handful of top replies too, so the agent sees discussion
	if len(all) > max/2 && max > 0 {
		all = all[:max / 2]
	}
	merged := append(top, all...)
	out := map[string]any{
		"video_id":          str(info, "id"),
		"total_fetched":     len(raw),
		"top_level_count":   len(top),
		"replies_count":     len(all),
		"comments":          merged,
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "comments.json"), b, 0o644)
}

// ---------- transcript cleaning ----------

var (
	tagRe     = regexp.MustCompile(`<[^>]*>`)
	timingRe  = regexp.MustCompile(`^\s*\d{1,2}:\d{2}(:\d{2})?([.,]\d+)?\s*-->\s*\d{1,2}:\d{2}(:\d{2})?([.,]\d+)?\s*([a-zA-Z][^:]*?:.*)?\s*$`)
	idxRe     = regexp.MustCompile(`^\s*\d+\s*$`)
	multiSpace = regexp.MustCompile(`[ \t]{2,}`)
)

func buildTranscript(work, vid string) (string, error) {
	// prefer manual subs: files without "auto" in name
	files, _ := filepath.Glob(filepath.Join(work, vid+".*"))
	var subFile string
	for _, f := range files {
		ext := filepath.Ext(f)
		if ext == ".vtt" || ext == ".srt" {
			if !strings.Contains(strings.ToLower(f), "auto") {
				subFile = f
				break
			}
			if subFile == "" {
				subFile = f
			}
		}
	}
	if subFile == "" {
		return "", errors.New("no subtitle file written")
	}
	data, err := os.ReadFile(subFile)
	if err != nil {
		return "", err
	}
	return clean(string(data)), nil
}

// clean parses VTT/SRT into plain text, stripping timing, tags and
// entities, then collapsing YouTube's rolling auto-caption duplicates:
// consecutive cues that are prefixes/supersets of one another merge
// into the longer line instead of being printed twice.
func clean(s string) string {
	var (
		blocks  []string
		cur     []string
		flush   = func() {
			if len(cur) > 0 {
				blocks = append(blocks, strings.Join(cur, " "))
			}
			cur = cur[:0]
		}
	)
	lines := strings.Split(s, "\n")
	skipNote := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		switch {
		case t == "":
			flush()
			skipNote = false
			continue
		case strings.HasPrefix(t, "WEBVTT"), strings.HasPrefix(t, "Kind:"), strings.HasPrefix(t, "Language:"), strings.HasPrefix(t, "X-TIMESTAMP"):
			continue
		case strings.HasPrefix(t, "NOTE"):
			skipNote = true
			continue
		case skipNote:
			continue
		case timingRe.MatchString(t), idxRe.MatchString(t):
			continue
		}
		cur = append(cur, htmlUnescape(tagRe.ReplaceAllString(t, "")))
	}
	flush()

	// YouTube auto-captions emit rolling cues: each cue drops a few
	// leading words from the previous one and appends a few new words,
	// so consecutive cues overlap heavily but are NOT substrings of
	// each other. We merge by finding the longest word-aligned suffix
	// of the running text that is also a prefix of the next cue, and
	// only appending the non-overlapping tail. Manual captions (no
	// overlap) fall through with zero overlap and are concatenated
	// verbatim, separated by a space.
	var acc []string
	for _, b := range blocks {
		b = strings.TrimSpace(multiSpace.ReplaceAllString(b, " "))
		if b == "" {
			continue
		}
		if len(acc) == 0 {
			acc = strings.Fields(b)
			continue
		}
		next := strings.Fields(b)
		k := overlapWords(acc, next)
		if k == 0 {
			acc = append(acc, next...)
		} else {
			acc = append(acc, next[k:]...)
		}
	}
	return strings.Join(acc, " ") + "\n"
}

// overlapWords returns the largest k (0 <= k <= min(len(a),len(b))) such
// that the last k words of a equal the first k words of b. Word
// comparison is case-insensitive to survive capitalization drift.
func overlapWords(a, b []string) int {
	max := len(a)
	if len(b) < max {
		max = len(b)
	}
	for k := max; k > 0; k-- {
		ok := true
		for i := 0; i < k; i++ {
			if !strings.EqualFold(a[len(a)-k+i], b[i]) {
				ok = false
				break
			}
		}
		if ok {
			return k
		}
	}
	return 0
}

// ---------- helpers ----------

func str(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func num(m map[string]any, k string) float64 {
	switch v := m[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return 0
}

func boolVal(m map[string]any, k string) bool {
	if v, ok := m[k]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

var entityRe = regexp.MustCompile(`&[a-zA-Z#0-9]+;`)

func htmlUnescape(s string) string {
	return entityRe.ReplaceAllStringFunc(s, func(e string) string {
		switch e {
		case "&amp;":
			return "&"
		case "&lt;":
			return "<"
		case "&gt;":
			return ">"
		case "&quot;":
			return `"`
		case "&#39;", "&apos;", "&#x27;":
			return "'"
		case "&#x2F;":
			return "/"
		case "&nbsp;":
			return " "
		}
		// numeric: &#NN; or &#xHH;
		if strings.HasPrefix(e, "&#") {
			body := strings.TrimSuffix(strings.TrimPrefix(e, "&#"), ";")
			base := 10
			if strings.HasPrefix(body, "x") || strings.HasPrefix(body, "X") {
				body = body[1:]
				base = 16
			}
			var n int
			for _, ch := range body {
				d := byte(0)
				switch {
				case ch >= '0' && ch <= '9':
					d = byte(ch - '0')
				case ch >= 'a' && ch <= 'f':
					d = byte(ch-'a') + 10
				case ch >= 'A' && ch <= 'F':
					d = byte(ch-'A') + 10
				default:
					return e
				}
				n = n*base + int(d)
			}
			if n > 0 {
				return string(rune(n))
			}
		}
		return e
	})
}
