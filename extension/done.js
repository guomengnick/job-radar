// 送到雷達: on 104's 應徵成功 page (www.104.com.tw/job/apply/done/?jobNo=…),
// tells the radar which job the user just applied to, so the weekly report
// and Telegram know without being told. It reads only this page: the job
// code from the address, and the title and company printed under 應徵成功.
// The page can be opened again later; the radar keeps the first record.

(() => {
  const code = new URLSearchParams(location.search).get("jobNo") || "";
  if (!/^[0-9a-z]{1,20}$/.test(code)) return;

  // 104 draws the page after load; wait for 應徵成功 to appear.
  const WAIT_MS = 15000;
  function read() {
    const lines = (document.querySelector("main") || document.body).innerText
      .split("\n").map((s) => s.trim()).filter(Boolean);
    const i = lines.indexOf("應徵成功");
    if (i < 0) return null;
    return { code, title: lines[i + 1] || "", company: lines[i + 2] || "" };
  }

  function note(text) {
    const box = document.createElement("div");
    box.setAttribute("role", "status");
    box.style.cssText = "position:fixed;top:12px;left:12px;z-index:2147483647;padding:8px 12px;border-radius:6px;" +
      "font:13px/1.5 system-ui,sans-serif;background:#e8f5e9;color:#1b5e20;box-shadow:0 2px 8px rgba(0,0,0,.25)";
    box.textContent = text;
    document.body.appendChild(box);
    setTimeout(() => box.remove(), 6000);
  }

  function send(app) {
    chrome.runtime.sendMessage({ type: "applied", ...app }, (r) => {
      if (chrome.runtime.lastError || !r || r.error) return; // the popup is not needed for this; quiet on failure
      if (r.recorded) note("📡 送到雷達：已記下這次投遞");
    });
  }

  const now = read();
  if (now) return send(now);
  const obs = new MutationObserver(() => {
    const app = read();
    if (app) {
      obs.disconnect();
      send(app);
    }
  });
  obs.observe(document.documentElement, { childList: true, subtree: true });
  setTimeout(() => obs.disconnect(), WAIT_MS);
})();
