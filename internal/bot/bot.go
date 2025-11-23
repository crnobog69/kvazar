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

	// Set Do Not Disturb status
	_ = k.session.UpdateStatusComplex(discordgo.UpdateStatusData{
		Status: "dnd",
		Activities: []*discordgo.Activity{
			{
				Name: pickOrDefault(k.status, "/play"),
				Type: discordgo.ActivityTypeListening,
			},
		},
	})
	
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
    switch ic.Type {
    case discordgo.InteractionApplicationCommand:
        switch ic.ApplicationCommandData().Name {
        case commandPlay:
            k.handlePlay(ic)
        case commandPlayer:
            k.handlePlayer(ic)
        case commandPause:
            k.handlePause(ic)
        case commandStop:
            k.handleStop(ic)
        case commandSkip:
            k.handleSkip(ic)
        case commandLoop:
            k.handleLoop(ic)
        }
    case discordgo.InteractionMessageComponent:
        k.handleButtonClick(ic)
    }
}

func (k *Kvazar) handlePlay(ic *discordgo.InteractionCreate) {
    data := ic.ApplicationCommandData()
	if len(data.Options) == 0 {
		k.respondError(ic, "Молим те унеси упит или URL адресу.")
		return
	}

	query := strings.TrimSpace(data.Options[0].StringValue())
	if query == "" {
		k.respondError(ic, "Молим те унеси упит.")
		return
	}

	guildID := ic.GuildID
	if guildID == "" {
		k.respondError(ic, "Ова команда се може користити само на серверу.")
		return
	}

	userID := ic.Member.User.ID
	voiceChannel, err := locateVoiceChannel(k.session, guildID, userID)
	if err != nil {
		k.respondError(ic, "Мораш бити повезан на гласовни канал да би користио /play.")
		return
	}
	
	if err := k.session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "Припремам песму…",
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
        k.editInteractionError(ic, fmt.Sprintf("Неуспело повезивање на гласовни канал: %v", err))
        return
    }

    ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
    defer cancel()

    track, err := k.resolver.Resolve(ctx, query, requestedBy, ic.ChannelID)
    if err != nil {
        k.editInteractionError(ic, fmt.Sprintf("Не могу да пронађем песму: %v", err))
        return
    }

    position := player.Enqueue(track)

    message := fmt.Sprintf("У реду **%s** — позиција #%d.", track.Title, position)
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
		k.respondError(ic, "Ништа тренутно не свира.")
		return
	}

	if !player.Skip() {
		k.respondError(ic, "Нема активне песме за прескакање.")
		return
	}

	k.respondSuccess(ic, "Прескочена је тренутна песма.")
}

func (k *Kvazar) handlePlayer(ic *discordgo.InteractionCreate) {
	player := k.findPlayer(ic.GuildID)
	if player == nil {
		k.respondError(ic, "Ништа тренутно не свира.")
		return
	}

	player.mu.Lock()
	current := player.current
	queueLen := len(player.queue)
	loop := player.loop
	paused := player.paused
	player.mu.Unlock()

	if current == nil {
		k.respondError(ic, "Ништа тренутно не свира.")
		return
	}

	embed := buildNowPlayingEmbed(current, loop)
	
	// Add queue info
	if queueLen > 0 {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "У реду",
			Value:  fmt.Sprintf("%d песама", queueLen),
			Inline: true,
		})
	}
	
	// Add pause state
	if paused {
		embed.Color = 0xFFA500 // Orange for paused
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Статус",
			Value:  "⏸️ Паузирано",
			Inline: true,
		})
	}

	// Add control buttons
	loopLabel := "Укључи понављање"
	loopStyle := discordgo.SecondaryButton
	if loop {
		loopLabel = "Искључи понављање"
		loopStyle = discordgo.SuccessButton
	}

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Пауза",
					Style:    discordgo.SecondaryButton,
					CustomID: "pause_button",
					Emoji: discordgo.ComponentEmoji{
						Name: "⏸️",
					},
				},
				discordgo.Button{
					Label:    "Заустави",
					Style:    discordgo.DangerButton,
					CustomID: "stop_button",
					Emoji: discordgo.ComponentEmoji{
						Name: "⏹️",
					},
				},
				discordgo.Button{
					Label:    "Прескочи",
					Style:    discordgo.PrimaryButton,
					CustomID: "skip_button",
					Emoji: discordgo.ComponentEmoji{
						Name: "⏭️",
					},
				},
				discordgo.Button{
					Label:    loopLabel,
					Style:    loopStyle,
					CustomID: "loop_button",
					Emoji: discordgo.ComponentEmoji{
						Name: "🔁",
					},
				},
			},
		},
	}

	_ = k.session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds:     []*discordgo.MessageEmbed{embed},
			Components: components,
		},
	})
}

