# 部署步驟

全程約 1 小時。第 1–3 步要你本人操作，因為牽涉建立帳號、付款方式和密鑰。

## 1. 建立 AWS 帳號並先設預算

2026 年起，新帳號預設是「專案」模式，用 Google（AWS Builder ID）登入，跟舊教學的畫面不同。實際走過的流程：

1. 到 aws.amazon.com 註冊，選 Free plan 或 Paid plan。Free plan 最長 6 個月，之後要升級才能繼續用。
2. 要用 Lambda、DynamoDB 這類服務，帳號要先升級成 Paid plan，再到「設定 → 專案」頁底部按「啟用進階功能」。這個動作**無法復原**，而且會**刪除支出限額**。
3. 啟用後會建立一個組織，裡面有三個帳號：管理帳戶（帳單與政策）、Identity Delegated Admin（管理登入身分），以及原本的專案帳戶（radar 部署在這裡）。
4. 組織預設掛一份 SCP（`AdvancedModeRegionRestrictionSecurityControlPolicy`），**只允許一個區域**。本帳號是 `ap-southeast-2`（雪梨），所以 radar 部署在雪梨，見 ADR 6。你的帳號可能不同，可以用下面這行逐區測試，有回 `TableNames` 的就是允許的區域：

```bash
for r in $(aws ec2 describe-regions --region us-east-1 --query 'Regions[].RegionName' --output text); do aws dynamodb list-tables --region $r >/dev/null 2>&1 && echo "$r 允許"; done
```

5. 在**管理帳戶**的 Billing → Budgets 建一個每月 10 美元的預算告警，補上被刪掉的支出限額。Terraform 之後還會在專案帳戶再建一個。

## 2. 安裝工具並登入（Mac）

Homebrew 主倉庫已經沒有 terraform，要從 HashiCorp 官方的 tap 安裝：

```bash
brew install awscli
```

```bash
brew tap hashicorp/tap
```

```bash
brew install hashicorp/tap/terraform
```

確認版本至少是 1.10（`infra/versions.tf` 的要求，S3 state 鎖定需要這個版本）：

```bash
terraform version
```

先在瀏覽器登入專案帳戶，再讓 CLI 借用這個登入，取得幾小時後失效的暫時憑證，不需要建立 IAM 使用者或長期金鑰。`--region` 一定要填 SCP 允許的區域，填錯會出現「Something went wrong」：

```bash
aws login --profile radar --region ap-southeast-2
```

之後的指令都用這個 profile：

```bash
export AWS_PROFILE=radar
```

確認身分（應該看到 `assumed-role/AccountFullAccessRole/...`）：

```bash
aws sts get-caller-identity
```

憑證過期時，重新執行 `aws login --profile radar --region ap-southeast-2`。

## 3. 建立 Telegram 機器人

1. 在 Telegram 找 @BotFather，送出 `/newbot`，記下它給的 token。
2. 傳任何一則訊息給你的新機器人，然後開下面這個網址，在回應裡找到 `chat.id`：

```bash
open "https://api.telegram.org/bot<你的token>/getUpdates"
```

3. 把 token 存進 SSM。之後由 Lambda 讀取，不會進入 git 或 Terraform state：

```bash
aws ssm put-parameter --region ap-southeast-2 --name /job-radar/telegram-bot-token --type SecureString --value '<你的token>'
```

### 送到雷達需要的三個參數（不用這個功能可以跳過）

clip 函式另外讀三個 SecureString，一樣手動建立，不進 git 或 Terraform state：

- Anthropic API 金鑰：到 console.anthropic.com 建立，先在 Limits 設每月上限。下面這行會提示你貼上，畫面不顯示，也不留在指令歷史：

```bash
read -rs "KEY?Anthropic API key: " && aws ssm put-parameter --region ap-southeast-2 --name /job-radar/anthropic-api-key --type SecureString --value "$KEY" && unset KEY
```

- 擴充功能送來的密鑰，隨機產生：

```bash
aws ssm put-parameter --region ap-southeast-2 --name /job-radar/clip-secret --type SecureString --value "$(openssl rand -hex 32)"
```

- 你的評分檔案（技能、條件、紅旗、履歷版本規則）。這是個人資料，放在 repo 之外的檔案；超過 4 KB 要加 `--tier Advanced`：

```bash
aws ssm put-parameter --region ap-southeast-2 --name /job-radar/assess-profile --type SecureString --overwrite --value file://<你的評分檔案路徑>
```

部署後照 `extension/README.md` 安裝擴充功能。

### 批次送出的規則（改了不用重新部署）

「批次送到雷達」送出的頁面，先經過 `/job-radar/batch-rules` 這組規則，被排除的不送 AI，但總表會列出來。Terraform 第一次部署時會建立它，之後由你手動改，Terraform 不會蓋回去。看目前的值：

