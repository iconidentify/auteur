/* Auteur control room */
"use strict";

const ROLES = ["director", "screenwriter", "curator", "cinematographer", "editor"];
const ROLE_COLORS = {
  director: "var(--r-director)",
  screenwriter: "var(--r-screenwriter)",
  curator: "var(--r-curator)",
  cinematographer: "var(--r-cinematographer)",
  editor: "var(--r-editor)",
};

const $ = (id) => document.getElementById(id);
const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;
  return n;
};

const state = {
  prod: null,
  es: null,
  timer: null,
  poll: null,
  filterRole: null,
  tokens: new Map(),      // agentID -> latest cumulative tokens
  openThinking: new Map(),// agentID -> {card, body}
  openStatement: new Map(),// agentID -> {card, body}
  pendingTools: new Map(),// agentID -> [{card, name, ...}]
  shots: new Map(),       // key -> tile element
  crewState: new Map(),   // role -> {strip, stateEl, live}
  analysisLoaded: false,
  finalShown: null,
  sawEvents: false,
};

/* ---------- routing ---------- */

function route() {
  const h = location.hash || "#/";
  const m = h.match(/^#\/prod\/([\w-]+)/);
  if (m) {
    showView("view-room");
    enterRoom(m[1]);
  } else {
    leaveRoom();
    showView("view-lobby");
    loadCallsheet();
  }
}
window.addEventListener("hashchange", route);

function showView(id) {
  document.querySelectorAll(".view").forEach((v) => v.classList.remove("active"));
  $(id).classList.add("active");
}

/* ---------- lobby ---------- */

// Intake files live in JS state so selections accumulate across multiple
// drops/picks instead of replacing each other.
const intake = { music: [], opening: [], refs: [] };

function wireDrop(dropId, inputId, kind, single) {
  const drop = $(dropId);
  const input = $(inputId);
  const body = drop.querySelector(".drop-body");
  const orig = body.innerHTML;

  const render = () => {
    const files = intake[kind];
    drop.classList.toggle("filled", files.length > 0);
    if (!files.length) { body.innerHTML = orig; return; }
    body.innerHTML = "";

    if (kind === "music") {
      const f = files[0];
      body.appendChild(el("span", "file-line", `${f.name}  (${(f.size / 1048576).toFixed(1)} MB)`));
      body.appendChild(el("span", "thumb-count", "click or drop to replace"));
      return;
    }

    const grid = el("div", "thumb-grid");
    files.forEach((f, i) => {
      const cell = el("span", "thumb" + (kind === "opening" ? " opening" : ""));
      cell.title = f.name;
      const img = el("img");
      img.alt = f.name;
      if (!f._url) f._url = URL.createObjectURL(f);
      img.src = f._url;
      cell.appendChild(img);
      if (kind === "opening") cell.appendChild(el("span", "thumb-tag", "opening frame"));
      const rm = el("button", "thumb-rm", "×");
      rm.type = "button";
      rm.setAttribute("aria-label", "Remove " + f.name);
      rm.addEventListener("click", (e) => {
        e.preventDefault();
        e.stopPropagation();
        URL.revokeObjectURL(f._url);
        files.splice(i, 1);
        render();
      });
      cell.appendChild(rm);
      grid.appendChild(cell);
    });
    body.appendChild(grid);
    if (kind === "refs") {
      body.appendChild(el("span", "thumb-count",
        `${files.length} reference${files.length > 1 ? "s" : ""}  -  click or drop to add more`));
    } else {
      body.appendChild(el("span", "thumb-count", "click or drop to replace"));
    }
  };

  const accept = (list) => {
    let files = [...list];
    if (kind !== "music") files = files.filter((f) => f.type.startsWith("image/"));
    if (!files.length) return;
    if (single) {
      intake[kind].forEach((f) => f._url && URL.revokeObjectURL(f._url));
      intake[kind] = [files[0]];
    } else {
      for (const f of files) {
        if (!intake[kind].some((g) => g.name === f.name && g.size === f.size)) {
          intake[kind].push(f);
        }
      }
    }
    render();
  };

  input.addEventListener("change", () => { accept(input.files); input.value = ""; });
  ["dragover", "dragenter"].forEach((ev) => drop.addEventListener(ev, (e) => { e.preventDefault(); drop.classList.add("over"); }));
  ["dragleave", "drop"].forEach((ev) => drop.addEventListener(ev, (e) => { e.preventDefault(); drop.classList.remove("over"); }));
  drop.addEventListener("drop", (e) => accept(e.dataTransfer.files));
}

wireDrop("drop-music", "file-music", "music", true);
wireDrop("drop-opening", "file-opening", "opening", true);
wireDrop("drop-refs", "file-refs", "refs", false);

// Production mode: a music video (the track is the source) or a narrated
// film (source text is the source, drawn in a chosen animation style).
function currentMode() {
  const r = document.querySelector('input[name="mode"]:checked');
  return r ? r.value : "music-video";
}

function applyMode() {
  const film = currentMode() === "film";
  $("film-fields").hidden = !film;
  $("in-lyrics").hidden = film;
  $("music-drop-title").textContent = film ? "Drop a music score here" : "Drop the music track here";
  $("music-drop-hint").textContent = film
    ? "Optional. A score to sit under the narration."
    : "mp3, wav, m4a, flac. The track is the source of the film.";
  $("in-title").placeholder = film
    ? "Production title (defaults to the opening words)"
    : "Production title (defaults to the track name)";
}

document.querySelectorAll('input[name="mode"]').forEach((r) => r.addEventListener("change", applyMode));

async function loadStyles() {
  const sel = $("in-style");
  try {
    const res = await fetch("/api/styles");
    const styles = await res.json();
    for (const s of styles) {
      const opt = document.createElement("option");
      opt.value = s.id;
      opt.textContent = s.name;
      opt.dataset.blurb = s.blurb;
      sel.appendChild(opt);
    }
  } catch (ex) {
    // styles are still selectable by id if the fetch fails; nothing to do
  }
  sel.addEventListener("change", () => {
    const opt = sel.options[sel.selectedIndex];
    $("style-blurb").textContent = opt && opt.dataset.blurb ? opt.dataset.blurb : "";
  });
}
loadStyles();
applyMode();

$("intake").addEventListener("submit", async (e) => {
  e.preventDefault();
  const err = $("form-error");
  err.hidden = true;
  const mode = currentMode();
  if (mode === "film") {
    if (!$("in-source").value.trim()) { err.textContent = "Paste the source text for the film."; err.hidden = false; return; }
    if (!$("in-style").value) { err.textContent = "Choose an animation style."; err.hidden = false; return; }
  } else if (!intake.music.length) {
    err.textContent = "A music track is required."; err.hidden = false; return;
  }

  const fd = new FormData();
  fd.append("mode", mode);
  fd.append("title", $("in-title").value);
  fd.append("prompt", $("in-prompt").value);
  if (mode === "film") {
    fd.append("source_text", $("in-source").value);
    fd.append("animation_style", $("in-style").value);
  } else {
    fd.append("lyrics", $("in-lyrics").value);
  }
  if (intake.music.length) fd.append("music", intake.music[0]);
  if (intake.opening.length) fd.append("opening", intake.opening[0]);
  intake.refs.forEach((f) => fd.append("references", f));

  const btn = $("btn-greenlight");
  btn.disabled = true;
  btn.textContent = "Uploading sources...";
  try {
    const res = await fetch("/api/productions", { method: "POST", body: fd });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || res.statusText);
    location.hash = `#/prod/${data.id}`;
  } catch (ex) {
    err.textContent = ex.message;
    err.hidden = false;
  } finally {
    btn.disabled = false;
    btn.textContent = "Greenlight production";
  }
});

