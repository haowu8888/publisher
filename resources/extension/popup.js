// Sends this browser's cookies of the publishing platforms to the Publisher
// app (internal/cookiesync), which puts them into its automation browser.
// The app names the domains; host_permissions in manifest.json must cover
// them, or chrome.cookies returns nothing for a domain.
const API = "http://127.0.0.1:17891";

const button = document.getElementById("sync");
const status = document.getElementById("status");

const statusText = { loggedIn: "已登录", loggedOut: "未登录", checking: "检测中…" };

function show(html) {
  status.innerHTML = html;
}

async function sync() {
  button.disabled = true;
  show("正在连接 Publisher…");
  try {
    let domains;
    try {
      const res = await fetch(`${API}/domains`);
      if (!res.ok) throw new Error(await res.text());
      domains = await res.json();
    } catch {
      return show(`<span class="err">连不上 Publisher，请先打开应用再试。</span>`);
    }

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
    if (cookies.length === 0) {
      return show(`<span class="err">本浏览器里没有找到这些平台的登录信息。</span>`);
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
      .map((p) => `<li><span>${p.name}</span><span class="${p.status}">${statusText[p.status] ?? p.status}</span></li>`)
      .join("");
    show(
      `已导入 ${result.imported} 条${result.failed ? `，${result.failed} 条失败` : ""}。` +
        (rows ? `<ul>${rows}</ul>` : "") +
        `<p style="margin-top:8px">「检测中」的平台结果会显示在 Publisher 的账号页。</p>`,
    );
  } catch (err) {
    show(`<span class="err">同步失败：${String(err.message || err)}</span>`);
  } finally {
    button.disabled = false;
  }
}

button.addEventListener("click", sync);
