// The popup only asks; background.js does the work, since the popup closes
// when the new window opens.

const GROUPS = [
  { id: "digest", label: "日報有列的（遠端、台中）" },
  { id: "elsewhere", label: "台中以外、非遠端" },
  { id: "excluded", label: "被規則排除的（職稱、薪資）" },
];
const GAP_SECONDS = 1.5; // OPEN_GAP_MS in background.js
const MAX_DAYS_BACK = 13; // the endpoint allows 14 days

const day = document.getElementById("day");
const groups = document.getElementById("groups");
const openButton = document.getElementById("open");
const status = document.getElementById("status");
let jobs = [];

function isoDay(d) {
  const pad = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

function say(text, error = false) {
  status.textContent = text;
  status.className = error ? "error" : "";
}

function selectedURLs() {
  const ticked = new Set(GROUPS.filter((g) => document.getElementById("g-" + g.id)?.checked).map((g) => g.id));
  return jobs.filter((j) => ticked.has(j.group)).map((j) => j.url);
}

function updateButton() {
  const n = selectedURLs().length;
  openButton.disabled = n === 0;
  openButton.textContent = n === 0 ? "開啟" : `開啟 ${n} 個分頁（約 ${Math.ceil(n * GAP_SECONDS)} 秒）`;
}

function render() {
  groups.replaceChildren();
  for (const g of GROUPS) {
    const count = jobs.filter((j) => j.group === g.id).length;
    const row = document.createElement("label");
    row.className = "group";
    const box = document.createElement("input");
    box.type = "checkbox";
    box.id = "g-" + g.id;
    box.checked = count > 0;
    box.disabled = count === 0;
    box.addEventListener("change", updateButton);
    const name = document.createElement("div");
    name.textContent = g.label;
    const n = document.createElement("span");
    n.textContent = String(count);
    row.append(box, name, n);
    groups.append(row);
  }
  updateButton();
}

async function load() {
  jobs = [];
  render();
  say("讀取中…");
  // Midnight on the chosen day in this computer's time zone.
  const since = new Date(day.value + "T00:00:00").toISOString();
  const res = await chrome.runtime.sendMessage({ type: "list", since });
  if (!res || res.error) {
    say("讀取失敗：" + (res ? res.error : "沒有回應"), true);
    return;
  }
  jobs = res.jobs || [];
  render();
  say(jobs.length === 0 ? "這段時間沒有新的 104 職缺。" : "");
}

document.getElementById("clip").addEventListener("click", async () => {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  await chrome.runtime.sendMessage({ type: "clip", tabId: tab.id });
  window.close();
});

openButton.addEventListener("click", async () => {
  const urls = selectedURLs();
  openButton.disabled = true;
  say(`開始開啟 ${urls.length} 個分頁，圖示上的數字是還剩幾個。`);
  await chrome.runtime.sendMessage({ type: "open", urls });
});

document.getElementById("settings").addEventListener("click", (e) => {
  e.preventDefault();
  chrome.runtime.openOptionsPage();
});

const today = new Date();
day.value = day.max = isoDay(today);
day.min = isoDay(new Date(today.getFullYear(), today.getMonth(), today.getDate() - MAX_DAYS_BACK));
day.addEventListener("change", () => day.value && load());
load();
