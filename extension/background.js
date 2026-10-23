// 送到雷達: the popup's two buttons end up here, because the popup closes as
// soon as it loses focus while this worker keeps running.
//
// - clip: read the text of the page the user has open and send it to the
//   job-radar endpoint (POST /). The result arrives on Telegram; the badge
//   shows the score, "…" while waiting, or "✗" on failure.
// - list / open: ask the endpoint for the 104 alert jobs (GET /jobs) and open
//   the ones the user ticked, one tab at a time, in a new window.
// - batch: read every open 104 job tab and send them all (POST /batch); the
//   model judges them as one batch and the summary arrives on Telegram.
//
// Pages are read only when the user presses a button: the current tab
// (activeTab), or the open 104 job tabs (the www.104.com.tw permission).
// Tabs are opened only from the popup's button, never in the background.

// `make extension-config` writes config.local.js with the endpoint and
// secret, so a fresh install works without visiting the options page.
try {
  importScripts("config.local.js");
} catch (e) {
  // No local config: the options page is the only source.
}

// The pause between two new tabs. A person opening links goes no faster,
// and 50 pages take about a minute and a quarter.
const OPEN_GAP_MS = 1500;
const JOB_URL = /^https:\/\/www\.104\.com\.tw\/job\/[0-9a-z]+$/;
const JOB_TABS = "https://www.104.com.tw/job/*";
// The model reads at most this much of a page (assess.MaxPageRunes), and one
// request must stay under the endpoint's 5 MB.
const PAGE_CHARS = 20000;
const BATCH_BYTES = 4 << 20;

async function settings() {
  const saved = await chrome.storage.local.get(["endpoint", "secret"]);
  const defaults = self.RADAR_DEFAULTS || {};
  return {
    endpoint: saved.endpoint || defaults.endpoint,
    secret: saved.secret || defaults.secret,
  };
}

// Only the extension's own pages (the popup) may send these messages. The
// content script apply.js runs inside 104 pages and sends none, so anything
// from a tab is ignored; there is no externally_connectable.
chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (sender.id !== chrome.runtime.id || sender.tab) return false;
  switch (msg && msg.type) {
    case "clip":
      clipTab(msg.tabId);
      sendResponse({ ok: true });
      return false;
    case "list":
      listJobs(msg.since).then(sendResponse, (err) => sendResponse({ error: err.message }));
      return true; // answers later
    case "open":
      openJobs(msg.urls).catch((err) => console.error("開啟職缺失敗", err));
      sendResponse({ ok: true });
      return false;
    case "batch":
      sendBatch().then(sendResponse, (err) => sendResponse({ error: err.message }));
      return true;
  }
  return false;
});

// call sends one request to the endpoint with the secret and this
// extension's version, and returns the decoded JSON reply. The server names
// its commit in x-radar-server; the popup shows the last one seen. A 426
// means this extension is older than the server accepts.
async function call(path, init = {}) {
  const { endpoint, secret } = await settings();
  if (!endpoint || !secret) {
    chrome.runtime.openOptionsPage();
    throw new Error("請先在設定頁填好接收網址和密鑰");
  }
  const res = await fetch(new URL(path, endpoint), {
    ...init,
    headers: { ...init.headers, "x-radar-secret": secret, "x-radar-client": chrome.runtime.getManifest().version },
  });
  const server = res.headers.get("x-radar-server");
  if (server) await chrome.storage.local.set({ server: { commit: server, seen: new Date().toISOString() } });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(`HTTP ${res.status} ${body.error || ""}`.trim());
  return body;
}

async function clipTab(tabId) {
  await badge(tabId, "…", "#757575");
  try {
    const [{ result: page }] = await chrome.scripting.executeScript({
      target: { tabId },
      func: readPage,
    });
    const body = await call("/", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(page),
    });
    await badge(tabId, String(body.score), body.score >= 75 ? "#2e7d32" : body.score >= 55 ? "#f9a825" : "#c62828");
  } catch (err) {
    console.error("送到雷達失敗", err);
    await badge(tabId, "✗", "#c62828");
  }
}