async function loadCallsheet() {
  const box = $("callsheet");
  try {
    const res = await fetch("/api/productions");
    const list = await res.json();
    box.innerHTML = "";
    if (!list.length) {
      box.appendChild(el("div", "empty", "No productions yet. Start with a track."));
      return;
    }
    list.forEach((p) => {
      const row = el("button", "sheet-row");
      row.type = "button";
      row.appendChild(el("span", `status-dot ${p.status}`));
      row.appendChild(el("span", "sheet-title", p.title));
      row.appendChild(el("span", "sheet-meta", `${p.status}  ${new Date(p.created_at).toLocaleDateString()}`));
      row.addEventListener("click", () => { location.hash = `#/prod/${p.id}`; });
      box.appendChild(row);
    });
  } catch { /* server down; leave as is */ }
}

/* ---------- control room ---------- */

function fileURL(rel) {
  return `/api/productions/${state.prod.id}/files/${rel.split("/").map(encodeURIComponent).join("/")}`;
}

async function enterRoom(id) {
  if (state.prod && state.prod.id === id) return;
  leaveRoom();

  const res = await fetch(`/api/productions/${id}`);
  if (!res.ok) { location.hash = "#/"; return; }
  state.prod = await res.json();

  buildCrew();
  $("feed").innerHTML = "";
  $("feed").appendChild(emptyStage());
  $("shot-grid").innerHTML = "";
  $("board-empty").style.display = "";
  $("screening-body").innerHTML = "";
  $("screening-body").appendChild(el("div", "empty", "The final cut premieres here"));
  $("screening-meta").hidden = true;
  $("score-caption").textContent = "awaiting audio analysis";
  const canvas = $("score-canvas");
  canvas.getContext("2d").clearRect(0, 0, canvas.width, canvas.height);

  updateHeader();
  tryLoadAnalysis();
  if (state.prod.final_video) showFinal(state.prod.final_video, null);

  state.es = new EventSource(`/api/productions/${id}/events`);
  state.es.onmessage = (m) => {
    try { handleEvent(JSON.parse(m.data)); } catch { /* skip malformed */ }
  };

  state.timer = setInterval(tickTimecode, 500);
  state.poll = setInterval(refreshProduction, 8000);
}

