// The page shows five views: the library of materials with a publish
// form, the platform accounts, the publish tasks, the schedule, and the settings. State lives here and
// each view re-renders from it; Go pushes task and account changes as events.
import { onFileDrop } from "mygo-runtime";
import { Accounts, Library, Publisher, Scheduler, Settings, Updates, events } from "./mygo";
import type { AccountInfo, FolderInfo, Material, ScheduleInfo, SettingsInfo, SyncInfo, Task, UpdateState } from "./mygo";

type View = "library" | "accounts" | "tasks" | "schedule" | "settings";

const state = {
  view: "library" as View,
  materials: [] as Material[],
  accounts: [] as AccountInfo[],
  tasks: [] as Task[],
  selected: "" as string,
  // Which kind of material the library lists.
  kind: "video" as "video" | "article",
  // Platforms ticked per material, kept while switching between them.
  picks: {} as Record<string, string[]>,
  mode: "auto" as "review" | "auto",
  // Platforms whose login, or login check, is in progress.
  loggingIn: new Set<string>(),
  checking: new Set<string>(),
  sync: null as SyncInfo | null,
  schedule: null as ScheduleInfo | null,
  // The folders the library syncs from, and whether their list is open.
  folders: [] as FolderInfo[],
  showFolders: false,
  settings: null as SettingsInfo | null,
  update: null as UpdateState | null,
};

const $ = <T extends HTMLElement>(sel: string, root: ParentNode = document) => root.querySelector<T>(sel)!;
const view = $("#view");

const esc = (s: string) =>
  s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);

let toastTimer = 0;
function toast(msg: string, error = false) {
  const el = $("#toast");
  el.textContent = msg;
  el.className = error ? "toast error" : "toast";
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => (el.hidden = true), error ? 6000 : 2500);
}

interface AskOptions {
  title?: string;
  ok?: string;
  cancel?: string;
  // danger paints the confirming button red, for deleting.
  danger?: boolean;
  // detail lists what the action touches, in a box that scrolls.
  detail?: string[];
}

// ask asks the user to confirm in a dialog of the app's own look, in place
// of the browser's confirm(). Escape, the cancel button or a click beside
// the dialog says no.
function ask(message: string, opts: AskOptions = {}): Promise<boolean> {
  const dlg = $<HTMLDialogElement>("#ask");
  // One question at a time: a second one while the first is open, as from
  // a double click, is answered no (showModal would throw).
  if (dlg.open) return Promise.resolve(false);
  dlg.innerHTML = `<form method="dialog">
      ${opts.title ? `<h3>${esc(opts.title)}</h3>` : ""}
      <p>${esc(message)}</p>
      ${opts.detail?.length ? `<ul class="ask-detail">${opts.detail.map((d) => `<li>${esc(d)}</li>`).join("")}</ul>` : ""}
      <div class="ask-buttons">
        <button value="no" type="submit">${esc(opts.cancel ?? "取消")}</button>
        <button value="yes" type="submit" class="${opts.danger ? "danger-fill" : "primary"}" autofocus>${esc(opts.ok ?? "确定")}</button>
      </div>
    </form>`;
  return new Promise((resolve) => {
    dlg.returnValue = "";
    dlg.onclose = () => resolve(dlg.returnValue === "yes");
    dlg.onclick = (e) => {
      if (e.target === dlg) dlg.close("no"); // the backdrop
    };
    dlg.showModal();
  });
}

// Runs an action, showing its error instead of throwing.
async function attempt<T>(fn: () => Promise<T>): Promise<T | undefined> {
  try {
    return await fn();
  } catch (err) {
    toast(err instanceof Error ? err.message : String(err), true);
  }
}

const platformName = (id: string) => state.accounts.find((a) => a.id === id)?.name ?? id;
// taskTitle is the title of the material a task publishes, kept on the
// task for one since deleted.
const taskTitle = (t: Task) => state.materials.find((m) => m.id === t.materialId)?.title ?? `${t.title || "（无标题）"}（已删除）`;
const timeText = (iso: string) => {
  const d = new Date(iso);
  return d.getFullYear() < 2000 ? "从未" : d.toLocaleString("zh-CN", { hour12: false });
};

const statusText: Record<string, [string, string]> = {
  pending: ["排队中", ""],
  running: ["进行中", "info"],
  review: ["待确认", "warn"],
  success: ["已发布", "ok"],
  failed: ["失败", "bad"],
  canceled: ["已取消", ""],
};
const statusPill = (s: string) => {
  const [text, cls] = statusText[s] ?? [s, ""];
  return `<span class="pill ${cls}">${text}</span>`;
};

// ---- Navigation ----

function setView(v: View) {
  state.view = v;
  document.querySelectorAll<HTMLButtonElement>(".nav-item").forEach((b) => b.classList.toggle("active", b.dataset.view === v));
  render();
  // A fade on switching pages only: the pages draw again on every change.
  view.animate([{ opacity: 0, transform: "translateY(4px)" }, { opacity: 1, transform: "none" }], { duration: 160, easing: "ease-out" });
  // The pending list snapshots titles and platforms; edits since then would show stale.
  if (v === "schedule") refreshSchedule();
  // New files may have landed in the sync folders.
  if (v === "library") refreshFolders();
}

document.querySelectorAll<HTMLButtonElement>(".nav-item").forEach((b) =>
  b.addEventListener("click", async () => (await keepEdits()) && setView(b.dataset.view as View)),
);

// renderedView is the page last drawn, whose scroll a redraw of the same
// page keeps: pages draw again on every change, as the tasks page does at
// each step of an upload, and would jump back to the top.
let renderedView: View | "" = "";

function render() {
  const body = view.querySelector<HTMLElement>(".view-body");
  const top = renderedView === state.view ? (body?.scrollTop ?? 0) : 0;
  if (state.view === "library") renderLibrary();
  else if (state.view === "accounts") renderAccounts();
  else if (state.view === "schedule") renderSchedule();
  else if (state.view === "settings") renderSettings();
  else renderTasks();
  renderedView = state.view;
  const again = view.querySelector<HTMLElement>(".view-body");
  if (again) again.scrollTop = top;
  renderBadge();
}

function renderBadge() {
  const n = state.tasks.filter((t) => t.status === "running" || t.status === "pending").length;
  const el = $("#running-badge");
  el.hidden = n === 0;
  el.textContent = String(n);
  $("#schedule-on").hidden = !state.schedule?.schedule.enabled;
  $("#update-badge").hidden = !state.update?.newer;
}

// ---- Library ----

function folderButtons() {
  const n = state.folders.reduce((n, f) => n + f.new, 0);
  return state.folders.length
    ? `<button class="ghost ${state.showFolders ? "on" : ""}" data-act="toggle-folders">同步文件夹 ${state.folders.length} ▾</button>
       <button data-act="sync-folders" title="导入各同步文件夹里新增的视频和文章">同步${n ? `（${n} 个新）` : ""}</button>`
    : `<button data-act="add-folder" title="添加一个文件夹，以后一键导入里面新增的视频；标题、简介、标签取自同名 _caption.txt，封面取自 _cover 图片">添加同步文件夹</button>`;
}

function folderPanel() {
  if (!state.showFolders || !state.folders.length) return "";
  return `<div class="folders">
    ${state.folders
      .map(
        (f) => `<div class="folder">
          <span class="path">${esc(f.dir)}</span>
          ${f.error ? `<span class="pill bad" title="${esc(f.error)}">读取失败</span>` : f.new ? `<span class="pill info">${f.new} 个新</span>` : `<span class="muted"><small>没有新文件</small></span>`}
          <button class="ghost" data-act="remove-folder" data-dir="${esc(f.dir)}">移除</button>
          ${f.files?.length ? `<div class="new-files">${f.files.map((n) => `<small>${esc(n)}</small>`).join("")}</div>` : ""}
        </div>`,
      )
      .join("")}
    <div class="row"><button data-act="add-folder">添加文件夹</button><small class="muted">移除文件夹不会删除已导入的素材；导入过的文件不会重复导入</small></div>
  </div>`;
}

// showFolders redraws the sync folder parts of the library alone, which
// leaves unsaved edits be.
function showFolders() {
  const btns = document.querySelector("#folder-btns");
  const box = document.querySelector("#folders-box");
  if (btns) btns.innerHTML = folderButtons();
  if (box) box.innerHTML = folderPanel();
}

// The materials of the kind the library tab shows, the first imported
// first: the order the schedule publishes them in. The store keeps the
// newest first.
const shownMaterials = () => state.materials.filter((m) => m.kind === state.kind).reverse();

