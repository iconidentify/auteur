---
name: h3-video-promptcraft
description: How to shoot on MiniMax H3 locally: frame-chaining for continuity, prompt structure, the render-time budget, take discipline, and how it differs from Grok Imagine
agents: cinematographer, director, curator
---

# MiniMax H3 promptcraft

H3 is a 33B omni model: one denoise pass produces the picture and a native
32 kHz stereo track together. It runs on this machine's two RTX 3090s, split
across both cards. It is local, sequential, and paid for in minutes — and it
is a first/last-frame model, which changes how continuity is made.

## Reference mode: putting exact content into the world

Mode `reference` (the REF2VA variant) takes up to 9 reference images that
ride through the Qwen3-VL text encoder with your prompt. Bind each one by
index, in `reference_paths` order:

    reference_paths: [screenshot.png, logo.png]
    prompt: The operator leans toward the terminal. The monitor displays
    <Picture 1>, glowing green, readable. <Picture 2> defines the logo
    printed on the machine's badge. Slow push-in, fluorescent office light.

This is how a screenshot becomes what's ON a screen in the shot — rendered
with true perspective, glow, and reflections, because the model understands
it as content, not as a sticker. It is identity-faithful, not pixel-exact:
fine text survives no better than it ever does at the render resolution.

The price and the boundaries:

- ~2.5x a normal render (20 sampling steps; the turbo LoRAs are FL2V-only).
- No `image_path`/`end_image_path` — reference mode cannot chain. Use it to
  ESTABLISH a shot containing exact content, then harvest its last frame and
  chain onward in normal image mode.
- Not a substitute for cropped-pixel inserts of things already in footage,
  nor for chaining. Reach for it when the content exists nowhere in the
  film yet and must be exact: screen contents, logos, products, a person.

## Batch by mode: model swaps cost real time

Modes text and image share the FL2VA checkpoint; mode reference loads the
REF2VA checkpoint instead. Flipping between them evicts and reloads a 21GB
model — with this box's RAM the swap comes partly from disk, a minute or
more each flip. So SCHEDULE THE SHOOT BY MODE: shoot every reference shot
as one consecutive batch, and every text/image shot as another. Never
alternate modes shot-by-shot; that pays the swap tax on every clip.

## Lip sync: performing the track

Reference mode also takes audio (sync_audio_path + sync_audio_from_seconds):
the model generates the performance against that exact slice of sound —
mouth, breath, head rhythm. This is how a rap or vocal shot lands on the
bars.

- Pick the segment from analysis/lyrics.json: sync_audio_from_seconds is the
  line's start; the slice length is the clip's duration, so choose a
  duration that covers whole lines (never cut a bar in half).
- Prompt the PERFORMANCE, not the words: "he raps directly to camera,
  precise mouth movement, head nodding on the beat". Do not paste lyrics
  into the prompt — the audio carries the words.
- Use it only where the mouth reads (MCU or closer). A wide shot wastes the
  2.5x reference-mode cost on lips nobody can see.
- The editor must place a synced clip at exactly sync_audio_from_seconds on
  the timeline, in_seconds 0. One second of drift reads as a dub.

## Continuity comes from frames, not descriptions

This is the core of shooting on H3. When a shot has to match existing
imagery — the opening frame, a reference, the previous shot — you do not
describe the match, you hand H3 the actual frame:

- `mode: image` + `image_path` animates forward from a still. The frame fixes
  composition, subject, palette, and grade; the prompt describes MOTION AND
  CAMERA ONLY. Repeating the visual description fights the image.
- Adding `end_image_path` interpolates between two stills: the prompt
  describes the transit ("she turns to face the window as the light shifts").
- `extract_frame` harvests any moment of rendered footage as a still. That
  still seeds the next shot, and the two clips cut together invisibly.

This makes whole scenes buildable from one real image. The room after the
character leaves is not a new still to generate — it is the last frame of the
shot in which they leave. Render that shot, harvest its final frame, and
every later shot of the empty room inherits the true geometry.

