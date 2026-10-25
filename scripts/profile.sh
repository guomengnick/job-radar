#!/bin/sh
# Replaces the scoring profile in SSM with a local file, after showing what
# would change and asking, as terraform apply does. Takes effect on the
# next judgement; no deploy. Try a draft with make eval first.
#
#   make profile PROFILE=../履歷相關/job-radar_評分檔案.md
#
# Intelligent-Tiering keeps the parameter in the free standard tier while
# it fits in 4 KB and moves it to the advanced tier (about US$0.05 a month)
# when it grows past that.
set -eu
file=${1:?usage: scripts/profile.sh path/to/profile.md}
name=${PROFILE_PARAM:-/job-radar/assess-profile}
region=${AWS_REGION:-ap-southeast-2}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

aws ssm get-parameter --region "$region" --name "$name" --with-decryption \
	--query Parameter.Value --output text >"$tmp/current"
if diff -u --label "SSM 現在的評分檔案" --label "$file" "$tmp/current" "$file"; then
	echo "沒有差別，不用更新。"
	exit 0
fi
printf '\n%s：%s bytes。\n要用它取代 SSM 裡的評分檔案嗎？輸入 yes：' "$file" "$(wc -c <"$file" | tr -d ' ')"
read -r answer
if [ "$answer" != yes ]; then
	echo "沒有更新。"
	exit 1
fi
aws ssm put-parameter --region "$region" --name "$name" --type SecureString --overwrite \
	--tier Intelligent-Tiering --value "file://$file" >/dev/null
echo "已更新。下一次評分就會用新的評分檔案，不用部署。"