async function listJobs(since) {
  return call("/jobs?since=" + encodeURIComponent(since));
}

let opening = false;

// openJobs opens the first job in a new window and the rest as background
// tabs there, one every OPEN_GAP_MS. The badge counts down what is left.
// Each tabs call also keeps this worker alive while it waits.
async function openJobs(urls) {
  const list = (urls || []).filter((u) => JOB_URL.test(u));
  if (opening || list.length === 0) return;
  opening = true;
  try {
    await chrome.action.setBadgeBackgroundColor({ color: "#1565c0" });
    const win = await chrome.windows.create({ url: list[0], focused: true });
    for (let i = 1; i < list.length; i++) {
      await chrome.action.setBadgeText({ text: String(list.length - i) });
      await new Promise((r) => setTimeout(r, OPEN_GAP_MS));
      await chrome.tabs.create({ windowId: win.id, url: list[i], active: false });
    }
  } finally {
    opening = false;
    await chrome.action.setBadgeText({ text: "" });
  }
}

let sending = false;

// sendBatch reads every open 104 job tab and posts the pages. A tab that is
// still loading or was unloaded by Chrome is sent with no text, so the
// summary lists it instead of dropping it. Pages are split into requests of
// at most BATCH_BYTES; each request is its own batch.
async function sendBatch() {
  if (sending) throw new Error("上一批還在送");
  sending = true;
  await chrome.action.setBadgeBackgroundColor({ color: "#757575" });
  await chrome.action.setBadgeText({ text: "…" });
  try {
    const tabs = await chrome.tabs.query({ url: JOB_TABS });
    if (tabs.length === 0) throw new Error("沒有開著的 104 職缺頁");
    const pages = [];
    for (const tab of tabs) {
      let page = { url: tab.url, title: tab.title || "", text: "" };
      if (!tab.discarded && tab.status === "complete") {
        try {
          const [{ result }] = await chrome.scripting.executeScript({ target: { tabId: tab.id }, func: readPage });
          page = { ...result, text: result.text.slice(0, PAGE_CHARS) };
        } catch (err) {
          console.warn("讀不到分頁", tab.url, err);
        }
      }
      pages.push(page);
    }
    const total = { received: 0, submitted: 0, excluded: 0, skipped: 0, requests: 0 };
    for (const chunk of chunks(pages)) {
      const r = await call("/batch", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ pages: chunk }),
      });
      for (const k of ["received", "submitted", "excluded", "skipped"]) total[k] += r[k] || 0;
      total.requests++;
    }
    await chrome.action.setBadgeBackgroundColor({ color: "#2e7d32" });
    await chrome.action.setBadgeText({ text: String(total.received) });
    return total;
  } catch (err) {
    await chrome.action.setBadgeBackgroundColor({ color: "#c62828" });
    await chrome.action.setBadgeText({ text: "✗" });
    throw err;
  } finally {
    sending = false;
  }
}

function chunks(pages) {
  const out = [];
  let cur = [];
  let size = 0;
  for (const p of pages) {
    const n = new Blob([JSON.stringify(p)]).size;
    if (cur.length > 0 && size + n > BATCH_BYTES) {
      out.push(cur);
      cur = [];
      size = 0;
    }
    cur.push(p);
    size += n;
  }
  if (cur.length > 0) out.push(cur);
  return out;
}

// Runs inside the page. It must be self-contained: Chrome serializes it.
// main holds the posting on most job sites; the whole body is the fallback
// when there is no main or it holds little text.
function readPage() {
  const main = document.querySelector("main");
  const text = (main && main.innerText.length > 500 ? main : document.body).innerText;
  return {
    url: location.href,
    title: document.title,
    text: text.replace(/\n{3,}/g, "\n\n").slice(0, 60000),
  };
}

async function badge(tabId, text, color) {
  await chrome.action.setBadgeBackgroundColor({ tabId, color });
  await chrome.action.setBadgeText({ tabId, text });
}