function renderLibrary() {
  const list = shownMaterials();
  const sel = list.find((m) => m.id === state.selected) ?? list[0];
  state.selected = sel?.id ?? "";
  const isVideo = state.kind === "video";
  const count = (k: string) => state.materials.filter((m) => m.kind === k).length;
  // Redrawing would scroll the list back to the top, as when picking a
  // material far down it; the same tab keeps its place.
  const old = document.querySelector<HTMLElement>(".mat-list");
  const top = old?.dataset.kind === state.kind ? old.scrollTop : 0;
  view.innerHTML = `
    <div class="view-head">
      <h2>素材库</h2>
      <span class="muted">可直接把视频/Markdown 文件拖进窗口</span>
      <button class="ghost" data-act="delete-published">清理已发布</button>
      <span id="folder-btns" class="row">${folderButtons()}</span>
      <button data-act="import-video">导入视频</button>
      <button data-act="import-article">导入文章</button>
    </div>
    <div id="folders-box">${folderPanel()}</div>
    ${
      state.materials.length === 0
        ? `<div class="empty"><strong>还没有素材</strong><span>导入做好的视频（mp4 等）或文章（.md / .txt），同名图片会自动作为封面</span>
           <div class="row"><button class="primary" data-act="import-video">导入视频</button><button data-act="import-article">导入文章</button></div></div>`
        : `<div class="library">
            <div class="mat-side">
              <div class="kind-tabs">
                <button class="${isVideo ? "active" : ""}" data-act="kind" data-kind="video">视频 <small>${count("video")}</small></button>
                <button class="${isVideo ? "" : "active"}" data-act="kind" data-kind="article">文章 <small>${count("article")}</small></button>
              </div>
              <div class="mat-list" data-kind="${state.kind}">${
                list.length
                  ? list.map(materialItem).join("")
                  : `<div class="empty"><span>还没有${isVideo ? "视频" : "文章"}</span>
                     <button data-act="${isVideo ? "import-video" : "import-article"}">导入${isVideo ? "视频" : "文章"}</button></div>`
              }</div>
            </div>
            <div class="editor" id="editor"></div>
          </div>`
    }`;
  document.querySelector(".mat-list")?.scrollTo(0, top);
  if (sel) renderEditor(sel);
}

// latestTasks returns a material's newest task per platform.
function latestTasks(id: string) {
  const seen = new Set<string>();
  return state.tasks.filter((t) => t.materialId === id && !seen.has(t.platform) && seen.add(t.platform));
}

const busy = (t: Task) => t.status === "pending" || t.status === "running";

// published reports whether a material went out to every platform it was
// sent to, with nothing under way, as Library.DeletePublished decides it.
const published = (m: Material) => {
  const done = m.published ?? {};
  return Object.keys(done).length > 0 && state.tasks.every((t) => t.materialId !== m.id || (t.platform in done && !busy(t)));
};

function materialItem(m: Material) {
  const done = state.accounts.map((a) => a.id).filter((id) => id in (m.published ?? {}));
  // Platforms it went out to, then the latest try at the others.
  const pills = [
    ...done.map((p) => `<span class="pill ok">${esc(platformName(p))} ✓</span>`),
    ...latestTasks(m.id)
      .filter((t) => !done.includes(t.platform))
      .map((t) => `<span class="pill ${statusText[t.status]?.[1] ?? ""}" title="${statusText[t.status]?.[0] ?? t.status}">${esc(platformName(t.platform))}</span>`),
  ].join("");
  const tag = state.tasks.some((t) => t.materialId === m.id && busy(t))
    ? `<span class="state info">发布中</span>`
    : done.length
      ? `<span class="state ok">已发布</span>`
      : `<span class="state">未发布</span>`;
  return `<button class="mat-item ${m.id === state.selected ? "active" : ""}" data-select="${m.id}">
    <span class="kind ${m.kind}">${m.kind === "video" ? "视频" : "文章"}</span>
    <span style="min-width:0"><span class="t">${esc(m.title || "（无标题）")}</span>
    <span class="dots">${tag}${pills}</span></span>
  </button>`;
}

function renderEditor(m: Material) {
  const el = $("#editor");
  const isVideo = m.kind === "video";
  const plats = state.accounts.filter((a) => a.kinds.includes(m.kind));
  const done = m.published ?? {};
  const picks = state.picks[m.id] ?? plats.filter((a) => a.ready && a.loggedIn && !(a.id in done)).map((a) => a.id);
  state.picks[m.id] = picks;
  el.innerHTML = `
    <div class="field">
      <label>标题</label>
      <input type="text" id="f-title" value="${esc(m.title)}" maxlength="100" />
      <small class="limit" id="title-limit"></small>
    </div>
    ${
      isVideo
        ? `<div class="field"><label>简介</label><textarea id="f-desc" rows="4" placeholder="视频简介">${esc(m.description)}</textarea><small class="limit" id="text-limit"></small></div>`
        : `<div class="field"><label>正文（Markdown）</label><textarea id="f-body" rows="12">${esc(m.body ?? "")}</textarea><small class="limit" id="text-limit"></small></div>`
    }
    <div class="field">
      <label>标签</label>
      <input type="text" id="f-tags" value="${esc((m.tags ?? []).join("，"))}" placeholder="用逗号分隔，如：AI，教程，效率" />
      ${isVideo ? `<small class="muted">快手只取前 4 个、抖音前 5 个，B站和小红书最多 10 个</small>` : ""}
    </div>
    ${
      isVideo
        ? `<div class="field"><label>创作声明</label>
      <select id="f-statement">${["", ...statements]
        .map((o) => `<option value="${o}" ${o === (m.statement ?? "") ? "selected" : ""}>${o || "不设置（在页面上自己选）"}</option>`)
        .join("")}</select>
      <small class="muted">B站必填；全自动发布 B站时需要选好</small></div>`
        : ""
    }
    ${collectionField(m, plats)}
    <div class="field">
      <label>${isVideo ? "视频文件" : "文章文件"}</label>
      <div class="row"><span class="path">${esc((isVideo ? m.videoPath : m.articlePath) ?? "")}</span>
      <button class="ghost" data-act="reveal">打开所在文件夹</button></div>
    </div>
    ${isVideo ? "" : articleImages(m)}
    <div class="field">
      <label>封面</label>
      <div class="row">
        <span class="path">${m.coverPath ? esc(m.coverPath) : isVideo ? "未设置（平台将使用默认封面）" : "未设置（公众号文章需要在页面上自己设）"}</span>
        <button data-act="pick-cover">选择封面</button>
        ${m.coverPath ? `<button class="ghost" data-act="clear-cover">移除</button>` : ""}
      </div>
    </div>
    <div class="row">
      <button data-act="save">保存修改</button>
      <button class="danger" data-act="delete">删除素材</button>
    </div>

    <div class="section">
      <h3>发布到</h3>
      <div class="platforms">
        ${plats
          .map((a) => {
            const off = !a.ready;
            const tag = !a.ready
              ? `<span class="pill">开发中</span>`
              : a.loggedIn
                ? `<span class="pill ok">已登录</span>`
                : `<span class="pill warn">未登录</span>`;
            return `<label class="plat ${off ? "disabled" : ""}">
              <input type="checkbox" value="${a.id}" ${picks.includes(a.id) ? "checked" : ""} ${off ? "disabled" : ""} />
              <span class="name">${esc(a.name)}</span>${tag}${a.id in done ? `<button class="pill ok unpub" data-unpub="${a.id}" title="${timeText(done[a.id]!)} 发布；平台上其实没发出去时点这里撤销">已发布 ×</button>` : ""}</label>`;
          })
          .join("")}
      </div>
      <div class="modes">
        <label><input type="radio" name="mode" value="review" ${state.mode === "review" ? "checked" : ""} />填好后由我确认提交</label>
        <label><input type="radio" name="mode" value="auto" ${state.mode === "auto" ? "checked" : ""} />全自动提交</label>
      </div>
      ${plats
        .filter((a) => a.autoNote)
        .map((a) => `<div class="muted"><small>${esc(a.name)}：${esc(a.autoNote!)}</small></div>`)
        .join("")}
      <div class="row"><button class="primary" data-act="publish">开始发布</button>
      <span class="muted">会打开独立的 Chrome 窗口逐个平台上传，请勿关闭</span></div>
    </div>

    <div id="mat-tasks">${materialTasks(m)}</div>`;

  showLimits(m);
  // Assigned rather than added: the editor element outlives each render.
  el.oninput = el.onchange = (e) => {
    if (["f-title", "f-desc", "f-body", "f-tags", "f-weixin"].includes((e.target as HTMLElement).id)) showLimits(m);
  };
  el.querySelectorAll<HTMLInputElement>(".plat input").forEach((cb) =>
    cb.addEventListener("change", () => {
      state.picks[m.id] = [...el.querySelectorAll<HTMLInputElement>(".plat input:checked")].map((x) => x.value);
    }),
  );
  el.querySelectorAll<HTMLInputElement>('input[name="mode"]').forEach((r) =>
    r.addEventListener("change", () => (state.mode = r.value as "review" | "auto")),
  );
}