function leaveRoom() {
  if (state.es) { state.es.close(); state.es = null; }
  if (state.timer) { clearInterval(state.timer); state.timer = null; }
  if (state.poll) { clearInterval(state.poll); state.poll = null; }
  state.prod = null;
  state.tokens.clear();
  state.openThinking.clear();
  state.openStatement.clear();
  state.pendingTools.clear();
  state.shots.clear();
  state.crewState.clear();
  state.analysisLoaded = false;
  state.finalShown = null;
  state.sawEvents = false;
  state.filterRole = null;
  $("head-title").textContent = "";
  $("head-timecode").hidden = true;
  $("head-tally").hidden = true;
  $("head-tokens").textContent = "";
  $("btn-start").hidden = true;
  $("btn-cancel").hidden = true;
}

function emptyStage() {
  const d = el("div", "empty-stage");
  d.id = "empty-stage";
  const g = el("span", "glyph", "AUTEUR");
  d.appendChild(g);
  d.appendChild(document.createTextNode("The stage is set. Roll cameras to put the crew to work."));
  return d;
}

async function refreshProduction() {
  if (!state.prod) return;
  try {
    const res = await fetch(`/api/productions/${state.prod.id}`);
    if (res.ok) {
      state.prod = await res.json();
      updateHeader();
      if (state.prod.final_video && state.finalShown !== state.prod.final_video) {
        showFinal(state.prod.final_video, null);
      }
    }
  } catch { /* transient */ }
}

function updateHeader() {
  const p = state.prod;
  if (!p) return;
  $("head-title").innerHTML = "";
  $("head-title").appendChild(el("strong", null, p.title));

  const tally = $("head-tally");
  const text = $("head-tally-text");
  tally.hidden = false;
  tally.className = "tally";
  if (p.status === "running") { tally.classList.add("rec"); text.textContent = "rolling"; }
  else if (p.status === "completed") { tally.classList.add("done"); text.textContent = "delivered"; }
  else if (p.status === "failed") { tally.classList.add("failed"); text.textContent = "failed"; }
  else if (p.status === "cancelled") { text.textContent = "stopped"; }
  else { text.textContent = "standby"; }

  $("head-timecode").hidden = false;
  tickTimecode();

  $("btn-start").hidden = p.status === "running";
  $("btn-start").textContent = (p.status === "draft") ? "Roll cameras" : "Roll again";
  $("btn-cancel").hidden = p.status !== "running";
}

