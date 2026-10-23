// 送到雷達: fills 104's apply popup from a Telegram 帶入應徵 link, then stops.
//
// The link is a job page with 104's own ?apply=form, which opens the popup,
// and a fragment only job-radar adds: #radar-apply?cv=B+ML&line=…. This
// script does nothing on any other page load. It
//
// - chooses the résumé whose 104 name the options page maps to that version,
// - writes the cover letter: the options page's opening, the line from the
//   link, and the closing,
//
// and shows a note at the top of the page. It never presses 確認送出 and
// leaves the two checkboxes (儲存此次修改, 新增自我推薦信) alone, so the
// letter templates saved on 104 stay as they are. The user reads the form
// and sends it.
//
// It runs at document_start to take the fragment before 104's page script
// sees it, and removes it so a reload or a shared link does not refill.

(() => {
  const MARK = "#radar-apply";
  if (!location.hash.startsWith(MARK)) return;
  const raw = location.hash.slice(MARK.length).replace(/^\?/, "");
  history.replaceState(history.state, "", location.pathname + location.search);
  const params = new URLSearchParams(raw);
  const cv = params.get("cv") || "";
  const line = (params.get("line") || "").trim();

  // How long to wait for the popup: the page and 104's script load first.
  const WAIT_MS = 20000;
  // How long 104's script may take to react to a choice.
  const SETTLE_MS = 400;

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

  function waitFor(find) {
    return new Promise((resolve) => {
      const hit = find();
      if (hit) return resolve(hit);
      const obs = new MutationObserver(() => {
        const el = find();
        if (el) {
          obs.disconnect();
          resolve(el);
        }
      });
      obs.observe(document.documentElement, { childList: true, subtree: true });
      setTimeout(() => {
        obs.disconnect();
        resolve(null);
      }, WAIT_MS);
    });
  }

  // The résumé field is a custom dropdown (vue-multiselect), not a <select>:
  // the shown name is in .text-region, the choices are spans in a hidden list.
  // Clicking a choice is what a person's click does.
  async function chooseResume(body, name) {
    const box = body.querySelector(".select-resume");
    const shown = () => (box?.querySelector(".text-region")?.textContent || "").trim();
    if (!box) return { ok: false, why: "找不到「選擇履歷」欄位" };
    if (shown() === name) return { ok: true };
    const options = [...box.querySelectorAll(".multiselect__option")];
    const option = options.find((o) => o.textContent.trim() === name);
    if (!option) {
      return { ok: false, why: `104 上沒有叫「${name}」的履歷。有：${options.map((o) => o.textContent.trim()).join("、")}` };
    }
    option.click();
    await sleep(SETTLE_MS);
    if (shown() !== name) {
      // Some versions only take a choice while the list is open.
      box.querySelector(".form-element-tag-region")?.focus();
      option.click();
      await sleep(SETTLE_MS);
    }
    return shown() === name ? { ok: true } : { ok: false, why: `選不到「${name}」，請手動選` };
  }

  // A plain assignment would not reach 104's script (Vue listens for input
  // events), so set the value the way typing does and announce it.
  function setText(textarea, text) {
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value").set;
    setter.call(textarea, text);
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
  }

  async function writeLetter(body, text) {
    const textarea = body.querySelector("textarea");
    if (!textarea) return { ok: false, why: "找不到推薦信欄位" };
    setText(textarea, text);
    await sleep(SETTLE_MS);
    // Choosing a résumé may reload the default letter; write it once more.
    if (textarea.value !== text) {
      setText(textarea, text);
      await sleep(SETTLE_MS);
    }
    return textarea.value === text ? { ok: true } : { ok: false, why: "推薦信沒寫進去，請手動貼上" };
  }

  function note(lines, good) {
    const box = document.createElement("div");
    box.setAttribute("role", "status");
    box.style.cssText = [
      "position:fixed", "top:12px", "left:12px", "z-index:2147483647", "max-width:420px",
      "padding:10px 32px 10px 12px", "border-radius:6px", "font:14px/1.5 system-ui,sans-serif",
      "box-shadow:0 2px 8px rgba(0,0,0,.25)", "white-space:pre-line",
      good ? "background:#e8f5e9;color:#1b5e20" : "background:#fff3e0;color:#bf360c",
    ].join(";");
    box.textContent = "📡 送到雷達\n" + lines.join("\n");
    const close = document.createElement("button");
    close.textContent = "×";
    close.setAttribute("aria-label", "關閉");
    close.style.cssText = "position:absolute;top:4px;right:6px;border:0;background:none;font-size:18px;cursor:pointer;color:inherit";
    close.onclick = () => box.remove();
    box.appendChild(close);
    document.body.appendChild(box);
  }

  async function run() {
    const { apply = {} } = await chrome.storage.local.get("apply");
    const body = await waitFor(() => document.querySelector(".apply-popup__body"));
    if (!body) {
      note(["沒看到應徵視窗：可能已經應徵過、沒登入，或 104 改版了。"], false);
      return;
    }
    const done = [];
    const problems = [];

    const name = (apply.resumes || {})[cv];
    if (!name) {
      problems.push(`設定頁沒填「${cv || "？"}」版對應的 104 履歷名稱，請手動選。`);
    } else {
      const r = await chooseResume(body, name);
      r.ok ? done.push(`履歷：${name}`) : problems.push(r.why);
    }

    const letter = [apply.opening, line, apply.closing].map((s) => (s || "").trim()).filter(Boolean).join("\n");
    if (!apply.opening || !apply.closing) problems.push("設定頁沒填推薦信的開頭或結尾。");
    if (letter) {
      const r = await writeLetter(body, letter);
      r.ok ? done.push(line ? "推薦信：開頭＋這個職缺的一句＋結尾" : "推薦信：開頭＋結尾") : problems.push(r.why);
    }

    note([...done.map((s) => "✅ " + s), ...problems.map((s) => "⚠️ " + s), "看過後請自己按「確認送出」。"], problems.length === 0);
  }

  const start = () => run().catch((err) => note([`出錯了：${err.message}`], false));
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start, { once: true });
  } else {
    start();
  }
})();
