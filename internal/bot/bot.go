package bot

import (
    "context"
    "errors"
    "fmt"
    "log"
    "strings"
    "sync"
    "time"

    "github.com/bwmarrin/discordgo"

    "kvazar/internal/media"
)

// Config encapsulates boot parameters for the Kvazar bot.
type Config struct {
    Token      string
    FFMpegPath string
    YTDLPPath  string
    Status     string
}

// Kvazar represents the runtime bot instance.
type Kvazar struct {
    session    *discordgo.Session
    resolver   *media.Resolver
    ffmpegPath string
    players    map[string]*Player
    playersMu  sync.RWMutex
    commands   []*discordgo.ApplicationCommand
    status     string
}

// New constructs a Kvazar bot from the provided configuration.
func New(cfg Config) (*Kvazar, error) {
    if strings.TrimSpace(cfg.Token) == "" {
        return nil, errors.New("bot token must be provided")
    }

    sess, err := discordgo.New("Bot " + cfg.Token)
    if err != nil {
        return nil, fmt.Errorf("discord session: %w", err)
    }

    sess.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildVoiceStates

    bot := &Kvazar{
        session:    sess,
        resolver:   media.NewResolver(cfg.YTDLPPath),
        ffmpegPath: pickOrDefault(cfg.FFMpegPath, "ffmpeg"),
        players:    make(map[string]*Player),
        status:     cfg.Status,
    }

    sess.AddHandler(bot.onReady)
    sess.AddHandler(bot.onInteractionCreate)

    return bot, nil
}

// Open starts the Discord session and registers commands.
func (k *Kvazar) Open(ctx context.Context) error {
    if err := k.session.Open(); err != nil {
        return fmt.Errorf("open session: %w", err)
    }

    if err := k.registerCommands(ctx); err != nil {
        return err
    }

    if k.status != "" {
        _ = k.session.UpdateListeningStatus(k.status)
    } else {
        _ = k.session.UpdateListeningStatus("/play")
    }

    return nil
}

// Close deregisters commands and closes the Discord session.
func (k *Kvazar) Close(ctx context.Context) error {
    if err := k.unregisterCommands(ctx); err != nil {
        log.Printf("warning: failed to cleanup commands: %v", err)
    }
    for _, player := range k.snapshotPlayers() {
        player.Shutdown()
    }
    return k.session.Close()
}

func (k *Kvazar) registerCommands(ctx context.Context) error {
    appID := k.session.State.User.ID
    if appID == "" {
        return errors.New("application ID unavailable; ensure session is open")
    }
    k.commands = make([]*discordgo.ApplicationCommand, 0, len(globalCommands))
    for _, cmd := range globalCommands {
        created, err := k.session.ApplicationCommandCreate(appID, "", cmd)
        if err != nil {
            return fmt.Errorf("register command %s: %w", cmd.Name, err)
        }
        k.commands = append(k.commands, created)
    }
    return nil
}

func (k *Kvazar) unregisterCommands(ctx context.Context) error {
    appID := k.session.State.User.ID
    if appID == "" {
        return nil
    }
    for _, cmd := range k.commands {
        if err := k.session.ApplicationCommandDelete(appID, "", cmd.ID); err != nil {
            log.Printf("warning: failed to delete command %s: %v", cmd.Name, err)
        }
    }
    k.commands = nil
    return nil
}

func (k *Kvazar) onReady(_ *discordgo.Session, event *discordgo.Ready) {
    log.Printf("kvazar connected as %s#%s", event.User.Username, event.User.Discriminator)
}

func (k *Kvazar) onInteractionCreate(s *discordgo.Session, ic *discordgo.InteractionCreate) {
    if ic.Type != discordgo.InteractionApplicationCommand {
        return
    }

    switch ic.ApplicationCommandData().Name {
    case commandPlay:
        k.handlePlay(ic)
    case commandSkip:
        k.handleSkip(ic)
    case commandLoop:
        k.handleLoop(ic)
    }
}

