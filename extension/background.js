// 送到雷達: the popup's two buttons end up here, because the popup closes as
// soon as it loses focus while this worker keeps running.
//
// - clip: read the text of the page the user has open and send it to the
//   job-radar endpoint (POST /). The result arrives on Telegram; the badge
//   shows the score, "…" while waiting, or "✗" on failure.
// - list / open: ask the endpoint for the 104 alert jobs (GET /jobs) and open
//   the ones the user ticked, one tab at a time, in a new window.
//
// activeTab grants access to the current tab only when the user clicks, so
// the extension never reads pages on its own. Tabs are opened only from the
// popup's button, never in the background.

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

async function settings() {
  const saved = await chrome.storage.local.get(["endpoint", "secret"]);
  const defaults = self.RADAR_DEFAULTS || {};
  return {
    endpoint: saved.endpoint || defaults.endpoint,
    secret: saved.secret || defaults.secret,
  };
}

// Only the extension's own pages can send these messages: there is no
// content script and no externally_connectable.
chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
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
  }
  return false;
});

// call sends one request to the endpoint with the secret and returns the
// decoded JSON reply.
async function call(path, init = {}) {
  const { endpoint, secret } = await settings();
  if (!endpoint || !secret) {
    chrome.runtime.openOptionsPage();
    throw new Error("請先在設定頁填好接收網址和密鑰");
  }
  const res = await fetch(new URL(path, endpoint), {
    ...init,
    headers: { ...init.headers, "x-radar-secret": secret },
  });
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
