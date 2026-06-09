"use strict";

// moraine UI — vanilla JS, progressive and dependency-free.
// Covers: US1 (view groups), US2 (commit w/ confirm), US3 (drag&drop),
// US4 (inline edit label + dest), US5 (lightbox preview).

const groupsEl = document.getElementById("groups");
const tpl = document.getElementById("group-tpl");
const toastsEl = document.getElementById("toasts");
const lightbox = document.getElementById("lightbox");
const lightboxImg = document.getElementById("lightbox-img");
const lightboxClose = document.getElementById("lightbox-close");

function toast(message, kind = "") {
  const el = document.createElement("div");
  el.className = "toast" + (kind ? " " + kind : "");
  el.textContent = message;
  toastsEl.appendChild(el);
  setTimeout(() => el.remove(), 5000);
}

function fmtDate(iso) {
  const d = new Date(iso);
  if (isNaN(d)) return "";
  return d.toLocaleDateString("fr-FR", { year: "numeric", month: "short", day: "numeric" });
}

async function api(method, url, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(url, opts);
  let data = null;
  const ct = res.headers.get("content-type") || "";
  if (ct.includes("application/json")) {
    data = await res.json().catch(() => null);
  }
  return { status: res.status, ok: res.ok, data };
}

// ---- Rendering (US1) -------------------------------------------------------

function buildThumb(photo) {
  const t = document.createElement("div");
  t.className = "thumb";
  t.draggable = true;
  t.dataset.photoId = photo.id;
  t.title = photo.name;

  const img = document.createElement("img");
  img.loading = "lazy";
  img.src = photo.thumb_url;
  img.alt = photo.name;
  t.appendChild(img);

  const name = document.createElement("span");
  name.className = "name";
  name.textContent = photo.name;
  t.appendChild(name);

  // US5: click opens the full-resolution preview.
  t.addEventListener("click", () => openLightbox(photo.photo_url, photo.name));

  // US3: native HTML5 drag source.
  t.addEventListener("dragstart", (e) => {
    e.dataTransfer.setData("text/plain", photo.id);
    e.dataTransfer.effectAllowed = "move";
    t.classList.add("dragging");
  });
  t.addEventListener("dragend", () => t.classList.remove("dragging"));

  return t;
}

function buildGroup(group) {
  const node = tpl.content.firstElementChild.cloneNode(true);
  node.dataset.groupId = group.id;

  const label = node.querySelector(".group-label");
  label.textContent = group.label;

  node.querySelector(".group-date").textContent = fmtDate(group.start);
  node.querySelector(".group-count").textContent = group.count + " photo" + (group.count > 1 ? "s" : "");

  const dest = node.querySelector(".group-dest");
  dest.value = group.dest_subdir;

  const strip = node.querySelector(".group-strip");
  for (const p of group.photos) strip.appendChild(buildThumb(p));

  wireGroupEditing(node, label, dest);   // US4
  wireGroupCommit(node);                 // US2
  wireGroupDrop(node, strip);            // US3
  return node;
}

async function loadGroups() {
  try {
    const { data } = await api("GET", "/api/groups");
    render(data && data.groups ? data.groups : []);
  } catch (err) {
    groupsEl.innerHTML = "";
    toast("Impossible de charger les groupes : " + err, "error");
  }
}

function render(groups) {
  groupsEl.innerHTML = "";
  if (!groups.length) {
    const p = document.createElement("p");
    p.className = "empty";
    p.textContent = "Aucun groupe trouvé dans ce dossier.";
    groupsEl.appendChild(p);
    return;
  }
  for (const g of groups) groupsEl.appendChild(buildGroup(g));
}

// ---- Editing label + destination (US4) -------------------------------------

function wireGroupEditing(node, label, dest) {
  const id = node.dataset.groupId;

  label.addEventListener("blur", async () => {
    const value = label.textContent.trim();
    if (!value) { label.textContent = node.dataset.lastLabel || ""; return; }
    const { ok } = await api("PATCH", `/api/groups/${id}`, { label: value });
    if (ok) node.dataset.lastLabel = value;
    else toast("Le libellé n'a pas pu être enregistré.", "error");
  });
  label.addEventListener("keydown", (e) => {
    if (e.key === "Enter") { e.preventDefault(); label.blur(); }
  });

  dest.addEventListener("change", async () => {
    const value = dest.value.trim();
    const { ok, status, data } = await api("PATCH", `/api/groups/${id}`, { dest_subdir: value });
    if (ok) {
      dest.classList.remove("invalid");
    } else if (status === 422) {
      dest.classList.add("invalid");
      toast((data && data.message) || "Destination invalide (hors du répertoire autorisé).", "error");
    } else {
      toast("La destination n'a pas pu être enregistrée.", "error");
    }
  });
}

