package bot

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"layeh.com/gopus"

	"kvazar/internal/media"
)

const (
	pcmFrameSize      = 960 // 20ms at 48kHz
	pcmChannelCount   = 2
	sampleRate        = 48000
	opusBitrate       = 128000 // 128 kbps for high quality
	opusFrameCapacity = 4096
	disconnectDelay   = 90 * time.Second
)

// Player manages playback for a single guild.
type Player struct {
	bot    *Kvazar
	guild  string

	mu             sync.Mutex
	queue          []*media.Track
	current        *media.Track
	loop           bool
	playing        bool
	paused         bool
	skipRequested  bool
	cancelPlayback context.CancelFunc
	pauseChan      chan bool

	voice           *discordgo.VoiceConnection
	disconnectTimer *time.Timer
}

// NewPlayer constructs a player instance bound to a guild.
func NewPlayer(bot *Kvazar, guildID string) *Player {
	return &Player{bot: bot, guild: guildID}
}

// EnsureConnected joins or moves the bot into the requested voice channel.
func (p *Player) EnsureConnected(channelID string) error {
	p.mu.Lock()
	vc := p.voice
	p.mu.Unlock()

	if vc != nil && vc.ChannelID == channelID {
		return nil
	}

	if vc != nil {
		vc.Disconnect()
	}

	conn, err := p.bot.session.ChannelVoiceJoin(p.guild, channelID, false, true)
	if err != nil {
		return fmt.Errorf("voice join: %w", err)
	}

	time.Sleep(350 * time.Millisecond)
	p.mu.Lock()
	p.voice = conn
	p.mu.Unlock()
	return nil
}

// Enqueue adds the track to the playback queue and starts playback if idle.
func (p *Player) Enqueue(track *media.Track) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.queue = append(p.queue, track)
	position := len(p.queue)
	p.cancelDisconnectTimerLocked()

	if !p.playing {
		p.playing = true
		go p.playLoop()
	}

	return position
}

// Skip stops the current playback and advances to the next track. Returns false if nothing is playing.
func (p *Player) Skip() bool {
	p.mu.Lock()
	cancel := p.cancelPlayback
	active := p.current != nil
	if cancel != nil {
		p.skipRequested = true
		p.loop = false
		cancel()
	}
	p.mu.Unlock()
	return active
}

// Pause toggles the pause state. Returns true if now paused, false if resumed.
func (p *Player) Pause() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	if !p.playing || p.current == nil {
		return false
	}
	
	p.paused = !p.paused
	if p.pauseChan != nil {
		p.pauseChan <- p.paused
	}
	return p.paused
}

// Stop clears the queue and stops playback.
func (p *Player) Stop() bool {
	p.mu.Lock()
	cancel := p.cancelPlayback
	hadContent := p.current != nil || len(p.queue) > 0
	p.queue = nil
	p.current = nil
	p.loop = false
	p.paused = false
	if cancel != nil {
		p.skipRequested = true
		cancel()
	}
	p.mu.Unlock()
	return hadContent
}

// ToggleLoop toggles or explicitly sets loop state, returning the resulting value.
func (p *Player) ToggleLoop(explicit *bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if explicit != nil {
		p.loop = *explicit && p.current != nil
	} else if p.current != nil {
		p.loop = !p.loop
	}
	return p.loop
}

// Shutdown terminates playback and disconnects the voice connection.
func (p *Player) Shutdown() {
	p.mu.Lock()
	if p.cancelPlayback != nil {
		p.cancelPlayback()
	}
	vc := p.voice
	p.voice = nil
	p.mu.Unlock()

	if vc != nil {
		vc.Disconnect()
	}
}

func (p *Player) playLoop() {
	for {
		track, repeat := p.nextTrack()
		if track == nil {
			p.mu.Lock()
			p.playing = false
			p.paused = false
			p.scheduleDisconnectLocked()
			p.mu.Unlock()
			return
		}

		if !repeat {
			p.bot.announceNowPlaying(track, p.loop)
		}

		ctx, cancel := context.WithCancel(context.Background())
		p.mu.Lock()
		p.cancelPlayback = cancel
		p.pauseChan = make(chan bool, 1)
		p.mu.Unlock()

		err := p.streamTrack(ctx, track)

		p.mu.Lock()
		// Clear the cancel function after playback
		p.cancelPlayback = nil
		p.pauseChan = nil
		p.mu.Unlock()

		if errors.Is(err, context.Canceled) {
			continue
		}

		if err != nil {
			log.Printf("playback error: %v", err)
		}
	}
}

