---
name: beat-cut-editing
description: Building a beat-aligned edit decision list: cut placement math, pacing against energy, transition choices, and structural conventions of music video editing
agents: editor, director
---

# Beat-cut editing

A music video edit succeeds when cuts feel inevitable. That comes from
arithmetic plus taste: cut points live on the beat grid, cut RATE follows
the music's energy, and shot ORDER tells the story.

## The beat grid

From analysis/audio_analysis.json:

    beat(n) = first_beat_offset_seconds + n * beat_interval_seconds

Timeline cut points must land on beats. Clip durations are therefore whole
multiples of beat_interval (or of half/double bars). At 120 BPM
(interval 0.5s): a 4-beat shot = 2.0s, an 8-beat shot = 4.0s.

Practical method: pick the per-section cut length in BEATS, then convert:

- peak / drop sections: 2-4 beats per shot (1-2s at 120 BPM)
- steady / groove sections: 4-8 beats
- intro / outro / breakdown: 8-16 beats, or one held shot

Micro-timing: cutting 30-60ms BEFORE the beat reads as "on the beat" to the
eye (vision leads audition). If a cut feels late, pull it earlier by one
frame-ish increment (0.04s), never later.

## Building the EDL

1. Total timeline duration = the full track (or briefed span). Work
   section by section from the audio analysis timestamps.
2. Within each section: number of shots = section duration / chosen cut
   length. Assign clips: strongest imagery on structural landmarks (the
   drop, the first peak downbeat, the final chord).
3. Trim each clip with intent: in_seconds should catch the clip
   mid-motion (generated clips are weakest in their first second: trim in
   at least 0.5-1.0s). duration_seconds = the beat-multiple you assigned.
4. A clip may be used twice (different trims) if it is strong and the
   sections are far apart; never back-to-back.
5. Cut on motion when possible: exiting a clip while the subject moves
   hides the seam and adds propulsion.

## Transitions

- Hard cuts: 95% of music video editing. Default everywhere.
- Crossfade (0.3-0.8s): only for entering/leaving quiet sections
  (intro, breakdown, outro) where softness is the point.
- Never crossfade inside a peak section: it kills the punch.

Note stitch_timeline's crossfade applies globally between all clips; if
only some boundaries should fade, stitch peak sections and quiet sections
separately with matching settings, or accept hard cuts throughout: hard
cuts are the safe universal choice.

## Structure conventions

- Open with a establishing/mood shot; save the protagonist's clearest
  shot for the first chorus/drop.
- Repeat motif shots at parallel structural points (chorus 1 and chorus 2)
  to build the film's grammar.
- The last shot should resolve: longest hold of the edit, energy landing,
  music fading (audio_fade_out 2-3s).

## Verify

After stitching, probe the output duration against the plan. Extract frames
at 2-3 cut points and check the transitions land where intended. Listen
with fresh ears: if a section drags, halve its cut lengths and re-stitch.
Re-stitching is cheap; a limp edit is not.

## Cutting chained footage (frame-seeded shots)

When the cinematographer chained shots (shot N+1's first frame harvested
from shot N's end), those joins are continuations, not cuts. Rules:

- Join at the exact shared frame: outgoing clip runs to its final frame
  (no tail trim), incoming starts at in_seconds 0.0 (no head trim). Any
  trim at a chained join creates a visible jump.
- Do not beat-align chained joins; they are invisible. Spend the beat grid
  on the real cuts.
- If an incoming chained clip's head is unusable (settling artifacts), a
  frame-exact join is impossible: either request a retake or convert the
  join into a real cut by putting a contrasting insert between the shots.
- Real cuts want size contrast (two steps or more). A cut between two
  same-size framings of the same subject is a jump cut; fix the shot list
  rather than shipping it.
- transition_seconds on a clip dissolves into it from the previous one:
  0.5-1.0s for time-jumps or mood shifts. Never dissolve a chained join.

## Cutting to lyrics

When analysis/lyrics.json exists, the cut serves two grids at once: the beat
grid and the line grid. Rules:

- Cuts land on beats, but SHOT CHANGES land on line or bar boundaries —
  find the beat nearest the line's start. Cutting mid-line reads as a
  stumble unless the lyric itself breaks there.
- The hook gets a recurring shot or motif: same image family every time it
  returns. The audience learns the film's chorus like the song's.
- A signature line deserves its image on screen AS the line lands, a beat
  early rather than late — anticipation reads as intent, lag reads as error.
- Verses tolerate longer holds; rapid-fire bars tolerate faster cutting,
  but never cut faster than the words can be heard.

## Placing lip-synced clips

A clip whose render note says LIP-SYNCED carries a hard placement contract:
timeline position = its sync_audio_from_seconds, in_seconds = 0, full
duration. Never trim its head, never slide it to a nearer beat — the bars it
was generated against live at that exact spot in the master track. Cut the
shots around it to fit; the synced clip is the fixed point.
