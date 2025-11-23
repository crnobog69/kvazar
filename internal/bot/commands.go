package bot

import "github.com/bwmarrin/discordgo"

const (
	commandPlay   = "play"
	commandPlayer = "player"
	commandPause  = "pause"
	commandStop   = "stop"
	commandSkip   = "skip"
	commandLoop   = "loop"
)

var globalCommands = []*discordgo.ApplicationCommand{
	{
		Name:        commandPlay,
		Description: "Пусти музику са YouTube-а или SoundCloud-а, или претражи.",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "query",
				Description: "URL адреса или упит за претрагу (префикс 'sc' за SoundCloud)",
				Required:    true,
			},
		},
	},
	{
		Name:        commandPlayer,
		Description: "Прикажи тренутно стање плејера.",
	},
	{
		Name:        commandPause,
		Description: "Паузирај или настави репродукцију.",
	},
	{
		Name:        commandStop,
		Description: "Заустави репродукцију и испразни ред.",
	},
	{
		Name:        commandSkip,
		Description: "Прескочи тренутну песму.",
	},
	{
		Name:        commandLoop,
		Description: "Промени понављање реда.",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionBoolean,
				Name:        "enabled",
				Description: "Експлицитно постави понављање реда (изостави за промену).",
				Required:    false,
			},
		},
	},
}
