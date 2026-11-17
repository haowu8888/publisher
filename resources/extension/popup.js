// Sends this browser's cookies of the publishing platforms to the Publisher
// app (internal/cookiesync), which puts them into its automation browser.
// The app names the platforms and their domains; host_permissions in
// manifest.json must cover them, or chrome.cookies returns nothing for a
// domain. All platforms sync at once, or only the ticked ones.
const API = "http://127.0.0.1:17891";
// Platforms the user unticked; remembering these instead of the ticked ones
// leaves platforms the app adds later ticked.
const UNPICKED = "unpicked";

const list = document.getElementById("platforms");
const allButton = document.getElementById("sync-all");
const pickedButton = document.getElementById("sync-picked");
const status = document.getElementById("status");

const statusText = { loggedIn: "已登录", loggedOut: "未登录", checking: "检测中…" };

// The app's platforms, or null when it is not running. An app before
// /platforms gives only its domains, as one entry without an id.
let platforms = null;

function show(html) {
  status.innerHTML = html;
}

function esc(s) {
  return String(s).replace(/[&<>"]/g, (c) => `&${{ "&": "amp", "<": "lt", ">": "gt", '"': "quot" }[c]};`);
}

function loadUnpicked() {
  try {
    return new Set(JSON.parse(localStorage.getItem(UNPICKED)) ?? []);
  } catch {
    return new Set();
  }
}

function saveUnpicked() {
  const ids = [...list.querySelectorAll("input:not(:checked):not(:disabled)")].map((i) => i.value);
  try {
    localStorage.setItem(UNPICKED, JSON.stringify(ids));
  } catch {}
}

async function fetchPlatforms() {
  try {
    let res = await fetch(`${API}/platforms`);
    if (res.status === 404) {
      res = await fetch(`${API}/domains`);
      if (res.ok) return [{ id: "", name: "全部", domains: await res.json() }];
    }
    if (!res.ok) throw new Error(await res.text());
    return await res.json();
  } catch {
    return null;
  }
}

async function cookiesOf(domains) {
  const seen = new Set();
  const cookies = [];
  for (const domain of domains) {
    for (const c of await chrome.cookies.getAll({ domain })) {
      const key = `${c.domain}|${c.path}|${c.name}`;
      if (!seen.has(key)) {
        seen.add(key);
        cookies.push(c);
      }
    }
  }
  return cookies;
}

async function render() {
  const unpicked = loadUnpicked();
  const rows = [];
  for (const p of platforms) {
    if (!p.id) continue;
    if (!p.domains?.length) {
      rows.push(`<li><label class="off" title="${esc(p.note ?? "")}"><input type="checkbox" disabled><span class="name">${esc(p.name)}</span><small>${esc(p.note ? "需在应用里扫码" : "不能同步")}</small></label></li>`);
      continue;
    }
    const n = (await cookiesOf(p.domains)).length;
    rows.push(
      `<li><label><input type="checkbox" value="${esc(p.id)}" ${unpicked.has(p.id) ? "" : "checked"}><span class="name">${esc(p.name)}</span><small>${n ? `${n} 条 Cookie` : "本浏览器未登录"}</small></label></li>`,
    );
  }
  list.innerHTML = rows.join("");
  pickedButton.hidden = rows.length === 0;
  updatePicked();
}

function picked() {
  return [...list.querySelectorAll("input:checked")].map((i) => i.value);
}

function updatePicked() {
  const n = picked().length;
  pickedButton.textContent = `同步所选（${n}）`;
  pickedButton.disabled = n === 0;
}

// sync sends the cookies of the platforms with the given ids, or of all.
async function sync(ids) {
  allButton.disabled = pickedButton.disabled = true;
  show("正在连接 Publisher…");
  try {
    if (!platforms) {
      platforms = await fetchPlatforms();
      if (platforms) await render();
    }
    if (!platforms) {
      return show(`<span class="err">连不上 Publisher，请先打开应用再试。</span>`);
    }
    const chosen = platforms.filter((p) => !ids || ids.includes(p.id));
    const cookies = await cookiesOf(chosen.flatMap((p) => p.domains ?? []));
    if (cookies.length === 0) {
      return show(`<span class="err">本浏览器里没有找到${ids ? "所选" : "这些"}平台的登录信息。</span>`);
    }

    show(`正在导入 ${cookies.length} 条登录信息…（Publisher 会打开它的自动化浏览器）`);
    const res = await fetch(`${API}/import`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ cookies }),
    });
    if (!res.ok) throw new Error(await res.text());
    const result = await res.json();
    const rows = result.platforms
      .map((p) => `<li><span>${esc(p.name)}</span><span class="${esc(p.status)}">${statusText[p.status] ?? esc(p.status)}</span></li>`)
      .join("");
    show(
      `已导入 ${result.imported} 条${result.failed ? `，${result.failed} 条失败` : ""}。` +
        (rows ? `<ul>${rows}</ul>` : "") +
        `<p style="margin-top:8px">「检测中」的平台结果会显示在 Publisher 的账号页。</p>`,
    );
  } catch (err) {
    show(`<span class="err">同步失败：${esc(err.message || err)}</span>`);
  } finally {
    allButton.disabled = false;
    updatePicked();
  }
}

allButton.addEventListener("click", () => sync(null));
pickedButton.addEventListener("click", () => sync(picked()));
list.addEventListener("change", () => {
  saveUnpicked();
  updatePicked();
});

(async () => {
  platforms = await fetchPlatforms();
  if (platforms) await render();
  else show(`<span class="err">连不上 Publisher，请先打开应用；打开后点同步即可。</span>`);
})();
