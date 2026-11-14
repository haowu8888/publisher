// Lists a page's form controls, editors and buttons with the attributes useful for selectors.
(() => {
  const vis = (e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 };
  const cls = (e) => (typeof e.className === "string" ? e.className : "").trim().split(/\s+/).slice(0, 4).join(".");
  const out = [];
  document.querySelectorAll("input, textarea, [contenteditable=true], button, [role=button]").forEach((e) => {
    const a = ["type", "placeholder", "accept", "maxlength", "name"].map((k) => e.getAttribute(k) ? `${k}=${e.getAttribute(k)}` : "").filter(Boolean).join(" ");
    const t = (e.innerText || e.value || "").trim().replace(/\s+/g, " ").slice(0, 30);
    out.push(`${vis(e) ? "V" : "-"} ${e.tagName.toLowerCase()}.${cls(e)} ${a} ${t ? JSON.stringify(t) : ""}`);
  });
  return out.join("\n");
})()
