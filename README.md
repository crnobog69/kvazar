# Kvazar

Kvazar is a lightweight Discord music bot written in Go. It focuses on fast response times, a minimal player aesthetic, and first-class support for YouTube and SoundCloud audio sources.

## Features

- Slash command based control (`/play`, `/skip`, `/loop`)
- YouTube and SoundCloud playback with search support via [`yt-dlp`](https://github.com/yt-dlp/yt-dlp)
- Elegant now-playing embeds with loop status indicators
- Guild-isolated queues with seamless loop and skip handling
- Automatic voice channel disconnect after inactivity to stay resource-light

## Requirements

Kvazar shells out to a couple of battle-tested tools for media processing:

- [`ffmpeg`](https://ffmpeg.org/) available in your PATH (or specify via `KVZ_FFMPEG_PATH`)
- [`yt-dlp`](https://github.com/yt-dlp/yt-dlp) available in your PATH (or specify via `KVZ_YTDLP_PATH`)
- Go 1.21 or newer

Ensure these binaries are installed on the host that runs the bot.

## Configuration

Kvazar is configured using environment variables:

| Variable              | Description                                                    |
| --------------------- | -------------------------------------------------------------- |
| `KVZ_DISCORD_TOKEN`   | Discord bot token (falls back to `DISCORD_TOKEN`)              |
| `KVZ_FFMPEG_PATH`     | Optional explicit path to the `ffmpeg` binary                  |
| `KVZ_YTDLP_PATH`      | Optional explicit path to the `yt-dlp` binary                  |
| `KVZ_STATUS`          | Optional custom status shown as "Listening to ..."            |

## Slash Commands

| Command  | Arguments           | Description                                                                 |
| -------- | ------------------- | --------------------------------------------------------------------------- |
| `/play`  | `query` *(string)*  | Plays a YouTube/SoundCloud URL or searches (`sc <query>` prefers SoundCloud) |
| `/skip`  | —                   | Skips the current track                                                     |
| `/loop`  | `enabled` *(bool)*  | Toggle loop (omit to toggle, provide to set explicitly)                     |

When `/play` resolves a track successfully, Kvazar will queue it, inform the requester privately, and broadcast a minimalist "Now Playing" card to the invoking channel when playback starts.

## Running locally

1. Export your token and ensure dependencies are installed:

   ```bash
   export KVZ_DISCORD_TOKEN=your_token_here
   which ffmpeg yt-dlp
   ```

2. Start the bot:

   ```bash
   go run ./cmd/kvazar
   ```

3. Invite the bot to your server with the `applications.commands` and `bot` scopes, granting it permission to connect and speak in voice channels.

Kvazar listens for `SIGINT`/`SIGTERM` and will gracefully close the Discord session on shutdown, cleaning up registered slash commands.

## Deployment notes

- Registering the slash commands happens globally on startup; propagation can require up to an hour for brand new bots. For development, consider configuring a test guild and adapting the command registration accordingly.
- The bot keeps each guild player isolated. Resources are reclaimed automatically after 90 seconds of inactivity in a voice channel.
- Playback relies on streaming audio directly via `ffmpeg`; thus a stable network connection from the host to YouTube/SoundCloud CDNs is recommended for smooth playback.

Enjoy the cosmic vibes with Kvazar! 🌌🎶
