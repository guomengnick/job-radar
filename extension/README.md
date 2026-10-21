# 送到雷達（Chrome 擴充功能）

在任何職缺頁按一下工具列上的「送到雷達」，這一頁的文字就會送到 job-radar 的 clip 函式，由 Claude 依你的評分檔案評分，結果用 Telegram 回覆。

- 圖示上的徽章：`…` 評分中，數字是分數（綠 75 以上、黃 55–74、紅 55 以下），`✗` 是失敗（原因看 Telegram 或 CloudWatch 的 `/aws/lambda/job-radar-clip`）。
- 只有在你按下按鈕時，才會讀取目前這一頁（`activeTab` 權限），不會自己去讀任何網頁。
- 頁面是你自己用瀏覽器正常打開的，擴充功能只讀你畫面上已經顯示的文字。

## 安裝（每台電腦一次）

1. Chrome 開 `chrome://extensions`，打開右上角的「開發人員模式」。
2. 按「載入未封裝項目」，選這個 `extension` 資料夾。
3. 把「送到雷達」釘選到工具列（拼圖圖示 → 圖釘）。

## 設定接收網址和密鑰

第一次按按鈕會自動打開設定頁。兩個值：

接收網址（複製到剪貼簿）：

```bash
AWS_PROFILE=radar terraform -chdir=infra output -raw clip_url | pbcopy
```

密鑰：下面這行會把密鑰複製到剪貼簿，不會顯示在畫面上，貼到設定頁的「密鑰」欄位：

```bash
aws ssm get-parameter --profile radar --region ap-southeast-2 --name /job-radar/clip-secret --with-decryption --query Parameter.Value --output text | tr -d '\n' | pbcopy
```

## 換密鑰

密鑰外洩時，產生新的、覆蓋舊的，再到設定頁貼上新值：

```bash
aws ssm put-parameter --profile radar --region ap-southeast-2 --name /job-radar/clip-secret --type SecureString --overwrite --value "$(openssl rand -hex 32)"
```

## 費用

一次評分是一次 Claude 呼叫（`config/radar.yml` 的 `assess.model`）。花費上限請設在 Anthropic Console 的 Limits。
