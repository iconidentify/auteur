# Auteur

An agentic music-video studio. Hand it a music track, optional reference
imagery, and a producer's prompt; a director agent and its specialist crew
write the treatment, plan how the references enter the film, shoot clips
with Grok Imagine or MiniMax H3 (hosted or local ComfyUI), cut a
beat-aligned edit, master the audio, and deliver a finished music video.
The music is the source of the movie.

The control-room UI streams every agent's reasoning, decisions, tool calls,
and rendered artifacts live while the production runs.

## Requirements

- Go 1.25+
- ffmpeg and ffprobe on PATH
- An xAI API key in `.env` (`XAI_API_KEY=...`) or the environment
- For hosted MiniMax clips: `MINIMAX_API_KEY`
- For the local renderer only: a ComfyUI serving MiniMax H3 (see "Renderers")
- Optional, for narrated film productions: `ELEVENLABS_API_KEY`

## Run

    go build -o auteur ./cmd/auteur
    ./auteur

Open http://127.0.0.1:7788, drop a track, add references and a producer's
prompt, greenlight, then roll cameras.

Flags: `-addr` (default 127.0.0.1:7788), `-data` (workspace root, default
`workspaces`), `-skills` (default `skills`), `-model` (default `grok-4.6`,
also via `AUTEUR_MODEL`), `-video` (default `grok`, also via `AUTEUR_VIDEO`),
`-h3-url` (default http://127.0.0.1:8189, also via `AUTEUR_H3_URL`).

## Renderers

Clips come from one of three engines, chosen with `-video`. Everything else --
agent inference, `analyze_image`, and `generate_image` -- runs on the xAI API
either way, so `XAI_API_KEY` is always required.

    ./auteur                    # grok:     Grok Imagine, hosted
    ./auteur -video=minimax     # minimax:  MiniMax H3 on api.minimax.io
    ./auteur -video=h3          # h3:       MiniMax H3 on local ComfyUI

`h3` renders on a ComfyUI instance serving MiniMax H3 through Raylight, which
splits the sequence across the local GPUs (Ulysses) and sharded weights (FSDP);
the 33B model does not fit on one 24GB card. Auteur builds the API-format
workflow itself (`pkg/comfy`), so ComfyUI needs no saved workflow -- only the
weights, and the model list it advertises must contain the files named in
`pkg/comfy/h3.go`. The URL is checked at startup, so a renderer that is not up
fails immediately rather than mid-production.

The engine's rules reach the crew two ways: the `generate_video_clips` schema
and description are generated from the backend's `BackendCaps`, and `Crew.
engineBlock` adds a prompt section that overrides the Grok-specific role briefs.
The differences that matter for H3:

- No reference mode. A reference reaches the screen only as a `generate_image`
  keyframe animated with mode `image`.
- First/last-frame model: `image_path` animates forward, and adding
  `end_image_path` interpolates between two stills, which is how shots chain.
- Renders are sequential on local GPUs, roughly 36s of render per second of
  864x480 video, and about 3x that at 1344x768. Durations snap to the model's
  frame grid (`length % 17 == 5`).
- Clips arrive with model-generated audio, which the edit discards.

Check the renderer before committing a production to it:

    go run ./cmd/h3smoke -seconds 3 -out /tmp/smoke.mp4

## How a production runs

The director agent owns the film and delegates to four specialists, all
running the same harness with role-specific prompts, tools, and skills:

- screenwriter: analyzes the music (tempo, beat grid, energy, sections)
  and writes the treatment mapped to the track's structure.
- curator: studies each reference image with vision and writes the
  integration plan: whether a reference is a subject-anchor, style-guide,
  world-building, or mood, and concretely how it enters shots.
- cinematographer: writes shot prompts, picks generation modes
  (text / image-to-video from generated keyframes / reference-to-video),
  renders clips concurrently through Grok Imagine, reviews dailies with
  vision, and retakes weak shots.
- editor: builds a beat-aligned edit decision list, stitches the timeline
  with the music as the only audio, and masters loudness (EBU R128).

Agents share the production workspace as their common memory
(`workspaces/<id>/`): sources in `source/`, work documents in `analysis/`,
keyframes in `frames/`, rendered clips in `clips/`, deliverables in
`final/`. Every agent also has a local shell (`bash` tool) with ffmpeg
available.

Skills in `skills/*.md` are markdown playbooks loaded with progressive
disclosure: agents see one-line summaries in their prompt and read the full
text on demand. Edit them to tune the studio's craft without touching code.

## Architecture

    pkg/comfy        ComfyUI client (queue, poll, upload, fetch) and the
                     MiniMax H3 workflow builder
    pkg/harness      agent-loop library extracted from chonkbase pkg/agent
                     (Agent/Provider/EventSink contracts, RunLoop, tool
                     registry, skills, delegation, retries, streaming)
    pkg/xai          xAI API client: chat completions with tool calling and
                     streaming, Grok Imagine video and image generation
    pkg/minimax      hosted MiniMax-H3 video client (api.minimax.io)
    pkg/elevenlabs   optional voice engine for narrated film productions
    internal/media   pure-Go audio analysis (FFT onset detection, tempo,
                     beat grid, sections) plus ffmpeg stitch and mastering
    internal/agents  the crew: role prompts, toolbox, provider adapter, and
                     the video backends (Grok Imagine / hosted MiniMax / local H3)
    internal/studio  productions, workspaces, event log and live hub
    internal/server  REST API, SSE event stream, embedded control-room UI
    cmd/auteur       entry point
    cmd/h3smoke      one-clip smoke test for the local renderer
    skills/          the studio's playbooks

Events flow: harness RunLoop emits structured events (thinking deltas, tool
calls, artifacts) into a per-production hub that appends to
`events.jsonl` and fans out to SSE subscribers; the UI replays the log on
connect, so a reloaded browser reconstructs the full session.

## API

    POST /api/productions                multipart: title, prompt, music, references
    GET  /api/productions                list
    GET  /api/productions/{id}           state
    POST /api/productions/{id}/start     run the crew
    POST /api/productions/{id}/cancel    stop a running production
    GET  /api/productions/{id}/events    SSE stream (Last-Event-ID resume)
    GET  /api/productions/{id}/files/... serve workspace files

## Tests

    go test ./...

## License

Copyright (C) 2026 Chris Kearney

Auteur is free software: you can redistribute it and/or modify it under
the terms of the GNU General Public License as published by the Free
Software Foundation, version 3. See [LICENSE](LICENSE) (GPL-3.0-only).
