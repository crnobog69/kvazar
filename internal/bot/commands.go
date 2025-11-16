package bot

import "github.com/bwmarrin/discordgo"

const (
	commandPlay = "play"
	commandSkip = "skip"
	commandLoop = "loop"
)

var globalCommands = []*discordgo.ApplicationCommand{
	{
		Name:        commandPlay,
		Description: "Play music from YouTube or SoundCloud, or search by query.",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "query",
				Description: "URL or search query (prefix with 'sc' to prefer SoundCloud)",
				Required:    true,
			},
		},
	},
	{
		Name:        commandSkip,
		Description: "Skip the currently playing track.",
	},
	{
		Name:        commandLoop,
		Description: "Toggle loop for the current track or explicitly set the state.",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionBoolean,
				Name:        "enabled",
				Description: "Explicitly set loop to on or off (omit to toggle).",
				Required:    false,
			},
		},
	},
}
