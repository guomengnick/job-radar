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

部署完成後，信箱會收到 SNS 的訂閱確認信，記得點確認，之後才收得到告警。

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