```bash
aws ssm get-parameter --region ap-southeast-2 --name /job-radar/batch-rules --query Parameter.Value --output text
```

改成新的值（整組覆蓋；欄位名稱打錯會讓下一批直接失敗，不會悄悄失效）：

```bash
aws ssm put-parameter --region ap-southeast-2 --name /job-radar/batch-rules --type String --overwrite --value '{"exclude_title_words":["現場工程師","業務","韌體"],"exclude_location_words":["后里"],"min_annual_salary":0,"min_monthly_top_pay":0}'
```

- `exclude_title_words`：職稱（分頁標題第一個「｜」之前）含這些字就排除。
- `exclude_location_words`：頁面「上班地點」那一行含這些字就排除。
- `min_annual_salary`、`min_monthly_top_pay`：頁面「工作待遇」低於這個就排除；0 表示不檢查薪資。

## 4. 建立放 Terraform state 的 bucket（只做一次）

```bash
aws s3api create-bucket --region ap-southeast-2 --bucket job-radar-tfstate-<帳號ID> --create-bucket-configuration LocationConstraint=ap-southeast-2
```

```bash
aws s3api put-bucket-versioning --bucket job-radar-tfstate-<帳號ID> --versioning-configuration Status=Enabled
```

## 5. 部署

```bash
cd infra && cp backend.hcl.example backend.hcl && cp terraform.tfvars.example terraform.tfvars
```

編輯 `backend.hcl`（填入 bucket 名稱）和 `terraform.tfvars`（填入 Email 和 chat id），然後執行：

```bash
cd infra && terraform init -backend-config=backend.hcl
```

第一次 `init` 會產生 `infra/.terraform.lock.hcl`，裡面鎖定 provider 的版本。先補上 CI（Linux）用的雜湊，再把這個檔案 commit 進 git，之後本機、CI 和重建都會用同一版 provider：

```bash
cd infra && terraform providers lock -platform=darwin_arm64 -platform=linux_amd64
```

```bash
make plan
```

確認 plan 列出的資源都正確後，再部署：

```bash
make apply
```

`make apply` 會先檢查三件事，不符合就不部署：在 main、跟 GitHub 的 main 一致、程式碼沒有未 commit 的修改。因為 `make` 編譯的是資料夾裡現在的程式碼，從別的分支或舊的 main 部署，會讓雲端跑的東西跟 main 不一樣，舊版還會安靜地蓋掉新版。真的要緊急部署時才用 `ALLOW_DIRTY=1 make apply`，之後馬上把修正走 PR 進 main。

部署完確認雲端跑的是哪個 commit（下載每個函式的 zip，讀出 Go 編譯時寫進去的 commit，跟 GitHub 的 main 比）：

```bash
make deployed
```

其他看得到版本的地方：日報最後一行「伺服器版本」、擴充功能小視窗最下面，以及每一行 log 的 `commit` 欄位。commit 後面有 `+dirty` 或 `*`，表示編譯時資料夾裡有沒 commit 或沒進 git 的檔案。

部署完成後，信箱會收到 SNS 的訂閱確認信，記得點確認，之後才收得到告警。

告警也會由 alarm 函式轉發到 Telegram（不用確認）。要測試，把任一個告警手動設成 ALARM，Telegram 和信箱都應該收到「🚨 告警」；下一次評估時它會自己回到原本的狀態：

```bash
aws cloudwatch set-alarm-state --alarm-name job-radar-alarm-errors --state-value ALARM --state-reason "手動測試"
```

### 評分準不準：評估集

每個評過分的 JD 都存在 S3 pages 桶的 `jd/`（保存一年），評分紀錄的 `jd` 欄位指向它。拿它們加上你自己的判斷，就能在改評分檔案**之前**先看效果。

1. 部署 2026-10-25 之後的版本後，先執行一次，把更早評過的職缺補上 JD（只找得到 30 天內批次送出的）：

```bash
AWS_PROFILE=radar make backfill-jd
```

2. 在 repo 以外建一個 CSV，例如 `履歷相關/評分標準答案.csv`。第一行固定是 `key,want,note`；`key` 填職缺網址或 `104#代碼`；`want` 填你的判斷：`投`、`看`、`不`，或一個分數：

```
key,want,note
https://www.104.com.tw/job/abc12,投,自家產品的後端
104#def34,不,其實是 MES
```

3. 用現在 SSM 裡的評分檔案重評一次，或加上 `PROFILE=` 測一份草稿：

```bash
AWS_PROFILE=radar make eval LABELS=../履歷相關/評分標準答案.csv PROFILE=../履歷相關/job-radar_評分檔案.md
```

每個職缺會印一行：✅／❌、你的判斷、原本的分數 → 這次的分數、職稱，最後一行是「跟你的判斷一致：原本 x/n → 這次 y/n」和花費。每個職缺一次模型呼叫，約 0.03 美元。`want` 空著的那幾行會跳過，可以邊看邊標。

