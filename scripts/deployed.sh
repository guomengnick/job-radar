#!/bin/sh
# Prints the commit each job-radar function runs, next to GitHub's main.
# Go writes the commit into the binary when it builds (vcs stamping), so
# this downloads each deployed zip and reads it with `go version -m`: the
# answer comes from the code itself, not from a label that could be stale.
set -eu
region=${AWS_REGION:-ap-southeast-2}
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT

git fetch -q origin main
main=$(git rev-parse --short=7 origin/main)
all_main=yes

printf '%-20s %-8s %-10s %s\n' function commit "vs main" "updated (UTC)"
for f in $(aws lambda list-functions --region "$region" --output text \
	--query "Functions[?starts_with(FunctionName, 'job-radar-')].FunctionName"); do
	set -- $(aws lambda get-function --region "$region" --function-name "$f" --output text \
		--query '[Code.Location, Configuration.LastModified]')
	curl -sf -o "$dir/$f.zip" "$1"
	unzip -q -o "$dir/$f.zip" bootstrap -d "$dir/$f"
	info=$(go version -m "$dir/$f/bootstrap")
	rev=$(printf '%s\n' "$info" | sed -n 's/.*vcs.revision=\(.......\).*/\1/p')
	dirty=$(printf '%s\n' "$info" | sed -n 's/.*vcs.modified=//p')
	if [ "$rev" = "$main" ]; then same="= main"; else same="≠ main"; all_main=no; fi
	[ "$dirty" = true ] && rev="$rev*"
	printf '%-20s %-8s %-10s %s\n' "$f" "${rev:-unknown}" "$same" "$(printf '%s' "$2" | cut -c1-16)"
done
echo
echo "GitHub main: $main    (* = built with uncommitted or untracked files in the checkout)"
if [ "$all_main" = yes ]; then echo "✓ every function runs main"; else echo "✋ some functions do not run main"; fi