// The creation statements Bilibili offers, in its words, as services.go
// lists them.
const statements = ["内容无需标注", "含AI生成内容", "含虚构演绎内容"];

// collectionField names the collection (合集) the material joins on each
// platform that has them, in place of the default from the settings.
function collectionField(m: Material, plats: AccountInfo[]) {
  const withCollections = plats.filter((a) => a.collections);
  if (withCollections.length === 0) return "";
  const defaults = state.settings?.settings.collections ?? {};
  return `<div class="field"><label>合集</label>
    <div class="collections">${withCollections
      .map(
        (a) => `<label><span>${esc(a.name)}</span><input type="text" data-mcoll="${a.id}" value="${esc(m.collections?.[a.id] ?? "")}"
          placeholder="${defaults[a.id] ? `默认：${esc(defaults[a.id]!)}` : "不加入"}" /></label>`,
      )
      .join("")}</div>
    <small class="muted">填平台上已建好的合集名；留空用设置里的默认合集，填「无」不加入</small></div>`;
}

// limitTargets are the platforms a material of m's kind goes to, with the
// lengths each takes: the official account's by how it posts the article.
function limitTargets(m: Material) {
  const images = (document.querySelector<HTMLSelectElement>("#f-weixin")?.value ?? m.weixinType) === "images";
  return state.accounts
    .filter((a) => a.ready && a.kinds.includes(m.kind))
    .flatMap((a) => {
      const weixin = a.id === "weixin-mp";
      const l = a.limits?.[weixin && images ? "images" : m.kind];
      return l ? [{ name: weixin ? (images ? "公众号贴图" : "公众号文章") : a.name, l }] : [];
    });
}

const chars = (s: string) => [...s].length;

