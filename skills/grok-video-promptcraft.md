---
name: grok-video-promptcraft
description: How to write Grok Imagine video prompts that render as intended: structure, camera language, what fails, and mode-specific technique
agents: cinematographer, director
---

# Grok Imagine promptcraft

A video prompt is a shot description a cinematographer would give: one
subject, one action, one camera idea, one lighting/style treatment. The
model renders what is concrete and drops what is vague.

## Prompt structure

Order matters. Build each prompt as:

1. SUBJECT: who/what, with the identifying details that matter.
   "a woman in a silver raincoat" not "someone".
2. ACTION: one clear, continuous motion that fits the duration.
   A 6-second clip holds ONE beat of action: "she turns toward the neon
   sign as rain intensifies", not a three-part sequence.
3. CAMERA: name the shot and the move: "slow dolly-in from a low angle",
   "static wide shot", "handheld tracking shot following behind",
   "aerial pullback". One move per shot. Uncommitted camera language
   yields random drifting.
4. SETTING and LIGHT: place and hour: "empty parking garage, flickering
   sodium lights", "golden hour rooftop".
5. STYLE/GRADE: the film treatment: "35mm film, shallow depth of field,
   muted teal-and-rust palette, cinematic". Keep the same style block
   across the whole production for coherence.

## What fails and how to avoid it

- Multiple sequential actions ("walks in, sits down, then looks up"):
  the model averages them. One action per clip; sequence via editing.
- Crowds of specified characters: fidelity collapses past 2 subjects.
- Text on screen: signs and lettering render as glyph soup. Avoid asking.
- Precise counts and spatial layouts ("four cars parked in a diamond"):
  approximate at best.
- Negations ("no people in the street"): often ignored; describe what IS
  there ("a deserted street").
- Fast complex motion (fights, sports plays): expect artifacts; prefer
  the moment before/after the action, slow motion, or impressionistic
  treatment.

## Mode technique

- text mode: the prompt carries everything; be fully explicit including
  style block.
- image mode: the frame defines composition, subject, and grade; the
  prompt should describe MOTION ONLY plus camera: "slow push-in as she
  exhales, hair moving in wind". Repeating the visual description fights
  the image.
- reference mode: describe the scene normally and refer to subjects
  plainly ("the woman from the reference stands..."). Keep action simple:
  reference fidelity degrades with complex motion. Resolution caps at 720p.

## Duration and pacing

Ask for the duration the edit needs plus 2-3 seconds of trim room, within
the 1-15s cap. First and last seconds of a generation are often the
weakest (settling in / drifting out); the editor trims into the strong
middle. For a fast-cut section, many short varied clips beat one long
clip: variety is what montage needs.

## Retakes

A retake is a prompt fix, not a reroll. Diagnose first with analyze_image:
wrong subject → strengthen subject clause or switch to reference mode;
wrong look → tighten the style block; broken motion → simplify the action;
dead composition → change the camera clause. If two prompt-fix attempts
fail, change approach: generate a keyframe and animate it.