Never try to recreate an existing image with generate_image. Text-to-image
cannot see the target; each take re-rolls the composition, and no number of
takes converges on a match. And an INSERT IS NOT A NEW COMPOSITION: a
close-up of the keyboard, the screen, the hands — any detail of something
already on screen — must come from real pixels, because a generated version
will be a DIFFERENT keyboard on a DIFFERENT desk and the audience sees it
instantly. Crop the detail from the plate or a harvested frame and animate
the crop:

    ffmpeg -i plate.png -vf "crop=W:H:X:Y,scale=864:480" insert_seed.png

Cropped pixels are softer than a generated still; that is the honest price.
If the crop is too soft to hold a shot, the film does not have that insert —
cut around it. generate_image is ONLY for imagery of things that exist
nowhere yet: a new location, an object never shown, an abstract texture. The
take budget (three per still, enforced) exists because the matching loop is
tempting and always a dead end.

## The budget is time, and quality is what it buys

At 720p (1344x768) a clip costs roughly 110 seconds of render per second of
video. At 480p (864x480) roughly 36. Clips render one at a time; a submitted
batch queues, it does not parallelize.

Shoot at 480p — it is the studio default. 720p exists for productions whose
brief explicitly asks for it (about 3x the render time, and noticeably better
fine detail such as on-screen text). Decide the shot list before you render:
a 15-second film is ~10 minutes of GPU shot once at 480p. Every clip result
reports the run's cumulative GPU minutes — read it.

## Prompt structure

Same shape as any shot description: one subject, one action, one camera idea,
one lighting/style treatment. Concrete beats evocative; the model renders
what is specific and ignores what is vague.

    A woman in a red coat walks away down a wet alley, neon signs above her.
    Slow dolly follow at chest height. Night, cyan and magenta reflections in
    the puddles, shallow depth of field, 35mm anamorphic.

Because H3 generates sound from the same prompt, a short concrete sound cue
("rain on metal, distant traffic") can sharpen the physicality of the motion.
Keep it to a clause: the generated audio is discarded — the music track is
the film's only sound — so never let sound description crowd out the visual
specificity that earns the shot.

## Pin the action's duration

An unpinned action resolves as an arc: H3 gives it a beginning, middle, and
end inside the clip, whatever its length. "He types" becomes he types, then
sits back, then rests — and if the edit needed him typing at 14s, the take is
dead. When the action must persist, say so in the prompt in words of
duration: "he types continuously for the entire clip, hands never leaving the
keys". Likewise pin the camera when it must not move: "camera absolutely
locked, rigid tripod, zero push or drift" — "slow push" language, or no
camera language at all, licenses movement.

## On-screen text will not survive

Two separate failures, and retakes fix neither:

1. The video VAE reconstructs every frame, and at 480p-class it destroys
   small text EVEN IN THE FIRST FRAME — a crisp "Chonkbase" in the source
   still comes back "Chonbbase" before any motion happens. 720p preserves
   noticeably more.
2. Motion then morphs what survives; after 3-4 seconds, on-screen text is
   soup regardless of resolution.