func (k *Kvazar) handlePlay(ic *discordgo.InteractionCreate) {
    data := ic.ApplicationCommandData()
    if len(data.Options) == 0 {
        k.respondError(ic, "Please provide a query or URL to play.")
        return
    }

    query := strings.TrimSpace(data.Options[0].StringValue())
    if query == "" {
        k.respondError(ic, "Please provide a non-empty query.")
        return
    }

    guildID := ic.GuildID
    if guildID == "" {
        k.respondError(ic, "This command can only be used within a server.")
        return
    }

    userID := ic.Member.User.ID
    voiceChannel, err := locateVoiceChannel(k.session, guildID, userID)
    if err != nil {
        k.respondError(ic, "You must be connected to a voice channel to use /play.")
        return
    }

    if err := k.session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
        Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
        Data: &discordgo.InteractionResponseData{
            Content: "Preparing your track…",
            Flags:   discordgo.MessageFlagsEphemeral,
        },
    }); err != nil {
        log.Printf("failed to acknowledge interaction: %v", err)
        return
    }

    requestedBy := fmt.Sprintf("<@%s>", userID)

    go k.fulfilPlay(ic, query, voiceChannel, requestedBy)
}

func (k *Kvazar) fulfilPlay(ic *discordgo.InteractionCreate, query, voiceChannel, requestedBy string) {
    guildID := ic.GuildID
    player := k.getPlayer(guildID)

    if err := player.EnsureConnected(voiceChannel); err != nil {
        k.editInteractionError(ic, fmt.Sprintf("Failed to join voice channel: %v", err))
        return
    }

    ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
    defer cancel()

    track, err := k.resolver.Resolve(ctx, query, requestedBy, ic.ChannelID)
    if err != nil {
        k.editInteractionError(ic, fmt.Sprintf("Could not resolve track: %v", err))
        return
    }

    position := player.Enqueue(track)

    message := fmt.Sprintf("Queued **%s** — position #%d.", track.Title, position)
    embed := buildQueuedEmbed(track, position)
    embeds := []*discordgo.MessageEmbed{embed}

    if _, err := k.session.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
        Content: stringPtr(message),
        Embeds:  &embeds,
    }); err != nil {
        log.Printf("failed to edit interaction response: %v", err)
    }
}

func (k *Kvazar) editInteractionError(ic *discordgo.InteractionCreate, message string) {
    empty := []*discordgo.MessageEmbed{}
    if _, err := k.session.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
        Content: stringPtr(message),
        Embeds:  &empty,
    }); err != nil {
        log.Printf("failed to edit interaction response: %v", err)
    }
}

func (k *Kvazar) handleSkip(ic *discordgo.InteractionCreate) {
    player := k.findPlayer(ic.GuildID)
    if player == nil {
        k.respondError(ic, "Nothing is playing right now.")
        return
    }

    if !player.Skip() {
        k.respondError(ic, "There is no active track to skip.")
        return
    }

    k.respondSuccess(ic, "Skipped the current track.")
}

func (k *Kvazar) handleLoop(ic *discordgo.InteractionCreate) {
    player := k.findPlayer(ic.GuildID)
    if player == nil {
        k.respondError(ic, "Nothing is playing to loop.")
        return
    }

    var explicit *bool
    if len(ic.ApplicationCommandData().Options) > 0 {
        value := ic.ApplicationCommandData().Options[0].BoolValue()
        explicit = &value
    }

    state := player.ToggleLoop(explicit)
    if state {
        k.respondSuccess(ic, "Loop enabled for the current track.")
    } else {
        k.respondSuccess(ic, "Loop disabled.")
    }
}

func (k *Kvazar) respondError(ic *discordgo.InteractionCreate, message string) {
    _ = k.session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
        Type: discordgo.InteractionResponseChannelMessageWithSource,
        Data: &discordgo.InteractionResponseData{
            Content: message,
            Flags:   discordgo.MessageFlagsEphemeral,
        },
    })
}

