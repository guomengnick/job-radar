#!/bin/sh
# Points the bot's webhook at the clip function's /telegram, so a pressed
# label button (👍 投 / 🤔 看 / 👎 不) reaches the radar. Telegram sends the
# clip secret back in its X-Telegram-Bot-Api-Secret-Token header, and the
# function refuses any request without it. Only button presses are sent
# (allowed_updates). Run once, and again after the clip secret changes.
#
# The bot token travels only in the request URL; it is never printed.
set -eu
region=${AWS_REGION:-ap-southeast-2}
param() {
	aws ssm get-parameter --region "$region" --name "$1" --with-decryption --query Parameter.Value --output text
}
token=$(param "${TELEGRAM_TOKEN_PARAM:-/job-radar/telegram-bot-token}")
secret=$(param "${CLIP_SECRET_PARAM:-/job-radar/clip-secret}")
url="$(terraform -chdir=infra output -raw clip_url)telegram"

curl -sS "https://api.telegram.org/bot$token/setWebhook" \
	--data-urlencode "url=$url" --data-urlencode "secret_token=$secret" \
	--data-urlencode 'allowed_updates=["callback_query"]' 2>&1 | sed "s/$token/<token>/g"
echo
curl -sS "https://api.telegram.org/bot$token/getWebhookInfo" 2>&1 | sed "s/$token/<token>/g" |
	sed 's/"url":"[^"]*"/"url":"<clip_url>telegram"/'
echo
