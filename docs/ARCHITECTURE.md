# 職缺雷達架構說明

## 1. 為什麼要做

**現有工具（career-ops）的痛點**：它只在 Mac 開機、App 開著時才會跑（2026-10-05 到 10-08 連續 4 天沒跑）。結果散在 6 個資料夾，看不出它做了什麼，也不會主動通知。每天還白打約 6,000 個已經不存在的看板，而且 404、429、逾時全部被算成「無法連線」。

**雷達補上的事**：
- 雲端 24 小時執行；
- 每天一則摘要傳到手機，裡面就是執行看板；
- 用職缺 ID 判斷是不是新職缺；
- 已經不存在的看板會自動跳過。

**和 career-ops 怎麼分工（不是取代）**

| 工作 | 誰負責 |
|---|---|
| Greenhouse、Lever、Ashby 每天掃描並通知 | 雷達 |
| Workday 每週掃描、Gmail 裡的 LinkedIn 通知信、0–100 深入評分、投遞追蹤、客製履歷 | career-ops |

流程：雷達通知 → 點連結看 → 想投的，把網址交給 career-ops 或 Claude 做完整評分。兩邊並行兩週、比對結果後，再決定要不要關掉 career-ops 每日排程裡這三家的掃描。

## 2. 架構

```
EventBridge Scheduler（Asia/Taipei）
  08:00 ─► Lambda job-radar-scan  {"task":"scan","ats":"greenhouse|lever|ashby"}
              │ 每家 API 一個限速器；逾時 20 秒；429、5xx、網路錯誤重試 2 次（退避加隨機延遲，遵守 Retry-After）
              ├─► DynamoDB job-radar-boards  看板狀態
              ├─► DynamoDB job-radar-jobs    通過職稱篩選的職缺（用條件寫入判斷是不是新的）
              ├─► DynamoDB job-radar-runs    每次執行的統計（保留 90 天）
              └─► stdout 輸出 EMF 指標 ─► CloudWatch 指標、儀表板、告警
  08:40 ─► Lambda job-radar-digest ─► 讀 runs 和待通知的 jobs ─► Telegram
  週日 07:00 ─► Lambda job-radar-sync ─► 下載公開看板清單 ─► 把新看板加進 boards
```

三個 Lambda 用同一個 Go 執行檔，但各自有不同的逾時設定和 IAM 權限。

## 3. 元件、用意與權衡

| 元件 | 用意 | 權衡 |
|---|---|---|
| EventBridge Scheduler | 不用電腦開著；排程可以直接指定台北時區 | 傳統的 EventBridge 規則只能用 UTC |
| Lambda（Go、arm64、provided.al2023） | 一天只跑幾分鐘，用多少付多少 | 單次最長 15 分鐘，靠「最久沒檢查的先掃」接續（見 §6）。Fargate 沒有時間上限，但設定比較多 |
| DynamoDB（隨需計費） | 不需要 VPC 或 NAT；條件寫入同時做到「判斷新舊」和去重 | 臨時查詢不方便；你熟悉的 PostgreSQL 要一直開著並放在 VPC 裡 |
| SSM Parameter Store | 存 Bot token：免費、加密，用 IAM 控制誰能讀 | Secrets Manager 能自動輪替，但要收費 |
| CloudWatch EMF | 印一行 JSON 就同時有日誌和指標 | 每多一個維度，計費的指標數就倍增，所以不設維度 |
| Telegram | 免費，一個 POST 就送到手機 | 需要自己建 Bot |
| Terraform | 一個指令建立或刪除整套環境；也是職缺 JD 上常見的要求 | 比 SAM 繁瑣 |
| 不放進 VPC | 用到的都是公開端點，靠 IAM 驗證，省下 NAT 每月 30 多美元 | 面試時要能說明理由（就是上面這句） |

**Go 程式的分層**：`ats`（每家 ATS 一個轉接器）→ `fetch`（HTTP 規則）→ `filter`（篩選規則）→ `store`（介面，有本機 JSON 和 DynamoDB 兩種實作）→ `app`（用例）。CLI 和 Lambda 呼叫同一份 `app`，所以本機能跑，就代表雲端的邏輯也是對的。只用標準庫加官方 SDK，沒有用 Web 框架或 ORM。

## 4. 資料模型

| 表 | 主鍵 | 重點 |
|---|---|---|
| boards | `ats` + `slug` | status（unknown／ok／failing／dead）、consecutive_404、last_checked_at、last_ok_at |
| jobs | `key` = `ats#slug#id` | 只存通過職稱篩選的職缺。寫入條件是 `attribute_not_exists`。另有稀疏索引 `pending-index`：只有待通知的職缺帶有 `pending` 欄位；索引 `source-index`（`ats`、`first_seen`）依來源和時間列出所有職缺，擴充功能用它列出某天起的 104 職缺，日報用它列出被規則排除的。同一張表也存評分結果（`assess#…`）和批次（`batch#…`，等待總表時 `pending` 為 `batch`，batch 函式每 10 分鐘從 `pending-index` 找出來） |
| runs | `pk="run"` + `sk=開始時間#ats#task` | 各種結果的計數、新增數、待通知數、狀態；`expires_at` 到期自動刪除 |

