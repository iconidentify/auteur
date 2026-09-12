#!/usr/bin/env python3
"""Transcribe a music track's vocals with word-level timing.

Used by auteur's transcribe_lyrics tool. Emits JSON on stdout:
  {"language": "en", "segments": [{"start": 1.2, "end": 4.7, "text": "..."}]}

CPU-only on purpose: the GPUs belong to the render pipeline. The 'small'
model transcribes a 3-minute track in roughly a minute and handles rap
cadence acceptably; override with AUTEUR_WHISPER_MODEL.
"""
import json
import os
import sys


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: transcribe.py <audio>", file=sys.stderr)
        return 2
    from faster_whisper import WhisperModel

    model = WhisperModel(
        os.environ.get("AUTEUR_WHISPER_MODEL", "small"),
        device="cpu",
        compute_type="int8",
    )
    segments, info = model.transcribe(
        sys.argv[1],
        vad_filter=True,          # skip instrumental stretches
        word_timestamps=False,
        beam_size=5,
    )
    out = []
    for seg in segments:
        text = seg.text.strip()
        if text:
            out.append({"start": round(seg.start, 2), "end": round(seg.end, 2), "text": text})
    json.dump({"language": info.language, "segments": out}, sys.stdout)
    return 0


if __name__ == "__main__":
    sys.exit(main())