func (p *Player) nextTrack() (*media.Track, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.skipRequested = false

	// If loop is enabled and we have a current track, add it back to the queue
	if p.loop && p.current != nil {
		p.queue = append(p.queue, p.current)
	}

	if len(p.queue) == 0 {
		p.current = nil
		return nil, false
	}

	track := p.queue[0]
	p.queue = p.queue[1:]
	p.current = track
	return track, false
}

func (p *Player) streamTrack(ctx context.Context, track *media.Track) error {
	p.mu.Lock()
	vc := p.voice
	p.mu.Unlock()

	if vc == nil {
		return errors.New("voice connection not established")
	}

	opusEncoder, err := gopus.NewEncoder(sampleRate, pcmChannelCount, gopus.Audio)
	if err != nil {
		return fmt.Errorf("opus encoder: %w", err)
	}
	
	// Set high quality bitrate
	opusEncoder.SetBitrate(opusBitrate)

	cmdArgs := buildFFMpegArgs(track)
	cmd := exec.CommandContext(ctx, p.bot.ffmpegPath, cmdArgs...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ffmpeg stdout: %w", err)
	}

	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg start: %w", err)
	}

	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	reader := bufio.NewReader(stdout)
	pcmBuf := make([]int16, pcmFrameSize*pcmChannelCount)
	byteBuf := make([]byte, len(pcmBuf)*2)

	if err := vc.Speaking(true); err != nil {
		log.Printf("failed to set speaking state: %v", err)
	}
	defer func() {
		if err := vc.Speaking(false); err != nil {
			log.Printf("failed to disable speaking: %v", err)
		}
	}()

	for {
		if ctx.Err() != nil {
			return context.Canceled
		}

		// Handle pause state
		p.mu.Lock()
		pauseChan := p.pauseChan
		isPaused := p.paused
		p.mu.Unlock()

		if isPaused {
			select {
			case <-ctx.Done():
				return context.Canceled
			case paused := <-pauseChan:
				if !paused {
					// Resumed, continue
					continue
				}
			case <-time.After(100 * time.Millisecond):
				continue
			}
		}

		if _, err := io.ReadFull(reader, byteBuf); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return fmt.Errorf("pcm read: %w", err)
		}

		for i := 0; i < len(pcmBuf); i++ {
			pcmBuf[i] = int16(binary.LittleEndian.Uint16(byteBuf[i*2 : i*2+2]))
		}

		packet, err := opusEncoder.Encode(pcmBuf, pcmFrameSize, opusFrameCapacity)
		if err != nil {
			return fmt.Errorf("opus encode: %w", err)
		}

		select {
		case <-ctx.Done():
			return context.Canceled
		case vc.OpusSend <- packet:
		}
	}
}

func (p *Player) scheduleDisconnectLocked() {
	if p.disconnectTimer != nil {
		p.disconnectTimer.Stop()
	}
	p.disconnectTimer = time.AfterFunc(disconnectDelay, func() {
		p.mu.Lock()
		vc := p.voice
		p.voice = nil
		p.disconnectTimer = nil
		p.mu.Unlock()

		if vc != nil {
			vc.Disconnect()
		}

		p.bot.releasePlayer(p.guild)
	})
}

func (p *Player) cancelDisconnectTimerLocked() {
	if p.disconnectTimer != nil {
		p.disconnectTimer.Stop()
		p.disconnectTimer = nil
	}
}

func buildFFMpegArgs(track *media.Track) []string {
	args := []string{
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "5",
	}

	headerLines := headersToLines(track.HTTPHeaders)
	if headerLines != "" {
		args = append(args, "-headers", headerLines)
	}

	args = append(args,
		"-i", track.StreamURL,
		"-vn",
		"-af", "loudnorm=I=-16:LRA=11:TP=-1.5", // Audio normalization for better quality
		"-f", "s16le",
		"-ac", fmt.Sprintf("%d", pcmChannelCount),
		"-ar", fmt.Sprintf("%d", sampleRate),
		"pipe:1",
	)

	return args
}

func headersToLines(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(headers))
	for k, v := range headers {
		pairs = append(pairs, fmt.Sprintf("%s: %s", k, v))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "\r\n") + "\r\n"
}

// locateVoiceChannel ensures we can find the member's voice channel.
func locateVoiceChannel(session *discordgo.Session, guildID, userID string) (string, error) {
	if guildID == "" || userID == "" {
		return "", errors.New("missing guild or user identifier")
	}

	guild, err := session.State.Guild(guildID)
	if err != nil {
		guild, err = session.Guild(guildID)
		if err != nil {
			return "", fmt.Errorf("guild fetch: %w", err)
		}
	}

	for _, vs := range guild.VoiceStates {
		if vs.UserID == userID {
			return vs.ChannelID, nil
		}
	}

	return "", errors.New("voice channel not found")
}
