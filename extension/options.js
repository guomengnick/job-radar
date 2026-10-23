const endpoint = document.getElementById("endpoint");
const secret = document.getElementById("secret");
const status = document.getElementById("status");

// config.local.js (from make extension-config) may set the defaults.
const defaults = window.RADAR_DEFAULTS || {};
chrome.storage.local.get(["endpoint", "secret"]).then((v) => {
  endpoint.value = v.endpoint || defaults.endpoint || "";
  secret.value = v.secret || defaults.secret || "";
});

document.getElementById("save").addEventListener("click", async () => {
  const url = endpoint.value.trim();
  if (!/^https:\/\/[a-z0-9]+\.lambda-url\.ap-southeast-2\.on\.aws\/?$/.test(url)) {
    status.textContent = "接收網址格式不對";
    status.style.color = "#c62828";
    return;
  }
  await chrome.storage.local.set({ endpoint: url, secret: secret.value.trim() });
  status.textContent = "已儲存";
  status.style.color = "#2e7d32";
});

// 一鍵帶入應徵: which 104 résumé each version maps to, and the letter's fixed
// opening and closing. apply.js reads them on the 104 page.
const VERSIONS = ["A", "A+ML", "B", "B+ML", "C", "C+ML"];
const resumes = document.getElementById("resumes");
const opening = document.getElementById("opening");
const closing = document.getElementById("closing");
const applyStatus = document.getElementById("apply-status");

for (const v of VERSIONS) {
  const label = document.createElement("label");
  label.textContent = `${v} 版用的 104 履歷`;
  const input = document.createElement("input");
  input.dataset.version = v;
  label.htmlFor = input.id = "resume-" + v.replace("+", "-");
  resumes.append(label, input);
}

chrome.storage.local.get("apply").then(({ apply = {} }) => {
  for (const input of resumes.querySelectorAll("input")) {
    input.value = (apply.resumes || {})[input.dataset.version] || "";
  }
  opening.value = apply.opening || "";
  closing.value = apply.closing || "";
});

document.getElementById("save-apply").addEventListener("click", async () => {
  const map = {};
  for (const input of resumes.querySelectorAll("input")) {
    if (input.value.trim()) map[input.dataset.version] = input.value.trim();
  }
  await chrome.storage.local.set({ apply: { resumes: map, opening: opening.value.trim(), closing: closing.value.trim() } });
  applyStatus.textContent = "已儲存";
});

// 早上提醒: on/off and the time; background.js reschedules when they change.
const morningOn = document.getElementById("morning-on");
const morningTime = document.getElementById("morning-time");
const morningStatus = document.getElementById("morning-status");

chrome.storage.local.get("morning").then(({ morning = {} }) => {
  morningOn.checked = morning.on !== false;
  morningTime.value = morning.time || "09:00";
});

document.getElementById("save-morning").addEventListener("click", async () => {
  await chrome.storage.local.set({ morning: { on: morningOn.checked, time: morningTime.value || "09:00" } });
  morningStatus.textContent = morningOn.checked ? `已儲存：每天 ${morningTime.value || "09:00"}` : "已關閉";
});

document.getElementById("test-morning").addEventListener("click", async () => {
  morningStatus.textContent = "讀取中…";
  const r = await chrome.runtime.sendMessage({ type: "morning-test" });
  morningStatus.textContent = r && r.error ? `失敗：${r.error}` : "通知已送出（列的是最近一天的職缺）";
});
