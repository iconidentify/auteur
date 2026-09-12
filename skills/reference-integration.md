---
name: reference-integration
description: The decision framework for weaving reference imagery into a generated music video: classifying references, choosing generation modes, and keeping the film coherent
agents: curator, director, cinematographer
---

# Reference integration

Reference material is a contract with the client. If a producer uploads an
image, they expect to feel it in the film. Your job is to determine what each
reference is FOR, then choose the cheapest mechanism that honors it.

## Step 1: Classify each reference

Study the image with analyze_image and assign one primary role:

- SUBJECT-ANCHOR: a person, character, product, logo, pet, or object that
  must literally and recognizably appear. The audience would notice its
  absence. Highest fidelity requirement.
- STYLE-GUIDE: the reference defines a look: palette, grain, lighting,
  lens character, art style, era. Its content is incidental; its treatment
  is the point.
- WORLD-BUILDING: locations, architecture, props, wardrobe that establish
  the film's setting. Should be recognizable in kind, not necessarily in
  detail.
- MOOD-ONLY: the reference communicates a feeling. It influences adjectives
  in prompts and nothing else.

An image can carry a secondary role (a portrait can anchor a subject AND
define a lighting style). Name both, but engineer for the primary.

## Step 2: Choose the mechanism per shot

Fidelity mechanisms, strongest to weakest:

1. reference mode (reference-to-video): attach the reference file(s) to the
   generation. Use for every shot where a SUBJECT-ANCHOR appears. Costs:
   capped at 720p; at most a few references per shot: spend them on the
   anchor, not on style images.
2. image mode via curated keyframe: generate_image a keyframe that fuses
   the reference's look with the shot's composition, verify it with
   analyze_image, then animate it (image-to-video). Use for: exact
   composition control, style-guide shots that must be precise, and
   continuity chains (extract the last frame of a rendered clip, animate
   onward).
3. Prompt language only: distilled descriptive phrases carried in every
   prompt. This is the default mechanism for STYLE-GUIDE, WORLD-BUILDING,
   and MOOD-ONLY references, and the coherence glue for shots where no
   file is attached.

Decision rules:
- Never attach a style-guide image as a reference to a shot with a
  subject-anchor: it dilutes subject fidelity. Translate style to words.
- A subject-anchor should appear in enough shots to feel like the film's
  protagonist, but not every shot: cutaways and environment shots relieve
  repetition and hide model drift.
- If two anchors must share a frame, attach both references to that shot
  and keep the prompt simple: complex action plus multiple anchors is where
  generation fails most.

## Step 3: Engineer the prompt language

For every reference, write a reusable phrase block that captures its
essence in words a diffusion model responds to: concrete nouns, materials,
light, color values. Example: "muted teal-and-rust palette, overcast
Scandinavian light, 16mm grain, shallow focus" beats "the vibe of image 2".
These phrases go in EVERY shot prompt for the sections the reference rules,
whether or not the file is attached: this is what makes the film feel like
one work instead of a playlist of unrelated clips.

## Large reference libraries (dozens of images)

Past roughly ten references, per-image treatment stops scaling. Batch your
vision calls (analyze_image takes several paths per call), then cluster the
library into named theme groups: protagonists/anchors, props, locations,
wardrobe, palette/grade, texture/mood. The integration plan is then written
per group: one role, one mechanism, one prompt-language block per group,
with only the standout individual images (anchors, the opening frame)
called out by filename. Not every image must appear in the film; coverage
comes from groups, and the producer's prompt decides which groups matter
most.

## The opening frame

When the producer designates an opening frame, it outranks every other
reference. The first shot is generated image-to-video from that exact
file: composition, subject, palette, and light all come from it, and the
prompt for shot one describes only motion and camera. Derive the film's
GLOBAL grade from the opening frame and reconcile all other references to
it. Echoing its composition or palette in the final shot closes the film's
loop and is usually worth a shot slot.

## Step 4: Verify and enforce

After clips render, check anchors with analyze_image against the original
reference: same person? same product? If fidelity drifted, retake with the
reference re-attached and the prompt simplified (fewer style words, plainer
action). Style coherence is judged across thumbnails side by side: when one
clip breaks palette, regrade the prompt and retake; do not ship the outlier.
