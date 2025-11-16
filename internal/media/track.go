package media

import (
    "fmt"
    "strings"
    "time"
)

// Source represents the origin platform of a track.
type Source string

const (
    SourceYouTube    Source = "YouTube"
    SourceSoundCloud Source = "SoundCloud"
    SourceUnknown    Source = "Unknown"
)

// Track holds metadata about an item queued for playback.
type Track struct {
    ID               string
    Title            string
    Author           string
    WebURL           string
    StreamURL        string
    Thumbnail        string
    Duration         time.Duration
    Source           Source
    RequestedBy      string
    RequestChannelID string
    HTTPHeaders      map[string]string
    QueuedAt         time.Time
}

// Label builds a compact human readable identifier for the track.
func (t Track) Label() string {
    source := string(t.Source)
    if source == "" {
        source = string(SourceUnknown)
    }
    return fmt.Sprintf("%s — %s", strings.TrimSpace(t.Title), source)
}

// HumanDuration renders the duration in an mm:ss or hh:mm:ss format.
func (t Track) HumanDuration() string {
    if t.Duration == 0 {
        return "live"
    }
    seconds := int(t.Duration.Seconds())
    hours := seconds / 3600
    minutes := (seconds % 3600) / 60
    secs := seconds % 60
    if hours > 0 {
        return fmt.Sprintf("%d:%02d:%02d", hours, minutes, secs)
    }
    return fmt.Sprintf("%02d:%02d", minutes, secs)
}