func (k *Kvazar) handlePause(ic *discordgo.InteractionCreate) {
	player := k.findPlayer(ic.GuildID)
	if player == nil {
		k.respondError(ic, "Ништа тренутно не свира.")
		return
	}

	paused := player.Pause()
	if paused {
		k.respondSuccess(ic, "⏸️ Репродукција паузирана.")
	} else {
		k.respondSuccess(ic, "▶️ Репродукција настављена.")
	}
}

func (k *Kvazar) handleStop(ic *discordgo.InteractionCreate) {
	player := k.findPlayer(ic.GuildID)
	if player == nil {
		k.respondError(ic, "Ништа тренутно не свира.")
		return
	}

	if !player.Stop() {
		k.respondError(ic, "Нема ничега за заустављање.")
		return
	}

	k.respondSuccess(ic, "⏹️ Репродукција заустављена и ред испражњен.")
}

func (k *Kvazar) handleLoop(ic *discordgo.InteractionCreate) {
	player := k.findPlayer(ic.GuildID)
	if player == nil {
		k.respondError(ic, "Ништа не свира да би се понављало.")
		return
	}
	
	var explicit *bool
	if len(ic.ApplicationCommandData().Options) > 0 {
		value := ic.ApplicationCommandData().Options[0].BoolValue()
		explicit = &value
	}

	state := player.ToggleLoop(explicit)
	if state {
		k.respondSuccess(ic, "Понављање је укључено.")
	} else {
		k.respondSuccess(ic, "Понављање је искључено.")
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
    
    // Add buttons for skip and loop
    loopLabel := "Понови"
    loopStyle := discordgo.SecondaryButton
    if loop {
        loopLabel = "Искључи понављање"
        loopStyle = discordgo.SuccessButton
    }
    
    components := []discordgo.MessageComponent{
        discordgo.ActionsRow{
            Components: []discordgo.MessageComponent{
                discordgo.Button{
                    Label:    "Пауза",
                    Style:    discordgo.SecondaryButton,
                    CustomID: "pause_button",
                    Emoji: discordgo.ComponentEmoji{
                        Name: "⏸️",
                    },
                },
                discordgo.Button{
                    Label:    "Заустави",
                    Style:    discordgo.DangerButton,
                    CustomID: "stop_button",
                    Emoji: discordgo.ComponentEmoji{
                        Name: "⏹️",
                    },
                },
                discordgo.Button{
                    Label:    "Прескочи",
                    Style:    discordgo.PrimaryButton,
                    CustomID: "skip_button",
                    Emoji: discordgo.ComponentEmoji{
                        Name: "⏭️",
                    },
                },
                discordgo.Button{
                    Label:    loopLabel,
                    Style:    loopStyle,
                    CustomID: "loop_button",
                    Emoji: discordgo.ComponentEmoji{
                        Name: "🔁",
                    },
                },
            },
        },
    }
    
    if _, err := k.session.ChannelMessageSendComplex(track.RequestChannelID, &discordgo.MessageSend{
        Embeds:     []*discordgo.MessageEmbed{embed},
        Components: components,
    }); err != nil {
        log.Printf("failed to send now playing message: %v", err)
    }
}

func (k *Kvazar) handleButtonClick(ic *discordgo.InteractionCreate) {
    customID := ic.MessageComponentData().CustomID
    
    player := k.findPlayer(ic.GuildID)
    if player == nil {
        _ = k.session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
            Type: discordgo.InteractionResponseChannelMessageWithSource,
            Data: &discordgo.InteractionResponseData{
                Content: "Ништа тренутно не свира.",
                Flags:   discordgo.MessageFlagsEphemeral,
            },
        })
        return
    }
    
    switch customID {
    case "pause_button":
        _ = k.session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
            Type: discordgo.InteractionResponseDeferredMessageUpdate,
        })
        paused := player.Pause()
        message := "⏸️ Репродукција паузирана."
        if !paused {
            message = "▶️ Репродукција настављена."
        }
        _, _ = k.session.FollowupMessageCreate(ic.Interaction, true, &discordgo.WebhookParams{
            Content: message,
            Flags:   discordgo.MessageFlagsEphemeral,
        })
    case "stop_button":
        _ = k.session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
            Type: discordgo.InteractionResponseDeferredMessageUpdate,
        })
        if player.Stop() {
            _, _ = k.session.FollowupMessageCreate(ic.Interaction, true, &discordgo.WebhookParams{
                Content: "⏹️ Репродукција заустављена и ред испражњен.",
                Flags:   discordgo.MessageFlagsEphemeral,
            })
        } else {
            _, _ = k.session.FollowupMessageCreate(ic.Interaction, true, &discordgo.WebhookParams{
                Content: "Нема ничега за заустављање.",
                Flags:   discordgo.MessageFlagsEphemeral,
            })
        }
    case "skip_button":
        _ = k.session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
            Type: discordgo.InteractionResponseDeferredMessageUpdate,
        })
        if player.Skip() {
            _, _ = k.session.FollowupMessageCreate(ic.Interaction, true, &discordgo.WebhookParams{
                Content: "⏭️ Прескочена је тренутна песма.",
                Flags:   discordgo.MessageFlagsEphemeral,
            })
        } else {
            _, _ = k.session.FollowupMessageCreate(ic.Interaction, true, &discordgo.WebhookParams{
                Content: "Нема активне песме за прескакање.",
                Flags:   discordgo.MessageFlagsEphemeral,
            })
        }
    case "loop_button":
        _ = k.session.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
            Type: discordgo.InteractionResponseDeferredMessageUpdate,
        })
        state := player.ToggleLoop(nil)
        emoji := "🔁"
        message := "Понављање је укључено."
        if !state {
            message = "Понављање је искључено."
        }
        _, _ = k.session.FollowupMessageCreate(ic.Interaction, true, &discordgo.WebhookParams{
            Content: emoji + " " + message,
            Flags:   discordgo.MessageFlagsEphemeral,
        })
    }
}