function tickTimecode() {
  const p = state.prod;
  if (!p) return;
  let ms = 0;
  if (p.started_at && p.started_at !== "0001-01-01T00:00:00Z") {
    const start = new Date(p.started_at).getTime();
    const end = (p.status === "running") ? Date.now() : new Date(p.finished_at || p.started_at).getTime();
    ms = Math.max(0, end - start);
  }
  const s = Math.floor(ms / 1000);
  const hh = String(Math.floor(s / 3600)).padStart(2, "0");
  const mm = String(Math.floor((s % 3600) / 60)).padStart(2, "0");
  const ss = String(s % 60).padStart(2, "0");
  $("head-timecode").textContent = `${hh}:${mm}:${ss}`;
}

$("btn-start").addEventListener("click", async () => {
  if (!state.prod) return;
  $("btn-start").disabled = true;
  try {
    const res = await fetch(`/api/productions/${state.prod.id}/start`, { method: "POST" });
    if (res.ok) {
      removeEmptyStage();
      await refreshProduction();
    }
  } finally { $("btn-start").disabled = false; }
});

$("btn-cancel").addEventListener("click", async () => {
  if (!state.prod) return;
  await fetch(`/api/productions/${state.prod.id}/cancel`, { method: "POST" });
  await refreshProduction();
});

/* ---------- crew rail ---------- */

function buildCrew() {
  const box = $("crew-strips");
  box.innerHTML = "";
  state.crewState.clear();
  ROLES.forEach((role) => {
    const strip = el("button", "strip");
    strip.type = "button";
    strip.style.setProperty("--role", ROLE_COLORS[role]);
    strip.appendChild(el("span", "lamp"));
    const who = el("span", "who");
    who.appendChild(el("span", "role-name", role));
    const st = el("span", "role-state", "standing by");
    who.appendChild(st);
    strip.appendChild(who);
    strip.addEventListener("click", () => toggleFilter(role, strip));
    box.appendChild(strip);
    state.crewState.set(role, { strip, stateEl: st });
  });
}

function crewNote(role, note, mode) {
  const c = state.crewState.get(role);
  if (!c) return;
  if (note) c.stateEl.textContent = note;
  if (mode === "live") { c.strip.classList.add("live"); c.strip.classList.remove("wrapped"); }
  if (mode === "wrapped") { c.strip.classList.remove("live"); c.strip.classList.add("wrapped"); }
}

function toggleFilter(role, strip) {
  document.querySelectorAll(".strip").forEach((s) => s.classList.remove("selected"));
  if (state.filterRole === role) {
    state.filterRole = null;
    $("filter-note").hidden = true;
  } else {
    state.filterRole = role;
    strip.classList.add("selected");
    $("filter-role").textContent = role;
    $("filter-note").hidden = false;
  }
  applyFilter();
}

$("filter-clear").addEventListener("click", () => {
  state.filterRole = null;
  $("filter-note").hidden = true;
  document.querySelectorAll(".strip").forEach((s) => s.classList.remove("selected"));
  applyFilter();
});

function applyFilter() {
  const feed = $("feed");
  [...feed.children].forEach((n) => {
    const role = n.dataset.role;
    n.style.display = (!state.filterRole || !role || role === state.filterRole) ? "" : "none";
  });
}

/* ---------- feed ---------- */

const feedWrap = $("feed-wrap");
const feed = $("feed");
let autoscroll = true;

feed.addEventListener("scroll", () => {
  const nearBottom = feed.scrollHeight - feed.scrollTop - feed.clientHeight < 80;
  autoscroll = nearBottom;
  feedWrap.classList.toggle("detached", !nearBottom);
});
$("jump-live").addEventListener("click", () => {
  feed.scrollTop = feed.scrollHeight;
});

function pushFeed(node, role) {
  removeEmptyStage();
  if (role) node.dataset.role = role;
  if (state.filterRole && role && role !== state.filterRole) node.style.display = "none";
  feed.appendChild(node);
  while (feed.children.length > 1200) feed.removeChild(feed.firstChild);
  if (autoscroll) feed.scrollTop = feed.scrollHeight;
}

function removeEmptyStage() {
  const e = $("empty-stage");
  if (e) e.remove();
}