// articleText is about the plain text an article posts as on platforms
// that take text, a paragraph per line, without its pictures.
const articleText = (md: string) =>
  md
    .split(/\n\s*\n/)
    .map((p) =>
      p
        .replace(/!\[[^\]]*\]\([^)]*\)/g, "")
        .replace(/^\s*\S+\.(jpe?g|png|webp|gif)\s*$/gim, "")
        .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
        .replace(/^\s{0,3}(#{1,6}\s+|>\s?|[-*+]\s+|\d+\.\s+)/gm, "")
        .replace(/[*_`~]/g, "")
        .trim(),
    )
    .filter(Boolean)
    .join("\n");

// showLimits counts the title and text being edited against what each
// platform takes, which publishing cuts to fit.
function showLimits(m: Material) {
  const targets = limitTargets(m);
  const title = $<HTMLInputElement>("#f-title").value.trim();
  const tags = $<HTMLInputElement>("#f-tags").value.split(/[,，、\s]+/).filter(Boolean);
  const text =
    m.kind === "video" ? $<HTMLTextAreaElement>("#f-desc").value.trim() : articleText($<HTMLTextAreaElement>("#f-body").value);
  const note = (n: number, max: [string, number][], over: [string, number, number][], unit: string) =>
    over.length
      ? `${unit}${n} 字 · <span class="over">超出 ${over
          .map(([name, len, cap]) => `${esc(name)}（${len}/${cap}）`)
          .join("、")}，发布时超出部分会被截掉</span>`
      : `${unit}${n} 字${max.length ? ` · 上限：${max.map(([name, cap]) => `${esc(name)} ${cap}`).join("、")}` : ""}`;

  const titled = targets.filter((t) => t.l.title > 0);
  $("#title-limit").innerHTML = note(
    chars(title),
    titled.map((t) => [t.name, t.l.title]),
    titled.filter((t) => chars(title) > t.l.title).map((t) => [t.name, chars(title), t.l.title]),
    "",
  );

  // Kuaishou and Channels have no title: it goes before the text. Topics follow it.
  const texted = targets
    .filter((t) => t.l.text > 0)
    .map((t) => {
      let len = chars(t.l.title === 0 && !text.includes(title) ? `${title}\n${text}` : text);
      for (const tag of tags.slice(0, t.l.tags ?? 0)) len += chars(tag) + 2;
      return { ...t, len };
    });
  $("#text-limit").innerHTML =
    note(
      chars(text),
      texted.map((t) => [t.name, t.l.text]),
      texted.filter((t) => t.len > t.l.text).map((t) => [t.name, t.len, t.l.text]),
      m.kind === "video" ? "" : "按纯文本约 ",
    ) + (tags.length && texted.some((t) => t.l.tags) ? "（话题也占字数）" : "");
}

function materialTasks(m: Material) {
  const tasks = state.tasks.filter((t) => t.materialId === m.id);
  if (tasks.length === 0) return "";
  return `<div class="section"><h3>发布记录</h3>${taskTable(tasks, false)}</div>`;
}

// The name of a file, without its folder.
const fileName = (p: string) => p.split(/[\\/]/).pop() ?? p;

// articleImages shows an article's pictures and how the official account
// posts it.
function articleImages(m: Material) {
  const images = m.images ?? [];
  const names = images.map(fileName);
  return `
    <div class="field">
      <label>图片（${images.length} 张）</label>
      <div class="row">
        <span class="path" title="${esc(images.join("\n"))}">${
          images.length ? esc(names.slice(0, 6).join("、") + (names.length > 6 ? ` 等 ${names.length} 张` : "")) : "未选择"
        }</span>
        <button data-act="pick-images">${images.length ? "重新选择" : "选择图片"}</button>
        ${images.length ? `<button class="ghost" data-act="clear-images">移除</button>` : ""}
      </div>
      <small class="muted">导入时自动取同名文件夹里的图片（按文件名排序，cover 图片作封面）。小红书发成图文笔记（封面在前，最多 18 张），没有图片时发长文；公众号贴图也用这些图片（最多 20 张）</small>
    </div>
    <div class="field">
      <label>公众号发布形式</label>
      <select id="f-weixin">
        <option value="" ${m.weixinType ? "" : "selected"}>文章（正文含图片，自动设置封面）</option>
        <option value="images" ${m.weixinType === "images" ? "selected" : ""}>贴图（多张图片 + 描述）</option>
      </select>
      <small class="muted">文章的正文图片：在正文里想放图的位置单独一行写图片文件名（如 3.jpg）；正文一张都没写时，自动把除封面外的图片均匀插到段落之间</small>
    </div>`;
}

function readForm(m: Material): Material {
  const tags = $<HTMLInputElement>("#f-tags").value.split(/[,，、\s]+/).filter(Boolean);
  return {
    ...m,
    title: $<HTMLInputElement>("#f-title").value,
    description: m.kind === "video" ? $<HTMLTextAreaElement>("#f-desc").value : m.description,
    body: m.kind === "article" ? $<HTMLTextAreaElement>("#f-body").value : m.body,
    statement: m.kind === "video" ? $<HTMLSelectElement>("#f-statement").value : m.statement,
    weixinType: m.kind === "article" ? $<HTMLSelectElement>("#f-weixin").value : m.weixinType,
    collections: Object.fromEntries(
      [...document.querySelectorAll<HTMLInputElement>("[data-mcoll]")].map((i) => [i.dataset.mcoll!, i.value.trim()]),
    ),
    tags,
  };
}

// formChanged reports whether the editor holds edits to m not yet saved.
function formChanged(m: Material) {
  if (!document.querySelector("#f-title")) return false;
  const f = readForm(m);
  const lines = (s?: string) => (s ?? "").replace(/\r\n?/g, "\n");
  const named = (c?: Record<string, string>) => JSON.stringify(Object.entries(c ?? {}).filter(([, v]) => v).sort());
  return (
    f.title.trim() !== m.title ||
    lines(f.description) !== lines(m.description) ||
    lines(f.body) !== lines(m.body) ||
    (f.statement ?? "") !== (m.statement ?? "") ||
    (f.weixinType ?? "") !== (m.weixinType ?? "") ||
    f.tags.join("\n") !== (m.tags ?? []).join("\n") ||
    named(f.collections) !== named(m.collections)
  );
}

// keepEdits saves what was typed into the editor before the page shows
// something else in its place. False means it could not be saved, and the
// page should stay as it is.
async function keepEdits() {
  const m = state.materials.find((x) => x.id === state.selected);
  if (state.view !== "library" || !m || !formChanged(m)) return true;
  return !!(await saveSelected());
}

function putMaterial(m: Material) {
  const i = state.materials.findIndex((x) => x.id === m.id);
  if (i >= 0) state.materials[i] = m;
}

async function saveSelected(): Promise<Material | undefined> {
  const m = state.materials.find((x) => x.id === state.selected);
  if (!m) return;
  const saved = await attempt(() => Library.update(readForm(m)));
  if (saved) putMaterial(saved);
  return saved;
}

async function importWith(fn: () => Promise<Material[]>, none = "") {
  if (!(await keepEdits())) return;
  const added = await attempt(fn);
  if (added && !added.length && none) toast(none);
  if (!added?.length) return;
  state.materials = await Library.list();
  state.selected = added[0]!.id;
  state.kind = added[0]!.kind as "video" | "article";
  toast(`已导入 ${added.length} 个素材`);
  refreshSchedule();
  if (state.view !== "library") setView("library");
  else render();
}

onFileDrop(({ paths }) => importWith(() => Library.importPaths(paths)));

// ---- Accounts ----

function renderAccounts() {
  const kindText: Record<string, string> = { video: "视频", article: "文章" };
  const grid = `
    <div class="acc-grid">
    ${state.accounts
      .map((a) => {
        const busy = state.loggingIn.has(a.id);
        const checking = state.checking.has(a.id);
        return `<div class="acc">
          <div class="head"><strong>${esc(a.name)} ${a.kinds.map((k) => `<span class="pill">${kindText[k] ?? k}</span>`).join(" ")}</strong>
            ${a.loggedIn ? `<span class="pill ok">已登录</span>` : `<span class="pill warn">未登录</span>`}</div>
          <div class="muted"><small>上次检测：${timeText(a.checkedAt)}</small>
            ${a.ready ? "" : ` · <span class="pill">自动发布开发中</span>`}</div>
          ${a.loginNote ? `<div class="muted"><small>${esc(a.loginNote)}</small></div>` : ""}
          <div class="row">
            <button class="${a.loggedIn ? "" : "primary"}" data-login="${a.id}" ${busy ? "disabled" : ""}>${busy ? "等待扫码登录…" : a.loggedIn ? "重新登录" : "登录"}</button>
            <button class="ghost" data-check="${a.id}" ${checking ? "disabled" : ""}>${checking ? "检测中…" : "检测"}</button>
          </div></div>`;
      })
      .join("")}</div>`;
  view.innerHTML = `
    <div class="view-head"><h2>账号</h2>
      <div class="row">
        <button data-act="check-all" ${state.checking.size ? "disabled" : ""}>${state.checking.size ? "检测中…" : "一键检测"}</button>
        <button data-act="login-all">一键重新登录</button>
        <button class="ghost" data-act="close-browser">关闭自动化浏览器</button>
      </div></div>
    <div class="view-body">
      <div class="section sync">
        <h3>从日常 Chrome 同步登录（免扫码）</h3>
        <div class="muted">你平时用的 Chrome 里已经登录过的平台，可以用配套扩展一键把登录状态复制过来：</div>
        <ol>
          <li>在 Chrome 地址栏打开 <code>chrome://extensions</code>，打开右上角「开发者模式」</li>
          <li>点「加载已解压的扩展程序」，选择扩展目录 <span class="path">${esc(state.sync?.extensionDir ?? "")}</span></li>
          <li>保持本应用打开，点 Chrome 工具栏里的「Publisher 登录同步」扩展 →「一键同步全部」，或勾选平台后「同步所选」（已装旧版扩展的，在扩展页点它的刷新按钮更新）</li>
        </ol>
        ${state.sync?.error ? `<div class="err">${esc(state.sync.error)}</div>` : ""}
        <div class="row">
          <button data-act="open-ext">打开扩展目录</button>
          <button class="ghost" data-act="copy-ext-url">复制 chrome://extensions</button>
        </div>
      </div>
      <div class="notice">也可以点平台的「登录」，在本应用的<strong>独立 Chrome 窗口</strong>里扫码登录。登录状态保存在本应用的浏览器配置里，一般长期有效；发布前若提示未登录，回到这里重新同步或登录。</div>
      ${grid}
    </div>`;
}

function putAccount(a: AccountInfo) {
  const i = state.accounts.findIndex((x) => x.id === a.id);
  if (i >= 0) state.accounts[i] = a;
}

async function login(id: string) {
  if (state.loggingIn.has(id)) return;
  state.loggingIn.add(id);
  render();
  const a = await attempt(() => Accounts.login(id));
  state.loggingIn.delete(id);
  if (a) {
    putAccount(a);
    toast(`${a.name} 登录成功`);
  }
  render();
}

async function check(id: string) {
  state.checking.add(id);
  render();
  const a = await attempt(() => Accounts.check(id));
  state.checking.delete(id);
  if (a) putAccount(a);
  render();
  return a;
}

// Checks every platform, one at a time as they share the browser.
async function checkAll() {
  const ids = state.accounts.map((a) => a.id);
  ids.forEach((id) => state.checking.add(id));
  const out: AccountInfo[] = [];
  for (const id of ids) {
    const a = await check(id);
    if (a) out.push(a);
  }
  const offline = out.filter((a) => !a.loggedIn).map((a) => a.name);
  toast(offline.length ? `检测完成，未登录：${offline.join("、")}` : `检测完成，${out.length} 个平台都已登录`, offline.length > 0);
}

// Opens the login page of every platform that is logged out, or of every
// one when all are logged in, each in its own tab.
async function loginAll() {
  let ids = state.accounts.filter((a) => !a.loggedIn).map((a) => a.id);
  if (ids.length === 0) {
    if (!(await ask("所有平台都显示已登录，要全部重新登录吗？", { title: "全部重新登录", ok: "全部重新登录" }))) return;
    ids = state.accounts.map((a) => a.id);
  } else if (
    !(await ask(`在自动化浏览器里打开 ${ids.map(platformName).join("、")} 的登录页，请逐个扫码登录。`, { title: "打开登录页", ok: "打开" }))
  )
    return;
  ids.forEach((id) => void login(id));
}

// ---- Tasks ----

function taskTable(tasks: Task[], showMaterial: boolean) {
  return `<table><thead><tr>
    ${showMaterial ? "<th>素材</th>" : ""}<th>平台</th><th>状态</th><th>进度</th><th>更新时间</th><th></th></tr></thead>
    <tbody>${tasks
      .map(
        (t) => `<tr>
        ${showMaterial ? `<td>${esc(taskTitle(t))}</td>` : ""}
        <td>${esc(platformName(t.platform))}<div class="muted"><small>${t.mode === "auto" ? "全自动" : "人工确认"}</small></div></td>
        <td>${statusPill(t.status)}</td>
        <td>${esc(t.step)}${t.error ? `<div class="err">${esc(t.error)}</div>` : ""}</td>
        <td class="muted"><small>${timeText(t.updatedAt)}</small></td>
        <td class="actions">${taskActions(t)}</td></tr>`,
      )
      .join("")}</tbody></table>`;
}

function taskActions(t: Task) {
  const b: string[] = [];
  if (t.status === "running" || t.status === "pending") b.push(`<button data-cancel="${t.id}">取消</button>`);
  if (t.status === "review") b.push(`<button class="primary" data-done="${t.id}">我已提交</button>`);
  const gone = !state.materials.some((m) => m.id === t.materialId);
  if (t.shot) b.push(`<button data-shot="${t.id}" title="失败时页面的样子">截图</button>`);
  if (t.status === "failed") b.push(`<button data-done="${t.id}" title="平台上其实已经发出去了">标为成功</button>`);
  if (!gone && (t.status === "failed" || t.status === "canceled" || t.status === "review"))
    b.push(`<button data-retry="${t.id}">重试</button>`);
  if (!busy(t)) b.push(`<button class="danger ghost" data-del-task="${t.id}">删除</button>`);
  return b.join(" ");
}

// retryable reports whether a failed task is worth another try: its
// material is still there and has not gone out to the platform since.
const retryable = (t: Task) => {
  const m = state.materials.find((x) => x.id === t.materialId);
  return t.status === "failed" && !!m && !(t.platform in (m.published ?? {}));
};

// taskKind is the kind of material a task publishes.
const taskKind = (t: Task) => state.materials.find((m) => m.id === t.materialId)?.kind ?? t.kind ?? "video";

// renderTasks lists the tasks of videos or of articles, as the library
// does its materials.
function renderTasks() {
  const count = (k: string) => state.tasks.filter((t) => taskKind(t) === k).length;
  const tab = (k: string, label: string) =>
    `<button class="${state.kind === k ? "active" : ""}" data-act="kind" data-kind="${k}">${label} <small>${count(k)}</small></button>`;
  const tasks = state.tasks.filter((t) => taskKind(t) === state.kind);
  const failed = tasks.filter(retryable).length;
  view.innerHTML = `
    <div class="view-head"><h2>任务</h2>
      <div class="kind-tabs inline">${tab("video", "视频")}${tab("article", "文章")}</div>
      ${failed ? `<button data-act="retry-failed" title="把这一页失败的任务重新排队；已经在该平台发布成功的不会重发">重试失败的（${failed}）</button>` : ""}
      <button data-act="clear-tasks" title="清除已发布、失败和已取消的任务记录；待确认的会保留">清除已结束</button></div>
    <div class="view-body">${
      tasks.length === 0
        ? `<div class="empty"><strong>还没有${state.kind === "video" ? "视频" : "文章"}任务</strong><span>在素材库选好平台后点「开始发布」</span></div>`
        : taskTable(tasks, true)
    }</div>`;
}

events.taskUpdated.on((t) => {
  const i = state.tasks.findIndex((x) => x.id === t.id);
  // Most updates are a new step of the same task, which the pending list
  // of the schedule does not show.
  const moved = state.tasks[i]?.status !== t.status;
  if (i >= 0) state.tasks[i] = t;
  else state.tasks.unshift(t);
  if (t.status === "review") toast(`${platformName(t.platform)}：已填好，请到浏览器确认提交`);
  if (t.status === "failed") toast(`${platformName(t.platform)} 发布失败：${t.error}`, true);
  if (t.status === "success") {
    toast(`${platformName(t.platform)} 发布成功`);
    // The store marks the material published; mirror it.
    const m = state.materials.find((x) => x.id === t.materialId);
    if (m && !m.published?.[t.platform]) m.published = { ...m.published, [t.platform]: t.updatedAt };
  }
  // Re-rendering the library would drop unsaved edits, so only the task
  // parts follow along there.
  if (state.view === "library") refreshLibraryTasks();
  else if (state.view === "tasks") render();
  else if (state.view === "schedule" && moved) refreshSchedule();
  renderBadge();
});

events.cookiesSynced.on((r) => {
  if (r.platforms.length === 0) return toast("你的 Chrome 里没有找到这些平台的登录信息", true);
  r.platforms.forEach((p) => p.status !== "checking" && putAccount({ ...state.accounts.find((a) => a.id === p.id)!, loggedIn: p.status === "loggedIn" }));
  const label: Record<string, string> = { loggedIn: "已登录", loggedOut: "登录已失效", checking: "检测中" };
  toast(`已从 Chrome 同步：${r.platforms.map((p) => `${p.name}（${label[p.status] ?? p.status}）`).join("、")}`);
  if (state.view === "accounts") render();
});

events.accountUpdated.on((a) => {
  putAccount(a);
  if (state.view === "accounts") render();
});

function refreshLibraryTasks() {
  const list = document.querySelector(".mat-list");
  if (list) list.innerHTML = shownMaterials().map(materialItem).join("");
  const m = state.materials.find((x) => x.id === state.selected);
  const box = document.querySelector("#mat-tasks");
  if (m && box) box.innerHTML = materialTasks(m);
}

// ---- Schedule ----

function renderSchedule() {
  const info = state.schedule;
  if (!info) return;
  const sc = info.schedule;
  // Drawn again after every run and save; the pending list keeps its
  // place while it shows the same kind (render keeps the page's).
  const old = document.querySelector<HTMLElement>("ol.pending");
  const top = old?.dataset.kind === state.kind ? old.scrollTop : 0;
  const kinds: [string, string][] = [
    ["video", "视频发布到"],
    ["article", "文章发布到"],
  ];
  const notes = state.accounts
    .filter((a) => kinds.some(([k]) => sc.platforms?.[k]?.includes(a.id)))
    .flatMap((a) => [a.autoNote, a.loginNote].filter(Boolean).map((n) => `<div class="muted"><small>${esc(a.name)}：${esc(n!)}</small></div>`))
    .join("");
  view.innerHTML = `
    <div class="view-head"><h2>定时发布</h2></div>
    <div class="view-body">
      <div class="notice">到点后，把素材库里<strong>还没发布过</strong>的素材按导入顺序（先导入的先发）<strong>全自动</strong>发布。视频和文章各自按自己的时间发。每个素材只会被定时发一次，失败的请在任务页重试。同步文件夹里的新文件要先同步进素材库，或在设置里打开「定时发布前自动同步文件夹」。需要本应用保持打开（可以最小化）；电脑睡眠错过 10 分钟以上的那次会跳过。</div>
      <div class="section" id="sched-form">
        <label class="switch"><input type="checkbox" id="s-enabled" ${sc.enabled ? "checked" : ""} /> 启用定时发布</label>
        ${kinds
          .map(
            ([k, label]) => `<div class="sched-kind"><h3>${kindLabel(k)}</h3>
              <div class="field"><label>每天发布时间</label>
                <input type="text" data-times="${k}" value="${esc((sc.kindTimes?.[k] ?? []).join("，"))}" placeholder="如：12:00，20:00" />
                <small class="muted">24 小时制，多个时间用逗号（中英文都行）、顿号或空格隔开，例如：9:30，12:00，20:00</small></div>
              <div class="field"><label>每次发布</label>
                <div class="row"><input type="number" data-per="${k}" min="1" max="20" value="${sc.kindPerRun?.[k] || 1}" class="num" /> <span>个${kindLabel(k)}（每个发到下面勾选的全部平台）</span></div></div>
              <div class="field"><label>${label}</label><div class="platforms">${state.accounts
                .filter((a) => a.kinds.includes(k) && a.ready)
                .map(
                  (a) => `<label class="plat"><input type="checkbox" data-kind="${k}" value="${a.id}" ${sc.platforms?.[k]?.includes(a.id) ? "checked" : ""} />
                  <span class="name">${esc(a.name)}</span>${a.loggedIn ? `<span class="pill ok">已登录</span>` : `<span class="pill warn">未登录</span>`}</label>`,
                )
                .join("")}</div>
                <small class="muted">都不勾就不定时发${kindLabel(k)}</small></div></div>`,
          )
          .join("")}
        <div class="field"><label>B站创作声明（素材里没选时使用）</label>
          <select id="s-statement">${["", ...statements]
            .map((o) => `<option value="${o}" ${o === (sc.statement ?? "") ? "selected" : ""}>${o || "不设置（没选声明的视频不发 B站）"}</option>`)
            .join("")}</select></div>
        ${notes}
        <div class="row"><button class="primary" data-act="save-schedule">保存设置</button>
          <button data-act="run-schedule" ${info.pending.length ? "" : "disabled"}>立即发布下一批</button></div>
      </div>
      <div class="section">
        <h3>运行情况</h3>
        ${kinds.map(([k]) => `<div>下次发布${kindLabel(k)}：${nextText(info, k)}</div>`).join("")}
        <div class="muted"><small>上次运行：${sc.lastResult ? `${timeText(sc.lastRun)}，${esc(sc.lastResult)}` : "还没有运行过"}</small></div>
      </div>
      <div class="section" id="pending">${pendingSection(info)}</div>
    </div>`;
  const list = view.querySelector<HTMLElement>("ol.pending");
  if (list) list.scrollTop = top;
}

function kindLabel(k: string) {
  return k === "video" ? "视频" : "文章";
}

function nextText(info: ScheduleInfo, k: string) {
  if (!info.schedule.enabled) return "未启用";
  const t = info.next?.[k];
  return t && !t.startsWith("0001") ? timeText(t) : "不发";
}

// pendingSection lists the materials waiting for the schedule, one kind at
// a time as the library does. The next run of a kind takes its first ones.
function pendingSection(info: ScheduleInfo) {
  const per = info.schedule.kindPerRun?.[state.kind] || 1;
  const count = (k: string) => info.pending.filter((p) => p.kind === k).length;
  const list = info.pending.filter((p) => p.kind === state.kind).map((p, i) => ({ ...p, next: i < per }));
  const tab = (k: string, label: string) =>
    `<button class="${state.kind === k ? "active" : ""}" data-act="kind" data-kind="${k}">${label} <small>${count(k)}</small></button>`;
  return `
    <div class="row"><h3>待发布素材（${info.pending.length}）</h3>
      <div class="kind-tabs inline">${tab("video", "视频")}${tab("article", "文章")}</div></div>
    ${
      list.length === 0
        ? `<div class="muted">没有待发布的${state.kind === "video" ? "视频" : "文章"}。导入新素材，或在上面勾选要发布的平台。</div>`
        : `<ol class="pending scroll" data-kind="${state.kind}">${list
            .map(
              (p) => `<li><span class="kind ${p.kind}">${p.kind === "video" ? "视频" : "文章"}</span>
                <span class="t">${esc(p.title || "（无标题）")}</span>
                <span class="muted"><small>${p.platforms.map(platformName).join("、")}</small></span>
                ${p.next ? `<span class="pill info">下一批</span>` : ""}</li>`,
            )
            .join("")}</ol>`
    }`;
}

function readSchedule() {
  const sc = state.schedule!.schedule;
  const platforms: Record<string, string[]> = {};
  document.querySelectorAll<HTMLInputElement>("#sched-form input[data-kind]:checked").forEach((cb) => {
    (platforms[cb.dataset.kind!] ??= []).push(cb.value);
  });
  const kindTimes: Record<string, string[]> = {};
  document.querySelectorAll<HTMLInputElement>("#sched-form input[data-times]").forEach((el) => {
    kindTimes[el.dataset.times!] = el.value.split(/[,，、\s]+/).filter(Boolean);
  });
  const kindPerRun: Record<string, number> = {};
  document.querySelectorAll<HTMLInputElement>("#sched-form input[data-per]").forEach((el) => {
    kindPerRun[el.dataset.per!] = Number(el.value) || 0;
  });
  return {
    ...sc,
    enabled: $<HTMLInputElement>("#s-enabled").checked,
    kindTimes,
    kindPerRun,
    platforms,
    statement: $<HTMLSelectElement>("#s-statement").value,
  };
}

async function refreshFolders() {
  state.folders = await Library.folders();
  if (state.view === "library") showFolders();
}

// Files may have been added to or deleted from the sync folders while the
// window was in the background.
window.addEventListener("focus", () => state.view === "library" && void refreshFolders());

async function refreshSchedule() {
  state.schedule = await Scheduler.get();
  if (state.view === "schedule") render();
  else renderBadge();
}

events.scheduleRan.on(async (sc) => {
  toast(`定时发布：${sc.lastResult}`);
  state.materials = await Library.list();
  if (state.view === "library") refreshLibraryTasks();
  refreshSchedule();
});

// ---- Settings ----

const accents: [string, string][] = [
  ["", "樱粉"],
  ["#3b82f6", "天蓝"],
  ["#10b981", "青绿"],
  ["#8b5cf6", "紫罗兰"],
  ["#f97316", "橙色"],
  ["#475569", "石墨"],
];

// inkOn is the text color that reads on the color hex: dark on light ones.
function inkOn(hex: string) {
  const lin = (i: number) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * lin(1) + 0.7152 * lin(3) + 0.0722 * lin(5) > 0.45 ? "#111318" : "#ffffff";
}

// applyLook gives the page the chosen appearance: light or dark (also
// where the system's override does not reach the web view), the accent,
// the style, the corners and the background. The zoom is the web view's
// (Settings.Save).
function applyLook(info: SettingsInfo) {
  const root = document.documentElement;
  const st = info.settings;
  const ui = st.ui ?? {};
  root.style.colorScheme = st.theme ?? "";
  const set = (name: string, value?: string) => (value ? root.style.setProperty(name, value) : root.style.removeProperty(name));
  set("--accent", ui.accent);
  set("--accent-ink", ui.accent && inkOn(ui.accent));
  set("--bg-image", info.backgroundURL && `url("${info.backgroundURL}")`);
  // The panels' transparency counts only over a background image.
  const panel = info.backgroundURL ? (ui.panel ?? 0) : 0;
  set("--panel-a", panel ? `${100 - panel}%` : undefined);
  for (const [k, v] of Object.entries({ style: ui.style, corner: ui.corner, bg: ui.background, clear: panel ? "on" : "" })) {
    if (v) root.dataset[k] = v;
    else delete root.dataset[k];
  }
  root.style.setProperty("--veil", String((ui.veil ?? 0) / 100));
  root.style.setProperty("--blur", `${ui.blur ?? 0}px`);
}

function renderSettings() {
  const info = state.settings;
  if (!info) return;
  const st = info.settings;
  const themes: [string, string][] = [
    ["", "跟随系统"],
    ["light", "浅色"],
    ["dark", "深色"],
  ];
  const ui = st.ui ?? {};
  const accent = ui.accent ?? "";
  const custom = accent !== "" && !accents.some(([c]) => c === accent);
  const styles: [string, string][] = [
    ["", "标准"],
    ["glass", "毛玻璃"],
    ["soft", "柔和"],
  ];
  const corners: [string, string][] = [
    ["small", "小"],
    ["", "中"],
    ["large", "大"],
  ];
  const zooms: [string, string][] = ["90", "100", "110", "120"].map((z) => [z, `${z}%`]);
  const segmented = (key: string, options: [string, string][], current: string) =>
    `<div class="segmented">${options
      .map(([v, label]) => `<button data-${key}="${v}" class="${current === v ? "active" : ""}">${label}</button>`)
      .join("")}</div>`;
  const range = (key: string, title: string, note: string, value: number, lo: number, hi: number, unit: string) => `<div class="setting">
      <span class="label"><strong>${title}</strong><small>${note}</small></span>
      <span class="range"><input type="range" data-ui="${key}" min="${lo}" max="${hi}" value="${value}" /><output>${value}${unit}</output></span></div>`;
  const check = (id: string, on: boolean, title: string, note: string) => `<label class="setting">
      <span class="label"><strong>${title}</strong><small>${note}</small></span>
      <input type="checkbox" data-setting="${id}" ${on ? "checked" : ""} /></label>`;
  view.innerHTML = `
    <div class="view-head"><h2>设置</h2></div>
    <div class="view-body">
      <div class="section">
        <h3>常规</h3>
        ${check("openAtLogin", info.openAtLogin, "开机自启", "登录 Windows 后自动启动，并直接隐藏到托盘，定时发布照常进行")}
        ${check("tray", !st.quitOnClose, "关闭窗口时隐藏到托盘", "关掉后点窗口的关闭按钮会直接退出应用，定时发布也随之停止")}
      </div>
      <div class="section">
        <h3>外观</h3>
        <div class="setting">
          <span class="label"><strong>主题</strong><small>白天/夜晚模式，跟随系统时随 Windows 的深浅色设置切换</small></span>
          ${segmented("theme", themes, st.theme ?? "")}
        </div>
        <div class="setting">
          <span class="label"><strong>主题色</strong><small>按钮、选中项和标记的颜色；最后一个可以自己调</small></span>
          <div class="swatches">${accents
            .map(([c, name]) => `<button class="swatch ${accent === c ? "active" : ""}" style="--c:${c || "#fb7299"}" data-accent="${c}" title="${name}"></button>`)
            .join("")}<label class="swatch swatch-custom ${custom ? "active" : ""}" style="--c:${custom ? accent : "transparent"}" title="自定义颜色"><input type="color" data-ui="accent" value="${accent || "#fb7299"}" /></label></div>
        </div>
        <div class="setting">
          <span class="label"><strong>风格</strong><small>毛玻璃：面板半透明，配背景图片效果最好；柔和：用阴影代替边框</small></span>
          ${segmented("style", styles, ui.style ?? "")}
        </div>
        <div class="setting">
          <span class="label"><strong>圆角</strong><small>按钮、卡片和输入框的圆角大小</small></span>
          ${segmented("corner", corners, ui.corner ?? "")}
        </div>
        <div class="setting">
          <span class="label"><strong>界面缩放</strong><small>文字和控件整体放大或缩小</small></span>
          ${segmented("zoom", zooms, String(ui.zoom || 100))}
        </div>
        <div class="setting">
          <span class="label"><strong>背景图片</strong><small>${ui.background ? "图片已复制到数据目录，原文件移走也不影响" : "选一张图片铺满窗口背景"}</small></span>
          <span class="row-buttons">${ui.background ? `<span class="bg-preview" style="background-image:url('${esc(info.backgroundURL ?? "")}')"></span>` : ""}
            <button data-act="pick-bg">${ui.background ? "更换" : "选择图片"}</button>${ui.background ? `<button class="ghost" data-act="clear-bg">移除</button>` : ""}</span>
        </div>
        ${
          ui.background
            ? `${range("veil", "图片透明度", "越高图片越淡，文字越清楚", ui.veil ?? 0, 0, 95, "%")}
               ${range("panel", "面板透明度", "卡片、侧栏等面板透出背景的程度；太高时文字可能看不清", ui.panel ?? 0, 0, 90, "%")}
               ${range("blur", "模糊", "把图片虚化，减少干扰", ui.blur ?? 0, 0, 30, "px")}`
            : ""
        }
        <div class="setting">
          <span class="label"><strong>恢复默认外观</strong><small>主题色、风格、圆角、缩放和背景都回到默认，深浅色不变</small></span>
          <button data-act="reset-look">恢复默认</button>
        </div>
      </div>
      <div class="section">
        <h3>发布</h3>
        ${check("autoSync", !!st.autoSync, "定时发布前自动同步文件夹", "到点时先导入同步文件夹里新增的视频和文章，再按顺序发布；5 分钟内还在改动的文件留到下一次")}
        ${check("closeBrowser", !st.keepBrowser, "全自动任务跑完后关闭浏览器", "仅限本轮任务才打开的浏览器；有确认模式任务、或你原本就开着浏览器时不会关")}
        ${state.accounts
          .filter((a) => a.collections)
          .map(
            (a) => `<label class="setting">
          <span class="label"><strong>${esc(a.name)}默认合集</strong><small>填平台上已建好的合集名，发布时自动加入；留空不加入，素材里可单独改</small></span>
          <input type="text" class="setting-text" data-collection="${a.id}" value="${esc(st.collections?.[a.id] ?? "")}" placeholder="不加入" /></label>`,
          )
          .join("")}
        ${check("original", !!st.original, "小红书声明原创", "发布笔记时打开「原创声明」并同意须知；视频短于 10 秒等平台不支持的笔记会跳过")}
      </div>
      <div class="section">
        <h3>更新</h3>
        <div class="setting" id="update-row">${updateRow()}</div>
        ${check("updateCheck", !st.noUpdateCheck, "自动检查更新", "启动一分钟后和之后每六小时看一次网盘（Flow 每次构建都会传上去），有新版本时侧栏「设置」上显示「新」")}
      </div>
      <div class="section">
        <h3>数据</h3>
        <div class="setting">
          <span class="label"><strong>数据目录</strong><small class="path">${esc(info.dataDir)}</small></span>
          <button data-act="open-data-dir">打开</button>
        </div>
        <div class="muted"><small>素材、任务、设置都在 data.json 里；chrome-profile 是自动化浏览器的登录信息。</small></div>
      </div>
    </div>`;
}

const mb = (n: number) => `${(n / 1048576).toFixed(1)} MB`;
const when = (ms: number) => new Date(ms).toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });

// updateRow is the version line of the settings: what the last check of
// the cloud drive found, and the buttons to check and to update.
function updateRow() {
  const u = state.update;
  const current = u?.current || state.settings?.version || "";
  let note = "Flow 每次构建都会把新版本传到网盘";
  if (u?.installing) note = `正在下载 ${u.latest}：${mb(u.done)}${u.size ? ` / ${mb(u.size)}` : ""}…`;
  else if (u?.checking) note = "正在检查网盘…";
  else if (u?.error) note = `<span class="err">${esc(u.error)}</span>`;
  else if (u?.newer) note = `有新版本 ${esc(u.latest ?? "")}（${u.published ? `${when(u.published)} 发布，` : ""}${mb(u.size ?? 0)}）`;
  else if (u?.checked) note = `已是最新（网盘最新 ${esc(u.latest ?? "")}，${when(u.checked)} 检查）`;
  const busy = u?.checking || u?.installing;
  if (u?.local && !u.installing && !u.checking && !u.error) note = `本地构建，不自动检查；${note}`;
  return `<span class="label"><strong>版本 ${esc(current || "开发版")}${u?.local ? ` <span class="pill">本地构建</span>` : ""}</strong><small>${note}</small></span>
    <span class="row-buttons"><button data-act="check-update" ${busy ? "disabled" : ""}>检查更新</button>${
      u?.newer ? `<button class="primary" data-act="install-update" ${busy ? "disabled" : ""}>${u.installing ? "正在更新…" : `更新到 ${esc(u.latest ?? "")}`}</button>` : ""
    }</span>`;
}

// Only the version line is drawn again: the settings' text boxes keep what
// is being typed.
events.updateChanged.on((u) => {
  state.update = u;
  renderBadge();
  const row = document.getElementById("update-row");
  if (row) row.innerHTML = updateRow();
});

async function saveSettings(change: (s: SettingsInfo) => void) {
  const next = structuredClone(state.settings!);
  change(next);
  const info = await attempt(() => Settings.save(next.settings, next.openAtLogin));
  if (info) {
    state.settings = info;
    applyLook(info);
  }
  render();
}

// The sliders and the color picker show their effect while they move,
// and save once let go.
view.addEventListener("input", (e) => {
  const el = e.target as HTMLInputElement;
  const key = el.dataset.ui;
  if (!key || !state.settings) return;
  const info = structuredClone(state.settings);
  info.settings.ui = { ...info.settings.ui, [key]: key === "accent" ? el.value : Number(el.value) };
  applyLook(info);
  const out = el.nextElementSibling;
  if (out?.tagName === "OUTPUT") out.textContent = `${el.value}${key === "blur" ? "px" : "%"}`;
});

const saveUI = (change: Partial<NonNullable<SettingsInfo["settings"]["ui"]>>) =>
  saveSettings((s) => (s.settings.ui = { ...s.settings.ui, ...change }));

view.addEventListener("change", (e) => {
  const cb = e.target as HTMLInputElement;
  const key = cb.dataset.ui;
  if (key) return void saveUI({ [key]: key === "accent" ? cb.value : Number(cb.value) });
  const platform = cb.dataset.collection;
  if (platform) {
    return void saveSettings((s) => (s.settings.collections = { ...s.settings.collections, [platform]: cb.value.trim() }));
  }
  const id = cb.dataset.setting;
  if (!id) return;
  void saveSettings((s) => {
    if (id === "openAtLogin") s.openAtLogin = cb.checked;
    else if (id === "tray") s.settings.quitOnClose = !cb.checked;
    else if (id === "closeBrowser") s.settings.keepBrowser = !cb.checked;
    else if (id === "original") s.settings.original = cb.checked;
    else if (id === "autoSync") s.settings.autoSync = cb.checked;
    else if (id === "updateCheck") s.settings.noUpdateCheck = !cb.checked;
  });
});

// ---- Actions ----

view.addEventListener("click", async (e) => {
  const target = (e.target as HTMLElement).closest<HTMLButtonElement>("button");
  if (!target) return;
  const d = target.dataset;

  // Most buttons of the library draw it again, and with it the editor.
  // Those that keep it, or save it themselves, are left to.
  const keeps = ["save", "publish", "reveal", "delete", "pick-images", "clear-images", "pick-cover", "clear-cover"];
  if (state.view === "library" && !keeps.includes(d.act ?? "") && !(await keepEdits())) return;

  if (d.select) {
    state.selected = d.select;
    return render();
  }
  if (d.theme !== undefined) return saveSettings((s) => (s.settings.theme = d.theme));
  if (d.accent !== undefined) return saveUI({ accent: d.accent });
  if (d.style !== undefined) return saveUI({ style: d.style });
  if (d.corner !== undefined) return saveUI({ corner: d.corner });
  if (d.zoom !== undefined) return saveUI({ zoom: d.zoom === "100" ? 0 : Number(d.zoom) });
  if (d.login) return login(d.login);
  if (d.check) {
    const a = await check(d.check);
    if (a) toast(`${a.name}：${a.loggedIn ? "已登录" : "未登录"}`);
    return;
  }
  if (d.cancel) return void attempt(() => Publisher.cancel(d.cancel!));
  if (d.retry) return void attempt(() => Publisher.retry(d.retry!));
  if (d.shot) return void attempt(() => Publisher.openShot(d.shot!));
  if (d.unpub) {
    e.preventDefault();
    const m = state.materials.find((x) => x.id === state.selected);
    if (!m) return;
    const name = platformName(d.unpub);
    if (
      !(await ask(`只在${name}上其实没发出去时这样做（如成功被误判）：素材在${name}显示为未发布，那里的成功任务改成失败，可以重试。`, {
        title: `撤销${name}的「已发布」？`,
        ok: "撤销",
        danger: true,
      }))
    )
      return;
    const saved = await attempt(() => Library.unpublish(m.id, d.unpub!));
    if (!saved) return;
    putMaterial(saved);
    state.tasks = await Publisher.tasks();
    return render();
  }
  if (d.done) {
    const failed = state.tasks.find((t) => t.id === d.done)?.status === "failed";
    if (
      failed &&
      !(await ask("只在平台上确实已经发出去时这样做：素材会显示为已发布，「重试失败的」也不会再发它。", {
        title: "把这条失败的任务标为成功？",
        ok: "标为成功",
      }))
    )
      return;
    return void attempt(() => Publisher.markDone(d.done!));
  }
  if (d.delTask) {
    const waiting = state.tasks.find((t) => t.id === d.delTask)?.status === "review";
    const sure = waiting
      ? await ask(
          "这条任务还在等你确认提交。如果已经在浏览器里提交了，请先点「我已提交」；直接删除的话，素材会算作没发过，定时发布可能再发一次。",
          { title: "删除待确认的任务", ok: "仍要删除", danger: true },
        )
      : await ask("已发布的状态会保留。", { title: "删除这条任务记录？", ok: "删除", danger: true });
    if (!sure) return;
    if (!(await attempt(() => Publisher.delete(d.delTask!).then(() => true)))) return;
    state.tasks = state.tasks.filter((t) => t.id !== d.delTask);
    if (state.view === "library") refreshLibraryTasks();
    else render();
    return renderBadge();
  }

  const sel = state.materials.find((m) => m.id === state.selected);
  switch (d.act) {
    case "kind":
      state.kind = d.kind as "video" | "article";
      // Re-rendering the whole schedule view would drop unsaved edits.
      if (state.view === "schedule") return void ($("#pending").innerHTML = pendingSection(state.schedule!));
      return render();
    case "import-video":
      return importWith(() => Library.importVideos());
    case "import-article":
      return importWith(() => Library.importArticles());
    case "toggle-folders":
      state.showFolders = !state.showFolders;
      showFolders();
      return refreshFolders();
    case "add-folder": {
      const before = state.folders.length;
      const fs = await attempt(() => Library.addFolder());
      if (!fs) return;
      state.folders = fs;
      render();
      const f = fs.length > before ? fs.at(-1)! : undefined;
      if (!f?.new || !(await ask(`${f.dir} 里有 ${f.new} 个新文件，现在导入吗？`, { title: "导入新文件", ok: "导入" }))) return;
      await importWith(() => Library.syncFolders(), "没有新文件");
      return refreshFolders();
    }
    case "remove-folder":
      if (!(await ask(`不再同步 ${d.dir}，已导入的素材会保留。`, { title: "移除同步文件夹", ok: "移除", danger: true }))) return;
      state.folders = (await attempt(() => Library.removeFolder(d.dir!))) ?? state.folders;
      return render();
    case "sync-folders":
      target.disabled = true;
      await importWith(() => Library.syncFolders(), "没有新文件");
      return refreshFolders();
    case "save":
      if (await saveSelected()) toast("已保存");
      return render();
    case "delete":
      if (!sel || !(await ask(`「${sel.title}」会从素材库移除，不会删除文件。`, { title: "删除素材", ok: "删除", danger: true }))) return;
      if (!(await attempt(() => Library.delete(sel.id).then(() => true)))) return;
      state.materials = state.materials.filter((m) => m.id !== sel.id);
      state.selected = "";
      refreshSchedule();
      return render();
    case "delete-published": {
      const done = state.materials.filter(published);
      if (done.length === 0) return toast("没有全部发布成功的素材", true);
      if (
        !(await ask(`删除 ${done.length} 个已全部发布成功的素材？不会删除文件；任务记录保留，可在任务页「清除已结束」。`, {
          title: "清理已发布",
          ok: "删除",
          danger: true,
          detail: done.map((m) => m.title),
        }))
      )
        return;
      const ids = await attempt(() => Library.deletePublished());
      if (!ids) return;
      state.materials = state.materials.filter((m) => !ids.includes(m.id));
      toast(`已删除 ${ids.length} 个素材`);
      refreshSchedule();
      return render();
    }
    case "save-schedule": {
      const info = await attempt(() => Scheduler.save(readSchedule()));
      if (!info) return;
      state.schedule = info;
      toast(info.schedule.enabled ? "已保存，定时发布已启用" : "已保存，定时发布未启用");
      return render();
    }
    case "run-schedule": {
      if (!(await ask("现在就全自动发布下一批素材？", { title: "立即发布下一批", ok: "发布" }))) return;
      const info = await attempt(() => Scheduler.runNow());
      if (info) state.schedule = info;
      return render();
    }
    case "reveal":
      return void (sel && attempt(() => Library.reveal(sel.id)));
    case "pick-images":
    case "clear-images": {
      if (!sel) return;
      await saveSelected();
      const m = await attempt(() => (d.act === "pick-images" ? Library.pickImages(sel.id) : Library.clearImages(sel.id)));
      if (m) putMaterial(m);
      return render();
    }
    case "pick-cover":
    case "clear-cover": {
      if (!sel) return;
      await saveSelected();
      const m = await attempt(() => (d.act === "pick-cover" ? Library.pickCover(sel.id) : Library.clearCover(sel.id)));
      if (m) putMaterial(m);
      return render();
    }
    case "publish": {
      if (!sel) return;
      const picks = state.picks[sel.id] ?? [];
      if (picks.length === 0) return toast("请先勾选要发布的平台", true);
      const m = await saveSelected();
      if (!m) return;
      if (!m.title.trim()) return toast("标题不能为空", true);
      const offline = picks.filter((id) => !state.accounts.find((a) => a.id === id)?.loggedIn);
      if (offline.length && !(await ask(`${offline.map(platformName).join("、")} 可能未登录，仍要继续吗？`, { title: "平台可能未登录", ok: "继续" })))
        return;
      const tasks = await attempt(() => Publisher.publish(m.id, picks, state.mode));
      if (tasks) toast(`已加入 ${tasks.length} 个发布任务`);
      return;
    }
    case "open-ext":
      return void attempt(() => Accounts.openExtensionDir());
    case "copy-ext-url":
      await navigator.clipboard.writeText("chrome://extensions");
      return toast("已复制，粘贴到 Chrome 地址栏打开");
    case "open-data-dir":
      return void attempt(() => Settings.openDataDir());
    case "pick-bg":
    case "clear-bg": {
      const info = await attempt(() => (d.act === "pick-bg" ? Settings.pickBackground() : Settings.clearBackground()));
      if (info) {
        state.settings = info;
        applyLook(info);
        render();
      }
      return;
    }
    case "reset-look":
      if (!(await ask("主题色、风格、圆角、缩放和背景图片都恢复默认，深浅色不变。", { title: "恢复默认外观", ok: "恢复默认" }))) return;
      await attempt(() => Settings.clearBackground());
      return saveSettings((s) => (s.settings.ui = {}));
    case "check-update":
      return void Updates.check().catch(() => {}); // the row shows the error
    case "install-update": {
      const u = state.update;
      if (!u) return;
      const how = u.portable ? "覆盖当前文件夹里的程序" : "静默运行新安装包";
      const notes = [`下载 ${mb(u.size ?? 0)} 后 Publisher 会退出，${how}，装好后自动重新打开。`];
      // Quitting closes the browser, and with it the forms waiting there.
      const review = state.tasks.filter((t) => t.status === "review").length;
      if (review) notes.push(`还有 ${review} 个「待确认」的任务，浏览器里填好的页面会随应用一起关闭，没提交的需要重新发布。`);
      if (u.local) notes.push(`当前是本地构建（${u.current}），网盘上的 ${u.latest} 不一定比本地代码新，更新会用它覆盖当前文件夹。`);
      if (!(await ask(notes.join("\n\n"), { title: `更新到 ${u.latest}`, ok: "下载并更新", danger: review > 0 || u.local }))) return;
      return void attempt(() => Updates.install());
    }
    case "check-all":
      return checkAll();
    case "login-all":
      return loginAll();
    case "close-browser":
      await attempt(() => Accounts.closeBrowser());
      return toast("已关闭自动化浏览器");
    case "retry-failed": {
      const failed = state.tasks.filter((t) => taskKind(t) === state.kind && retryable(t));
      const names = failed.map((t) => `${platformName(t.platform)}：${taskTitle(t)}`);
      if (!failed.length || !(await ask(`把 ${failed.length} 个失败的任务重新排队发布？`, { title: "重试失败的任务", ok: "重新排队", detail: names }))) return;
      for (const t of failed) await attempt(() => Publisher.retry(t.id));
      return;
    }
    case "clear-tasks":
      await attempt(() => Publisher.clearFinished());
      state.tasks = await Publisher.tasks();
      return render();
  }
});

// ---- Start ----

[state.materials, state.accounts, state.tasks, state.sync, state.schedule, state.folders, state.settings] = await Promise.all([
  Library.list(),
  Accounts.list(),
  Publisher.tasks(),
  Accounts.syncInfo(),
  Scheduler.get(),
  Library.folders(),
  Settings.get(),
]);
state.update = await Updates.state();
applyLook(state.settings);
setView("library");
