package media

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultResolverExecutable = "yt-dlp"
	defaultResolveTimeout     = 20 * time.Second
)

// Resolver discovers media metadata and stream URLs using yt-dlp.
type Resolver struct {
	Executable string
	Timeout    time.Duration
}

// NewResolver constructs a Resolver with sane defaults.
func NewResolver(path string) *Resolver {
	if strings.TrimSpace(path) == "" {
		path = defaultResolverExecutable
	}
	return &Resolver{Executable: path, Timeout: defaultResolveTimeout}
}

// Resolve attempts to resolve a query or URL into a Track description.
func (r *Resolver) Resolve(ctx context.Context, query, requestedBy, channelID string) (*Track, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	realQuery := prepareQuery(query)
	args := []string{
		"--no-playlist",
		"--ignore-errors",
		"--dump-json",
		"--no-warnings",
		"-f", "bestaudio[ext=m4a]/bestaudio[ext=webm]/bestaudio/best",
		"--audio-quality", "0",
		realQuery,
	}

	cmd := exec.CommandContext(ctx, r.Executable, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("resolver: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("resolver: start yt-dlp: %w", err)
	}

	dec := json.NewDecoder(bufio.NewReader(stdout))
	var payload ytdlpItem
	if err := dec.Decode(&payload); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("resolver: decode response: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("resolver: timeout reached after %s", r.Timeout)
		}
		return nil, fmt.Errorf("resolver: yt-dlp failed: %s", strings.TrimSpace(stderr.String()))
	}

	track := mapPayloadToTrack(payload)
	track.RequestedBy = requestedBy
	track.RequestChannelID = channelID
	track.QueuedAt = time.Now()

	return track, nil
}

type ytdlpItem struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Uploader    string            `json:"uploader"`
	WebpageURL  string            `json:"webpage_url"`
	Duration    json.Number       `json:"duration"`
	URL         string            `json:"url"`
	Thumbnail   string            `json:"thumbnail"`
	Extractor   string            `json:"extractor_key"`
	HTTPHeaders map[string]string `json:"http_headers"`
}

func mapPayloadToTrack(item ytdlpItem) *Track {
	duration := time.Duration(0)
	if item.Duration != "" {
		if seconds, err := item.Duration.Float64(); err == nil && seconds > 0 {
			duration = time.Duration(seconds * float64(time.Second))
		}
	}

	source := SourceUnknown
	key := strings.ToLower(item.Extractor)
	switch {
	case strings.Contains(key, "youtube"):
		source = SourceYouTube
	case strings.Contains(key, "soundcloud"):
		source = SourceSoundCloud
	}

	return &Track{
		ID:          item.ID,
		Title:       item.Title,
		Author:      item.Uploader,
		WebURL:      fallbackURL(item.WebpageURL, item.URL),
		StreamURL:   item.URL,
		Thumbnail:   item.Thumbnail,
		Duration:    duration,
		Source:      source,
		HTTPHeaders: item.HTTPHeaders,
	}
}

func prepareQuery(q string) string {
	trimmed := strings.TrimSpace(q)
	if trimmed == "" {
		return trimmed
	}
	if looksLikeURL(trimmed) {
		return trimmed
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "sc ") {
		return "scsearch:" + strings.TrimSpace(trimmed[3:])
	}
	return "ytsearch:" + trimmed
}

func looksLikeURL(value string) bool {
	if !strings.Contains(value, "://") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

func fallbackURL(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