function relTC(t) {
  const p = state.prod;
  if (!p || !p.started_at || p.started_at === "0001-01-01T00:00:00Z") return "";
  const s = Math.max(0, Math.floor((new Date(t).getTime() - new Date(p.started_at).getTime()) / 1000));
  return `T+${String(Math.floor(s / 60)).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;
}

function byline(ev) {
  const b = el("div", "byline");
  b.appendChild(el("span", null, ev.agent_name || "studio"));
  b.appendChild(el("span", "tc", relTC(ev.time)));
  return b;
}

function roleVar(ev) {
  return ROLE_COLORS[ev.agent_name] || "var(--line-2)";
}

/* ---------- event handling ---------- */

function handleEvent(ev) {
  state.sawEvents = true;
  const role = ev.agent_name;

  switch (ev.type) {
    case "producer_note":
    case "note": {
      const d = el("div", "ev card status");
      d.style.setProperty("--role", "var(--producer, #d3a97d)");
      d.appendChild(el("div", "body", `PRODUCER: ${ev.message.replace(/^PRODUCER'S NOTE[^:]*: /, "")}`));
      pushFeed(d, "producer");
      break;
    }

    case "approval_requested": {
      $("approval-stage").textContent = (ev.data && ev.data.stage) || ev.message;
      $("approval-summary").textContent = (ev.data && ev.data.summary) || "";
      $("approval-box").hidden = false;
      const d = el("div", "ev card status");
      d.appendChild(el("div", "body", `Awaiting producer approval: ${ev.message}`));
      pushFeed(d, "director");
      break;
    }
    case "agent_start": {
      closeOpenCards(ev.agent_id);
      const d = el("div", "slate-divider");
      d.style.setProperty("--role", roleVar(ev));
      d.appendChild(el("span", null, `${role} on set`));
      d.appendChild(el("span", "tc", relTC(ev.time)));
      pushFeed(d, role);
      crewNote(role, "on set", "live");
      break;
    }

    case "reasoning_delta": {
      let t = state.openThinking.get(ev.agent_id);
      if (!t) {
        const card = el("div", "ev card thinking collapsed");
        card.style.setProperty("--role", roleVar(ev));
        const body = el("div", "body");
        card.appendChild(body);
        const ex = el("button", "expando", "expand thinking");
        ex.type = "button";
        ex.addEventListener("click", () => {
          card.classList.toggle("collapsed");
          ex.textContent = card.classList.contains("collapsed") ? "expand thinking" : "collapse";
        });
        card.appendChild(ex);
        t = { card, body };
        state.openThinking.set(ev.agent_id, t);
        pushFeed(card, role);
      }
      t.body.textContent = clampText(t.body.textContent + ev.message, 20000);
      if (autoscroll) feed.scrollTop = feed.scrollHeight;
      crewNote(role, "thinking...");
      break;
    }

    case "text_delta": {
      let s = state.openStatement.get(ev.agent_id);
      if (!s) {
        const card = el("div", "ev card statement");
        card.style.setProperty("--role", roleVar(ev));
        card.appendChild(byline(ev));
        const body = el("div", "body");
        card.appendChild(body);
        s = { card, body };
        state.openStatement.set(ev.agent_id, s);
        pushFeed(card, role);
      }
      s.body.textContent += ev.message;
      if (autoscroll) feed.scrollTop = feed.scrollHeight;
      break;
    }

    case "text": {
      const s = state.openStatement.get(ev.agent_id);
      state.openThinking.delete(ev.agent_id);
      if (s) {
        s.body.textContent = ev.message; // authoritative full text
        state.openStatement.delete(ev.agent_id);
      } else if (ev.message) {
        const card = el("div", "ev card statement");
        card.style.setProperty("--role", roleVar(ev));
        card.appendChild(byline(ev));
        card.appendChild(el("div", "body", ev.message));
        pushFeed(card, role);
      }
      if (ev.tokens) state.tokens.set(ev.agent_id, ev.tokens);
      renderTokens();
      break;
    }

    case "tool_call": {
      closeOpenCards(ev.agent_id);
      const card = el("div", "ev card tool");
      card.style.setProperty("--role", roleVar(ev));
      card.appendChild(byline(ev));
      const line = el("div", "tool-line");
      line.appendChild(el("span", "spin"));
      line.appendChild(el("span", "tool-name", ev.tool_name));
      line.appendChild(el("span", "tool-args", summarizeArgs(ev.tool_name, ev.tool_input)));
      card.appendChild(line);
      if (ev.tool_input && Object.keys(ev.tool_input).length) {
        const det = el("details");
        det.appendChild(el("summary", null, "input"));
        const pre = el("pre", null, JSON.stringify(ev.tool_input, null, 2));
        det.appendChild(pre);
        card.appendChild(det);
      }
      const q = state.pendingTools.get(ev.agent_id) || [];
      q.push({ card, line, name: ev.tool_name });
      state.pendingTools.set(ev.agent_id, q);
      pushFeed(card, role);
      crewNote(role, toolVerb(ev.tool_name, ev.tool_input));
      break;
    }

    case "tool_result": {
      const q = state.pendingTools.get(ev.agent_id) || [];
      const idx = q.findIndex((p) => p.name === ev.tool_name);
      const isErr = !!(ev.data && ev.data.is_error);
      if (idx >= 0) {
        const p = q.splice(idx, 1)[0];
        p.line.querySelector(".spin")?.remove();
        const mark = el("span", `mark ${isErr ? "err" : "ok"}`, isErr ? "x" : "ok");
        p.line.insertBefore(mark, p.line.firstChild);
        if (ev.duration_ms) p.line.appendChild(el("span", "dur", fmtDur(ev.duration_ms)));
        if (ev.message) {
          const det = el("details");
          det.appendChild(el("summary", null, isErr ? "error" : "result"));
          det.appendChild(el("pre", null, ev.message));
          p.card.appendChild(det);
        }
      }
      break;
    }

    case "status": {
      const line = el("div", "ev status-line");
      line.style.setProperty("--role", roleVar(ev));
      const tag = el("span", "role-tag", (ev.agent_name || "") + "  ");
      line.appendChild(tag);
      line.appendChild(document.createTextNode(ev.message));
      pushFeed(line, role);
      if (ev.data && ev.data.shot_id) updateShotTile(ev);
      break;
    }

    case "artifact":
      handleArtifact(ev);
      break;

    case "complete": {
      closeOpenCards(ev.agent_id);
      const d = el("div", "slate-divider");
      d.style.setProperty("--role", roleVar(ev));
      d.appendChild(el("span", null, `${role} wrapped`));
      d.appendChild(el("span", "tc", relTC(ev.time)));
      pushFeed(d, role);
      crewNote(role, "wrapped", "wrapped");
      if (ev.tokens) state.tokens.set(ev.agent_id, ev.tokens);
      renderTokens();
      if (role === "director") refreshProduction();
      break;
    }

    case "error": {
      const card = el("div", "ev card error-card");
      card.style.setProperty("--role", roleVar(ev));
      card.appendChild(byline(ev));
      card.appendChild(el("div", "body", ev.message));
      pushFeed(card, role);
      crewNote(role, "error");
      refreshProduction();
      break;
    }
  }
}