func pickOrDefault(value, fallback string) string {
    if strings.TrimSpace(value) == "" {
        return fallback
    }
    return value
}

func buildQueuedEmbed(track *media.Track, position int) *discordgo.MessageEmbed {
    title := fmt.Sprintf("У реду • %s", track.Title)
    if position == 1 {
        title = fmt.Sprintf("Следеће • %s", track.Title)
    }

    fields := []*discordgo.MessageEmbedField{
        {Name: "Трајање", Value: track.HumanDuration(), Inline: true},
        {Name: "Извор", Value: string(track.Source), Inline: true},
    }
    if track.RequestedBy != "" {
        fields = append(fields, &discordgo.MessageEmbedField{Name: "Захтевао", Value: track.RequestedBy, Inline: true})
    }
    fields = append(fields, &discordgo.MessageEmbedField{Name: "Позиција", Value: fmt.Sprintf("#%d", position), Inline: true})

	return &discordgo.MessageEmbed{
		Title:     title,
		URL:       track.WebURL,
		Color:     0x5865F2,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL: track.Thumbnail,
		},
		Fields: fields,
	}
}

func buildNowPlayingEmbed(track *media.Track, loop bool) *discordgo.MessageEmbed {
    status := "Сада"
    if loop {
        status = "Понавља"
    }

	return &discordgo.MessageEmbed{
		Title:     fmt.Sprintf("%s • %s", status, track.Title),
		URL:       track.WebURL,
		Color:     0x1ABC9C,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Thumbnail: &discordgo.MessageEmbedThumbnail{URL: track.Thumbnail},
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Трајање", Value: track.HumanDuration(), Inline: true},
			{Name: "Извор", Value: string(track.Source), Inline: true},
		},
	}
}

func stringPtr(value string) *string {
    return &value
}
