const endpoint = document.getElementById("endpoint");
const secret = document.getElementById("secret");
const status = document.getElementById("status");

chrome.storage.local.get(["endpoint", "secret"]).then((v) => {
  endpoint.value = v.endpoint || "";
  secret.value = v.secret || "";
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