function closeOpenCards(agentID) {
  state.openThinking.delete(agentID);
  state.openStatement.delete(agentID);
}

function clampText(s, max) {
  return s.length > max ? "..." + s.slice(s.length - max) : s;
}

function fmtDur(ms) {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 120000) return `${(ms / 1000).toFixed(1)}s`;
  return `${Math.round(ms / 60000)}m`;
}

function renderTokens() {
  let sum = 0;
  state.tokens.forEach((v) => { sum += v; });
  $("head-tokens").textContent = sum ? `${(sum / 1000).toFixed(0)}k tokens` : "";
}

function summarizeArgs(name, input) {
  if (!input) return "";
  switch (name) {
    case "bash": return input.command || "";
    case "read_file": case "write_file": case "probe_media": case "analyze_audio": return input.path || "";
    case "read_skill": return input.name || "";
    case "delegate": return `${input.role}: ${String(input.task || "").slice(0, 120)}`;
    case "delegate_parallel": return `${(input.tasks || []).length} tasks: ${(input.tasks || []).map((t) => t.role).join(", ")}`;
    case "generate_video_clips": return `${(input.shots || []).length} shot(s): ${(input.shots || []).map((s) => s.id).join(", ")}`;
    case "generate_image": return `${input.id}: ${String(input.prompt || "").slice(0, 100)}`;
    case "analyze_image": return input.path || "";
    case "stitch_timeline": return `${(input.clips || []).length} clips`;
    case "finalize_production": return input.video_path || "";
    case "finish": return String(input.report || "").slice(0, 100);
    default: {
      const s = JSON.stringify(input);
      return s.length > 140 ? s.slice(0, 140) + "..." : s;
    }
  }
}