// ---- Commit a group to disk (US2) ------------------------------------------

function wireGroupCommit(node) {
  const id = node.dataset.groupId;
  const btn = node.querySelector(".group-commit");
  btn.addEventListener("click", async () => {
    const label = node.querySelector(".group-label").textContent.trim();
    const dest = node.querySelector(".group-dest").value.trim();
    // FR-022: explicit, irreversible-action confirmation before any disk move.
    if (!window.confirm(`Déplacer le groupe « ${label} » vers « ${dest} » ?\nCette action est irréversible.`)) {
      return;
    }
    btn.disabled = true;
    const { ok, status, data } = await api("POST", `/api/groups/${id}/commit`);
    if (status === 200) {
      toast(`${data.moved} photo(s) déplacée(s) vers ${data.dest}.`, "ok");
      node.classList.add("removing");
      setTimeout(() => { node.remove(); ensureNotEmpty(); }, 250);
    } else if (status === 207) {
      // Partial failure: keep failed thumbs, warn precisely (I5/FR-011).
      handlePartialCommit(node, data);
      btn.disabled = false;
    } else if (status === 422) {
      toast((data && data.message) || "Destination invalide — aucun fichier déplacé.", "error");
      btn.disabled = false;
    } else {
      toast((data && data.message) || "Le déplacement a échoué.", "error");
      btn.disabled = false;
    }
  });
}

function handlePartialCommit(node, data) {
  const failed = new Set((data.failed || []).map((f) => f.photo));
  const strip = node.querySelector(".group-strip");
  for (const thumb of strip.querySelectorAll(".thumb")) {
    if (failed.has(thumb.dataset.photoId)) thumb.classList.add("failed");
    else thumb.remove();
  }
  const count = node.querySelector(".group-count");
  count.textContent = failed.size + " photo" + (failed.size > 1 ? "s" : "") + " en échec";
  toast(`${data.moved} déplacée(s), ${failed.size} en échec. Vérifiez les fichiers en rouge.`, "error");
}

// ---- Drag & drop between groups (US3) --------------------------------------

function wireGroupDrop(node, strip) {
  node.addEventListener("dragover", (e) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    node.classList.add("drop-target");
  });
  node.addEventListener("dragleave", () => node.classList.remove("drop-target"));
  node.addEventListener("drop", async (e) => {
    e.preventDefault();
    node.classList.remove("drop-target");
    const photoId = e.dataTransfer.getData("text/plain");
    if (!photoId) return;

    const thumb = document.querySelector(`.thumb[data-photo-id="${photoId}"]`);
    if (!thumb) return;
    const fromGroup = thumb.closest(".group");
    if (fromGroup === node) return; // dropped on its own group

    // Optimistic move (US3: perceived-instant), then reconcile on error.
    strip.appendChild(thumb);
    adjustCount(node, +1);
    adjustCount(fromGroup, -1);

    const { ok } = await api("POST", `/api/photos/${photoId}/move`, { to_group: node.dataset.groupId });
    if (ok) {
      if (fromGroup.querySelectorAll(".thumb").length === 0) fromGroup.remove();
    } else {
      toast("Déplacement refusé — réaffichage de l'état réel.", "error");
      loadGroups(); // full reconcile
    }
  });
}

function adjustCount(groupNode, delta) {
  if (!groupNode) return;
  const el = groupNode.querySelector(".group-count");
  const n = Math.max(0, groupNode.querySelectorAll(".thumb").length);
  el.textContent = n + " photo" + (n > 1 ? "s" : "");
}

function ensureNotEmpty() {
  if (groupsEl.querySelectorAll(".group").length === 0) render([]);
}

// ---- Lightbox (US5) --------------------------------------------------------

function openLightbox(url, alt) {
  lightboxImg.src = url;
  lightboxImg.alt = alt || "";
  lightbox.classList.remove("hidden");
}
function closeLightbox() {
  lightbox.classList.add("hidden");
  lightboxImg.removeAttribute("src");
}
lightboxClose.addEventListener("click", closeLightbox);
lightbox.addEventListener("click", (e) => { if (e.target === lightbox) closeLightbox(); });
document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeLightbox(); });

// ---- Boot ------------------------------------------------------------------

loadGroups();
