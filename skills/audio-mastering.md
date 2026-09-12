---
name: audio-mastering
description: When and how to master the final audio: loudness targets, when the source needs treatment beyond loudnorm, and verification
agents: editor
---

# Audio mastering

The music track IS the film's soundtrack; mastering protects it. The goal
is a final file that plays at competitive loudness on any platform without
clipping or pumping.

## Standard path

master_audio runs two-pass EBU R128 loudnorm and re-muxes the untouched
video stream. Targets:

- -14 LUFS integrated: the streaming standard (YouTube, Spotify-adjacent
  platforms normalize to about this). Default; use unless briefed otherwise.
- -16 LUFS: podcasts/voice-forward content.
- -9 to -11 LUFS: club/trailer loudness; only when the producer's brief
  asks for aggressive loudness, and check for audible pumping afterward.

True peak is capped at -1.0 dBTP by the tool: safe for lossy encoding.

## When loudnorm is not enough

Diagnose the source before mastering (bash + ffmpeg are available):

    ffmpeg -i src.mp3 -af astats -f null -        # peak/RMS stats
    ffmpeg -i src.mp3 -af ebur128 -f null -       # loudness history

- Source clips (true peak already at 0 dBFS with distortion): nothing
  restores it; note it in your report.
- Huge loudness range (quiet verse, loud drop) causing the normalized
  master to feel weak: add gentle glue before loudnorm:
  acompressor=threshold=-18dB:ratio=2:attack=20:release=250
- DC offset or rumble: highpass=f=20 first.
- If the track needs a custom chain, build it with bash/ffmpeg into a
  treated intermediate, then master_audio that intermediate.

## Verify

After mastering, confirm with:

    ffmpeg -i final/master.mp4 -af ebur128 -f null -

The summary's integrated loudness should be within 0.5 LU of target and
true peak at or below -1.0 dBTP. State both numbers in your report.