評分結果（`assess#…`）的 `jd` 欄位指向 S3 pages 桶裡被評的那一頁：104 職缺是 `jd/104/<代碼>.json`，其他網站是 `jd/web/<網址雜湊>.json`，保存一年，用來對照 AI 的判斷和 JD 原文，也當評估集。整批送出的原始頁面在 `batches/`，保存 30 天。

## 5. 篩選規則（`config/radar.yml`，移植自 career-ops）

1. **職稱**：與 career-ops `portals.yml` 相同的關鍵字。另外加了排除詞「in Test」「AI Trainer」「Trainer」，這是第一次實際執行後補上的。
2. **地點分類**（`internal/filter/location.go`）：
   - **A**：明確可以從台灣應徵。台灣遠端；台北或新竹混合辦公、每週到班 2 天以內；地點寫全球、亞太或 Anywhere in the world。
   - **B**：待確認。只寫 Remote 沒寫國家；台灣職缺但工作型態或到班天數不明；寫全球但要配合美國或歐洲時區；台中。
   - **C**：限其他國家。包含 Remote 但綁在國外城市（例如 NYC）、「Anywhere in France」、JD 寫明必須住在某國。
   - **D**：在國外辦公室上班，或台灣到班天數超過限制。
3. **技能分數**：用技能關鍵字加權計分，上限 100。不會的語言不扣分。
4. **通知門檻**：
   - A 類分數 ≥ 40，B 類分數 ≥ 60。
   - **台灣遠端職缺一律通知。** 其他台灣職缺即使是 B 類，也用 A 類的門檻（≥ 40）。

## 6. 失敗處理

| 狀況 | 做法 |
|---|---|
| 被限速（429） | 每家每秒 15 個請求、同時最多 16 個；照 Retry-After 等待後重試 |
| 回應卡住 | `http.Client.Timeout` 連讀取內容的時間都包含在內 |
| 看板不存在 | 連續 3 次 404 判定為死，之後每 7 天檢查一次；恢復正常就算「復活」 |
| 快碰到 15 分鐘上限 | 截止前 60 秒停止開新的看板，截止前 30 秒取消還在進行的抓取（卡住的看板最多可能佔用 60 秒以上，會把存檔時間吃掉）。被取消的看板不記錄，這次記為 partial；下次從最久沒檢查的看板開始，不需要存進度 |
| 重複通知 | 條件寫入；摘要送出後改成 notified，並移除 pending |
| 系統默默停了 | 24 小時內沒有成功的掃描就觸發心跳告警；摘要也會寫出哪一家今天沒有執行紀錄 |
| 104 配對信默默停了 | 48 小時沒有解析到任何配對信（`AlertsParsed` 指標）就觸發告警 |
| 告警寄到信箱沒看到 | 告警的 SNS 主題同時訂閱 alarm 函式，用中文轉發到 Telegram；Email 保留當備援 |
| 打到非預期的網域 | 網域白名單只有三家 ATS API 加 GitHub raw；不跟隨重新導向 |
| Lambda 非同步呼叫自動重試，造成重複執行 | 把重試次數設為 0；失敗會在摘要和告警裡看到 |

## 7. 安全

- 每個函式有自己的 IAM 角色：只有 digest 能讀 token；只有 scan 和 sync 能寫 boards。
- Token 是手動建立的 SecureString，不會出現在 Terraform state 裡。發生錯誤時，訊息裡的 token 會被遮掉。
- Terraform state 存在私人的 S3 bucket；`terraform.tfvars` 和 `backend.hcl` 都不提交到 git。

## 8. 費用（估計，以第一個月的帳單為準）

Lambda 在免費額度內；DynamoDB 隨需計費每月不到 1 美元；CloudWatch 約 0–2 美元。**合計每月約 1–3 美元。** 預算告警在 5 美元和 10 美元時寄信。刻意不用 NAT、RDS、EKS、負載平衡器。

## 9. 進度

| 階段 | 狀態 |
|---|---|
| 1–4 本機程式（抓取、篩選、比對、通知） | ✅ 完成，有測試；2026-10-11 本機實跑每家 1,300 個看板 |
| 5 AWS 和 Terraform | 程式碼已寫好，**還沒部署**（需要 AWS 帳號，並安裝 Terraform 和 AWS CLI） |
| 6 指標、儀表板、告警 | 程式碼已寫好，跟第 5 階段一起部署 |
| 7 SQS 分派加 DLQ；GitHub Actions 用 OIDC 部署 | 未做（CI 已有：測試加上 terraform validate） |
| 8 和 career-ops 比對兩週；寫 README、ADR、履歷條目 | 部署後進行 |
| 9 Workday | 未做 |
