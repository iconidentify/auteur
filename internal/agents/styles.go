package agents

// AnimationStyle is one of the studio's sample looks for a narrated film. The
// Prompt is the style language baked into every image and clip prompt so the
// whole film — character sheets, location keyframes, every shot — reads as one
// consistent hand.
type AnimationStyle struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Blurb  string `json:"blurb"`
	Prompt string `json:"prompt"`
}

// AnimationStyles is the catalog offered in the intake form.
var AnimationStyles = []AnimationStyle{
	{
		ID:    "ghibli-watercolor",
		Name:  "Ghibli watercolor",
		Blurb: "Hand-painted, soft light, lush natural worlds, gentle expressive faces.",
		Prompt: "Studio Ghibli-inspired hand-painted 2D animation: soft watercolor backgrounds with " +
			"visible brush texture, warm diffused natural light, lush detailed environments, gentle " +
			"expressive character faces with clean linework, muted earthy palette with luminous " +
			"highlights, painterly clouds and foliage, quiet cinematic framing",
	},
	{
		ID:    "pixar-3d",
		Name:  "Pixar-style 3D",
		Blurb: "Polished CG, appealing stylized characters, rich lighting.",
		Prompt: "Pixar-style 3D computer animation: appealing stylized characters with expressive " +
			"large eyes and soft rounded forms, subsurface-scattered skin, physically-based rendering, " +
			"rich cinematic three-point lighting, shallow depth of field, vibrant saturated palette, " +
			"clean detailed environments, feature-film production quality",
	},
	{
		ID:    "disney-cel",
		Name:  "Classic Disney cel",
		Blurb: "Golden-age hand-drawn animation, ink lines and flat color, painted backgrounds.",
		Prompt: "classic golden-age Disney hand-drawn cel animation: confident ink outlines, flat " +
			"cel-shaded color with soft gradient shading, lavish painted multiplane backgrounds, " +
			"fluid graceful character motion, warm theatrical lighting, timeless storybook charm",
	},
	{
		ID:    "anime",
		Name:  "Anime",
		Blurb: "Japanese TV/film anime: sharp lines, dramatic lighting, expressive stills.",
		Prompt: "high-quality Japanese anime film style: sharp clean linework, cel shading with " +
			"dramatic rim lighting and lens flares, detailed atmospheric backgrounds, expressive " +
			"stylized faces, dynamic cinematic camera angles, vivid but harmonious palette, " +
			"Makoto Shinkai-level background detail",
	},
	{
		ID:    "graphic-novel",
		Name:  "Graphic novel",
		Blurb: "Bold ink, heavy shadows, limited palette, dramatic panel-like compositions.",
		Prompt: "graphic novel / comic-book illustration style: bold expressive ink outlines, heavy " +
			"chiaroscuro shadows, limited moody color palette with halftone texture, dramatic " +
			"high-contrast lighting, strong graphic compositions, gritty painterly finish",
	},
	{
		ID:    "storybook",
		Name:  "Storybook illustration",
		Blurb: "Children's picture-book warmth: gouache textures, friendly shapes, gentle palette.",
		Prompt: "children's picture-book illustration style: warm gouache and colored-pencil " +
			"textures, friendly rounded character shapes, soft gentle pastel-and-earth palette, " +
			"cozy hand-made paper feel, simple clear staging, tender and inviting mood",
	},
	{
		ID:    "claymation",
		Name:  "Claymation",
		Blurb: "Stop-motion clay: tactile, handmade, charming imperfection.",
		Prompt: "stop-motion claymation style: tactile sculpted clay characters with visible " +
			"fingerprint texture, handmade miniature sets, slightly staccato stop-motion movement, " +
			"warm practical studio lighting, shallow miniature depth of field, Aardman-like charm",
	},
	{
		ID:    "renaissance-oil",
		Name:  "Renaissance oil painting",
		Blurb: "Old-master canvas brought to life: chiaroscuro, rich glazes, sacred gravitas.",
		Prompt: "living Renaissance oil painting style: old-master chiaroscuro lighting, rich " +
			"layered glazes and visible canvas texture, deep umber shadows with golden highlights, " +
			"classical composition and drapery, solemn sacred gravitas, Caravaggio and Rembrandt " +
			"tonality, subtle painterly motion",
	},
	{
		ID:    "paper-cutout",
		Name:  "Paper cutout",
		Blurb: "Layered paper collage: flat shapes, drop shadows, playful depth.",
		Prompt: "layered paper-cutout collage animation style: flat cut-paper shapes with crisp " +
			"edges and soft drop shadows, visible paper grain and torn edges, stacked parallax " +
			"depth, bold simplified forms, warm craft-table palette, playful handmade charm",
	},
}

// StyleByID looks a style up; ok is false for an unknown id.
func StyleByID(id string) (AnimationStyle, bool) {
	for _, s := range AnimationStyles {
		if s.ID == id {
			return s, true
		}
	}
	return AnimationStyle{}, false
}