4. 草稿比較好，就換掉 SSM 裡的評分檔案。它會先列出差異，輸入 `yes` 才更新，不用部署：

```bash
AWS_PROFILE=radar make profile PROFILE=../履歷相關/job-radar_評分檔案.md
```

### 用 GitHub 部署（不用 aws login，也不用打指令）

設定好之後，部署變成在 GitHub 上按按鈕：

1. GitHub → Actions → **deploy** → **Run workflow**（分支 main）。
2. 等 **plan** 那一步跑完，點進去看 Summary：要新增、修改、刪除哪些東西。
3. 沒問題，到 **apply** 那一步按 **Review deployments → Approve**。沒按就不會部署。
4. 跑完，Summary 最後一段就是 `make deployed` 的結果：每個函式跑的是哪個 commit。

另外，每個 PR 只要改到程式或 `infra/`，GitHub 會自動跑一次唯讀的 plan，在 PR 的 Checks → plan → Summary 看得到這個 PR 會改雲端的哪些東西。

**原理**：GitHub Actions 用 OIDC 登入 AWS。每次執行，GitHub 發一張幾分鐘內有效的證明，AWS 只認這個 repo 的；所以 GitHub 上沒有存任何 AWS 金鑰。有兩個角色：

| 角色 | 誰能用 | 能做什麼 |
|---|---|---|
| `job-radar-github-plan` | 這個 repo 的 PR、從 main 執行的 deploy 的 plan 那一步 | 只能讀，而且讀不到四個秘密參數 |
| `job-radar-github-deploy` | 只有 `production` 環境的工作（只能從 main、要你核准） | 改整套服務；IAM 只能動 `job-radar-*` 的角色 |

**一次性設定（照順序）**

1. 先在本機部署一次，把上面兩個角色建出來（它們不存在之前，GitHub 登入不了）：

```bash
AWS_PROFILE=radar make apply
```

2. 建 `production` 環境：GitHub → Settings → Environments → New environment，名字填 `production`：
   - 勾 **Required reviewers**，選你自己；
   - **Deployment branches and tags** 選 Selected branches，加上 `main`。
3. 設 3 個變數（不是秘密，只是名字）：

```bash
gh variable set AWS_PLAN_ROLE_ARN --body "$(terraform -chdir=infra output -json github_roles | jq -r .plan)"
```

```bash
gh variable set AWS_DEPLOY_ROLE_ARN --body "$(terraform -chdir=infra output -json github_roles | jq -r .deploy)"
```

```bash
gh variable set TF_STATE_BUCKET --body "$(sed -n 's/^bucket *= *"\(.*\)"/\1/p' infra/backend.hcl)"
```

4. 設 3 個秘密，值就是 `infra/terraform.tfvars` 裡的那三個。每個指令會等你貼上值再按 Enter，值不會留在指令紀錄裡：

```bash
gh secret set TF_VAR_ALERT_EMAIL
```

```bash
gh secret set TF_VAR_TELEGRAM_CHAT_ID
```

```bash
gh secret set TF_VAR_MAIL_DOMAIN
```

**公開 repo 的注意事項**：這個 repo 是公開的，Actions 的 log 誰都看得到。所以兩個 workflow 都只印 plan 的摘要和資源名稱，不印值；`alert_email`、`telegram_chat_id` 和 `clip_url` 也標成 sensitive，Terraform 不會顯示它們。來自 fork 的 PR 拿不到 AWS 角色，也拿不到秘密。

`make apply` 還是可以用（例如 GitHub 掛了）；兩條路改的是同一份 Terraform 狀態。

## 6. 第一次載入看板，並建立基準線

```bash
for a in greenhouse lever ashby; do aws lambda invoke --function-name job-radar-sync --payload "{\"task\":\"sync-boards\",\"ats\":\"$a\"}" --cli-binary-format raw-in-base64-out /dev/stdout; done
```

第一天的掃描會把每個看板上現有的職缺記為「基準線」，不發通知。從第二天起，只會通知新出現的職缺。

如果想手動測試摘要（只發送，不把職缺標成已通知）：

```bash
aws lambda invoke --function-name job-radar-digest --payload '{"task":"digest","dry_run":true}' --cli-binary-format raw-in-base64-out /dev/stdout
```

## 7. 驗收

- [ ] Mac 關機一整天，08:40 手機照樣收到摘要。
- [ ] CloudWatch 儀表板（網址見 `terraform output dashboard_url`）有圖表。
- [ ] `terraform destroy` 之後再 `make apply`，可以完整重建。重建後要重新做第 6 步。

## 不用的時候

```bash
cd infra && terraform destroy
```

這個指令會刪除全部資源，包括資料表裡的資料。SSM 參數和 state bucket 不在 Terraform 管理範圍內，要另外手動刪除。