function toolVerb(name, input) {
  switch (name) {
    case "bash": return "at the terminal";
    case "analyze_audio": return "listening to the track";
    case "analyze_image": return "studying imagery";
    case "generate_image": return "shooting a frame";
    case "generate_video_clips": return `shooting ${(input && input.shots || []).length} clip(s)`;
    case "stitch_timeline": return "cutting the timeline";
    case "master_audio": return "mastering audio";
    case "delegate": return `briefing the ${input && input.role}`;
    case "delegate_parallel": return "briefing the crew";
    case "read_skill": return "consulting a skill";
    case "write_file": return "writing documents";
    case "finalize_production": return "delivering the film";
    default: return name.replaceAll("_", " ");
  }
}

/* ---------- shot board ---------- */

function shotKey(ev) {
  return (ev.data && (ev.data.shot_id || ev.data.id || ev.data.path)) || Math.random().toString(36);
}

function updateShotTile(ev) {
  const d = ev.data;
  const key = d.shot_id;
  if (!key) return;
  let tile = state.shots.get(key);
  if (d.state === "submitted" || d.state === "rendering") {
    if (!tile) {
      tile = el("button", "shot pending");
      tile.type = "button";
      tile.appendChild(el("span", "spin"));
      tile.appendChild(el("span", "shot-tag", key));
      state.shots.set(key, tile);
      $("board-empty").style.display = "none";
      $("shot-grid").appendChild(tile);
    }
  } else if (d.state === "failed" && tile && tile.classList.contains("pending")) {
    tile.remove();
    state.shots.delete(key);
  }
}

function handleArtifact(ev) {
  const d = ev.data || {};
  const role = ev.agent_name;

  // Feed card with thumbnail where available.
  const card = el("div", "ev card artifact-card");
  card.style.setProperty("--role", roleVar(ev));
  const thumbRel = d.thumbnail || (d.kind === "frame" ? d.path : null);
  if (thumbRel) {
    const img = el("img");
    img.src = fileURL(thumbRel);
    img.alt = "";
    card.appendChild(img);
  }
  const info = el("div", "art-info");
  info.appendChild(el("div", "art-kind", d.kind || "artifact"));
  info.appendChild(el("div", "art-path", d.path || ev.message));
  card.appendChild(info);
  pushFeed(card, role);

  switch (d.kind) {
    case "analysis":
      tryLoadAnalysis(true);
      break;
    case "frame":
      addShot(d.id || d.path, d.path, d.path, "frame", d.prompt);
      break;
    case "clip":
      addShot(d.shot_id, d.thumbnail, d.path, "clip", d.prompt);
      break;
    case "cut":
    case "master":
      showFinal(d.path, d.kind);
      break;
    case "final":
      showFinal(d.path, "final");
      refreshProduction();
      break;
  }
}

function addShot(key, thumbRel, mediaRel, kind, prompt) {
  let tile = state.shots.get(key);
  if (!tile) {
    tile = el("button", "shot");
    tile.type = "button";
    state.shots.set(key, tile);
    $("board-empty").style.display = "none";
    $("shot-grid").appendChild(tile);
  }
  tile.className = `shot kind-${kind}`;
  tile.innerHTML = "";
  if (thumbRel) {
    const img = el("img");
    img.src = fileURL(thumbRel);
    img.alt = key;
    tile.appendChild(img);
  }
  tile.appendChild(el("span", "shot-tag", `${kind === "frame" ? "frame " : ""}${key}`));
  tile.onclick = () => openLightbox(kind, mediaRel, key, prompt);
}

/* ---------- screening room ---------- */

function showFinal(rel, kind) {
  state.finalShown = rel;
  const body = $("screening-body");
  body.innerHTML = "";
  const vid = el("video");
  vid.controls = true;
  vid.src = fileURL(rel);
  body.appendChild(vid);
  const meta = $("screening-meta");
  meta.hidden = false;
  meta.innerHTML = "";
  meta.appendChild(el("span", null, kind === "final" ? "final master" : (kind || "cut")));
  const a = el("a", null, "download");
  a.href = fileURL(rel);
  a.download = "";
  meta.appendChild(a);
}

