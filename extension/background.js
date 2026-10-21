// 送到雷達: on a click, read the text of the page the user has open and send
// it to the job-radar clip endpoint. The result arrives on Telegram; the
// badge shows the score, "…" while waiting, or "✗" on failure.
//
// activeTab grants access to the current tab only when the user clicks, so
// the extension never reads pages on its own.

chrome.action.onClicked.addListener(async (tab) => {
  const { endpoint, secret } = await chrome.storage.local.get(["endpoint", "secret"]);
  if (!endpoint || !secret) {
    chrome.runtime.openOptionsPage();
    return;
  }
  await badge(tab.id, "…", "#757575");
  try {
    const [{ result: page }] = await chrome.scripting.executeScript({
      target: { tabId: tab.id },
      func: readPage,
    });
    const res = await fetch(endpoint, {
      method: "POST",
      headers: { "content-type": "application/json", "x-radar-secret": secret },
      body: JSON.stringify(page),
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(`HTTP ${res.status} ${body.error || ""}`);
    await badge(tab.id, String(body.score), body.score >= 75 ? "#2e7d32" : body.score >= 55 ? "#f9a825" : "#c62828");
  } catch (err) {
    console.error("送到雷達失敗", err);
    await badge(tab.id, "✗", "#c62828");
  }
});

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