func (k *Kvazar) respondSuccess(ic *discordgo.InteractionCreate, message string) {
    _ = k.session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
        Type: discordgo.InteractionResponseChannelMessageWithSource,
        Data: &discordgo.InteractionResponseData{Content: message},
    })
}

func (k *Kvazar) getPlayer(guildID string) *Player {
    k.playersMu.Lock()
    defer k.playersMu.Unlock()
    if player, ok := k.players[guildID]; ok {
        return player
    }
    player := NewPlayer(k, guildID)
    k.players[guildID] = player
    return player
}

func (k *Kvazar) findPlayer(guildID string) *Player {
    k.playersMu.RLock()
    defer k.playersMu.RUnlock()
    return k.players[guildID]
}

func (k *Kvazar) releasePlayer(guildID string) {
    k.playersMu.Lock()
    defer k.playersMu.Unlock()
    delete(k.players, guildID)
}

func (k *Kvazar) snapshotPlayers() []*Player {
    k.playersMu.RLock()
    defer k.playersMu.RUnlock()
    res := make([]*Player, 0, len(k.players))
    for _, player := range k.players {
        res = append(res, player)
    }
    return res
}

func (k *Kvazar) announceNowPlaying(track *media.Track, loop bool) {
    if track.RequestChannelID == "" {
        return
    }
    embed := buildNowPlayingEmbed(track, loop)
    if _, err := k.session.ChannelMessageSendEmbed(track.RequestChannelID, embed); err != nil {
        log.Printf("failed to send now playing message: %v", err)
    }
}

func pickOrDefault(value, fallback string) string {
    if strings.TrimSpace(value) == "" {
        return fallback
    }
    return value
}

func buildQueuedEmbed(track *media.Track, position int) *discordgo.MessageEmbed {
    title := fmt.Sprintf("Queued • %s", track.Title)
    if position == 1 {
        title = fmt.Sprintf("Up next • %s", track.Title)
    }

    fields := []*discordgo.MessageEmbedField{
        {Name: "Length", Value: track.HumanDuration(), Inline: true},
        {Name: "Source", Value: string(track.Source), Inline: true},
    }
    if track.RequestedBy != "" {
        fields = append(fields, &discordgo.MessageEmbedField{Name: "Requested by", Value: track.RequestedBy, Inline: true})
    }
    fields = append(fields, &discordgo.MessageEmbedField{Name: "Position", Value: fmt.Sprintf("#%d", position), Inline: true})

    return &discordgo.MessageEmbed{
        Title:       title,
        URL:         track.WebURL,
        Description: "Kvazar keeps your session light and focused.",
        Color:       0x5865F2,
        Timestamp:   time.Now().UTC().Format(time.RFC3339),
        Thumbnail: &discordgo.MessageEmbedThumbnail{
            URL: track.Thumbnail,
        },
        Fields: fields,
        Footer: &discordgo.MessageEmbedFooter{Text: "Kvazar • minimal music companion"},
    }
}

func buildNowPlayingEmbed(track *media.Track, loop bool) *discordgo.MessageEmbed {
    status := "Now playing"
    if loop {
        status = "Looping"
    }

    return &discordgo.MessageEmbed{
        Title:     fmt.Sprintf("%s • %s", status, track.Title),
        URL:       track.WebURL,
        Color:     0x1ABC9C,
        Timestamp: time.Now().UTC().Format(time.RFC3339),
        Thumbnail: &discordgo.MessageEmbedThumbnail{URL: track.Thumbnail},
        Fields: []*discordgo.MessageEmbedField{
            {Name: "Length", Value: track.HumanDuration(), Inline: true},
            {Name: "Source", Value: string(track.Source), Inline: true},
        },
        Footer: &discordgo.MessageEmbedFooter{Text: "Kvazar • stay in flow"},
    }
}

func stringPtr(value string) *string {
    return &value
}