/* ---------- score sparkline ---------- */

async function tryLoadAnalysis(force) {
  if (state.analysisLoaded && !force) return;
  if (!state.prod) return;
  try {
    const res = await fetch(fileURL("analysis/audio_analysis.json"));
    if (!res.ok) return;
    const a = await res.json();
    state.analysisLoaded = true;
    drawScore(a);
    $("score-caption").textContent =
      `${a.tempo_bpm ? Math.round(a.tempo_bpm) + " bpm" : ""}  ${fmtClock(a.duration_seconds)}  ${a.sections ? a.sections.length + " sections" : ""}`;
  } catch { /* not there yet */ }
}

function fmtClock(s) {
  if (!s) return "";
  return `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, "0")}`;
}

function drawScore(a) {
  const canvas = $("score-canvas");
  const ctx = canvas.getContext("2d");
  const W = canvas.width, H = canvas.height;
  ctx.clearRect(0, 0, W, H);
  const curve = a.energy_curve_per_second || [];
  if (!curve.length) return;

  // Section shading.
  const secColors = { peak: "rgba(232,163,61,0.20)", build: "rgba(232,163,61,0.10)", breakdown: "rgba(122,162,247,0.10)", intro: "rgba(255,255,255,0.03)", outro: "rgba(255,255,255,0.03)", steady: "rgba(255,255,255,0.05)" };
  (a.sections || []).forEach((s) => {
    ctx.fillStyle = secColors[s.label] || "rgba(255,255,255,0.04)";
    const x0 = (s.start_seconds / a.duration_seconds) * W;
    const x1 = (s.end_seconds / a.duration_seconds) * W;
    ctx.fillRect(x0, 0, x1 - x0, H);
  });

  // Energy bars.
  ctx.fillStyle = "#e8a33d";
  const bw = W / curve.length;
  curve.forEach((v, i) => {
    const h = (v / 100) * (H - 8);
    ctx.globalAlpha = 0.35 + 0.65 * (v / 100);
    ctx.fillRect(i * bw, H - h, Math.max(1, bw - 0.5), h);
  });
  ctx.globalAlpha = 1;
}

/* ---------- lightbox ---------- */

function openLightbox(kind, mediaRel, title, prompt) {
  const media = $("lightbox-media");
  media.innerHTML = "";
  if (kind === "clip" || kind === "final" || kind === "cut" || kind === "master") {
    const v = el("video");
    v.controls = true;
    v.autoplay = true;
    v.loop = true;
    v.src = fileURL(mediaRel);
    media.appendChild(v);
  } else {
    const img = el("img");
    img.src = fileURL(mediaRel);
    img.alt = title;
    media.appendChild(img);
  }
  const meta = $("lightbox-meta");
  meta.innerHTML = "";
  meta.appendChild(el("span", "prompt", prompt || ""));
  meta.appendChild(el("span", null, title));
  $("lightbox").classList.add("open");
}

function closeLightbox() {
  $("lightbox").classList.remove("open");
  $("lightbox-media").innerHTML = "";
}
$("lightbox-close").addEventListener("click", closeLightbox);
$("lightbox").addEventListener("click", (e) => { if (e.target === $("lightbox")) closeLightbox(); });
document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeLightbox(); });

/* ---------- boot ---------- */

route();

// --- Producer channel ---
$("note-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const input = $("note-input");
  const text = input.value.trim();
  if (!text || !state.prod) return;
  input.disabled = true;
  try {
    await fetch(`/api/productions/${state.prod.id}/notes`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text }),
    });
    input.value = "";
  } finally { input.disabled = false; input.focus(); }
});

async function sendApproval(approve) {
  if (!state.prod) return;
  let notes = "";
  if (!approve) {
    notes = prompt("What should change?") || "";
    if (!notes) return;
  }
  await fetch(`/api/productions/${state.prod.id}/approval`, {
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ approve, notes }),
  });
  $("approval-box").hidden = true;
}
$("btn-approve").addEventListener("click", () => sendApproval(true));
$("btn-changes").addEventListener("click", () => sendApproval(false));