When text must read (a brand, a terminal, a sign), do not spend takes on it —
fix it in post. The proven pipeline (all ffmpeg, editor's bash):

1. **Expect camera drift even when the prompt mandates a lock.** H3 cannot
   hold a hard tripod over long clips; prompting "absolutely locked" reduces
   but does not remove drift. Remove the rest with the virtual tripod:

       ffmpeg -i take.mp4 -vf "vidstabdetect=tripod=1:result=t.trf" -f null -
       ffmpeg -i take.mp4 -vf "vidstabtransform=tripod=1:input=t.trf:interpol=bicubic:crop=keep" stab.mp4

   Residual drift of a few pixels remains (a moving subject biases the
   estimator); the patch below must over-cover for it.
2. **Read patch coordinates from the VIDEO frame, not the source still.**
   Generation recomposes: the screen quad in the render will NOT sit where it
   sits in the still. Extract frame 0, overlay a grid
   (`drawgrid=w=32:h=32:t=1:c=red@0.6`) on both the frame and the
   video-sized still, and read both regions off with vision.
3. **Patch the wordmark, not the whole screen.** The full screen quad differs
   in shape and tilt between still and render — a full-screen plate needs
   corner-pin warping and reads wrong. A small patch over the brand/headline
   on dark glass is forgiving: seams vanish, and residual drift hides. Crop
   the wordmark from the video-sized still WITH generous dark-glass margin
   (so leftover glyphs beneath cannot peek out), scale ~1.2x to the video's
   wordmark box, overlay:

       ffmpeg -i still_scaled.png -vf "crop=W:H:X:Y" patch.png
       ffmpeg -i stab.mp4 -i patch.png -filter_complex "[0:v][1:v]overlay=VX:VY" -c:a copy fixed.mp4

4. **Verify with review_clip** on the fixed output: wordmark legible and
   seam invisible at every sampled frame, early and late.

Body text stays soup and that is fine — unreadable CRT rows read as texture;
the brand is what the audience reads.

## Duration

2-15s per clip, snapped up to the model's frame grid: 3s=73, 5s=124, 10s=243,
15s=362 frames at 24 fps. Ask for what the edit needs plus a second or two of
trim room. The opening and closing moments of a generation are the weakest —
motion settles in and drifts out — so the editor cuts into the strong middle.
5s is the workhorse; go to 10s when a shot has to breathe.

## Cutting a chained film

Chained shots (each seeded by the previous one's last frame) join invisibly
ONLY at the exact shared frame: the outgoing clip plays to its final frame,
the incoming starts at 0.0. Trim either side and the subject jumps — a
visible pop at every join. So:

- Treat a chained sequence as ONE long shot. Its internal joins are not
  cuts, they need no beat alignment, and the editor must not trim them.
  Beat-align only the REAL cuts (inserts, size changes, time-jumps).
- Seed only from the outgoing shot's END. Seeding a later shot from a frame
  the audience already watched (a mid-frame of an earlier clip) rewinds
  time and resets the camera on screen — the "abrupt zoom back" artifact.
- Never end a chain by morphing back to an already-shown framing. If the
  film wants a bookend return to the opening, cut to it hard from a
  contrasting size, or dissolve — do not interpolate toward it.
- A visible cut needs CONTRAST to read as intentional: change shot size by
  at least two steps (wide to close, not wide to slightly-tighter-wide).
  Two same-size shots of the same subject cut together read as a mistake.
- Dissolves (stitch_timeline transition_seconds, ~0.5-1.0s) are for
  time-jumps and mood shifts, and they also soften a join that cannot be
  frame-exact. Use sparingly; a music film is mostly hard cuts on the beat.

## Short tracks want few shots

Shot count scales with the music. Under ~20 seconds, the default answer is
ONE sustained take — two at most — and every cut must earn its place with a
reason the hold cannot provide. A cut to a regenerated location reads as a
different place at this length; there is no time to re-establish.

## Take discipline

The tools enforce budgets: three takes per still, three per shot. This is not
an obstacle, it is the craft — a professional judges a take on whether it is
USABLE, not whether it is perfect, and spends the schedule on the film.

Diagnose before re-rendering, with analyze_image on the frames:

- Wrong subject or lost continuity → you needed a real frame, not a prompt.
  Chain from the source image (animate + extract_frame) and reshoot once.
- Right subject, wrong look → tighten the style and lighting clause and reuse
  the reported seed, so only what you changed changes.
- Broken or soupy motion → simplify the action to one verb and slow the
  camera. H3 degrades on compound motion first.
- Dead composition → change the camera clause, or seed from a frame that has
  the framing you want.

If the budget for a shot is spent and nothing is usable, the shot concept is
wrong: redesign it under a new id — different seed frame, simpler motion —
and say so in your report. Never spend the last take on the same idea that
failed twice.
